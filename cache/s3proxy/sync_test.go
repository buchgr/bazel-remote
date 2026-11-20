package s3proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk"
	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
	testutils "github.com/buchgr/bazel-remote/v2/utils"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const testBucket = "bazel-remote"

// fakeS3 is an in-memory S3 server whose object PUTs can be made to fail
// with a 503, or to hang until the request is cancelled.
type fakeS3 struct {
	srv      *httptest.Server
	failPuts atomic.Int64 // Number of upcoming PUTs to fail; negative fails all.
	hangPuts atomic.Bool
	puts     atomic.Int64 // Object PUTs that reached the server.
	stop     chan struct{}
}

func newFakeS3(t *testing.T) *fakeS3 {
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket(testBucket); err != nil {
		t.Fatal(err)
	}
	f := &fakeS3{stop: make(chan struct{})}
	h := gofakes3.New(backend).Server()
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Count(strings.Trim(r.URL.Path, "/"), "/") > 0 {
			f.puts.Add(1)
			if f.hangPuts.Load() {
				// Reading the body lets the server notice the client
				// going away; stop releases a handler at cleanup.
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case <-r.Context().Done():
				case <-f.stop:
				}
				return
			}
			if n := f.failPuts.Load(); n != 0 {
				if n > 0 {
					f.failPuts.Add(-1)
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	t.Cleanup(func() { close(f.stop) }) // Runs before srv.Close.
	return f
}

func newSyncProxy(t *testing.T, f *fakeS3, maxAttempts int) *syncS3Cache {
	t.Helper()
	p := New(strings.TrimPrefix(f.srv.URL, "http://"), testBucket, minio.BucketLookupPath, "",
		credentials.NewStaticV4("access", "secret", ""), true, false, "us-east-1", 16,
		true, maxAttempts,
		"zstd", testutils.NewSilentLogger(), testutils.NewSilentLogger(), 1, 1)
	sp, ok := p.(*syncS3Cache)
	if !ok {
		t.Fatalf("New with SyncUpload returned %T, want *syncS3Cache", p)
	}
	sp.baseBackoff = time.Millisecond
	sp.maxBackoff = 5 * time.Millisecond
	return sp
}

func newDiskCache(t *testing.T, p cache.Proxy) disk.Cache {
	t.Helper()
	dc, err := disk.New(t.TempDir(), 10<<20, disk.WithProxyBackend(p),
		disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	return dc
}

func blob(s string) ([]byte, string) {
	b := []byte(s)
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:])
}

func TestSyncUploadFailingPutIsNotPresent(t *testing.T) {
	f := newFakeS3(t)
	f.failPuts.Store(-1)
	dc := newDiskCache(t, newSyncProxy(t, f, 3))
	ctx := context.Background()

	for _, kind := range []cache.EntryKind{cache.CAS, cache.AC} {
		data, hash := blob("failing put " + kind.String())
		before := f.puts.Load()
		err := dc.Put(ctx, kind, hash, int64(len(data)), bytes.NewReader(data))
		var cerr *cache.Error
		if !errors.As(err, &cerr) || cerr.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s Put: got %v, want a cache.Error with code 503", kind, err)
		}
		if got := f.puts.Load() - before; got != 3 {
			t.Errorf("%s: %d PUT attempts, want 3", kind, got)
		}
		if ok, _ := dc.Contains(ctx, kind, hash, int64(len(data))); ok {
			t.Errorf("%s: Contains reports a blob whose upload failed", kind)
		}
		if kind == cache.CAS {
			missing, err := dc.FindMissingCasBlobs(ctx, []*pb.Digest{{Hash: hash, SizeBytes: int64(len(data))}})
			if err != nil {
				t.Fatal(err)
			}
			if len(missing) != 1 {
				t.Errorf("FindMissingCasBlobs reports a blob whose upload failed as present")
			}
		}
	}
}

func TestSyncUploadSucceedsAfterTransientFailures(t *testing.T) {
	f := newFakeS3(t)
	f.failPuts.Store(3)
	p := newSyncProxy(t, f, 5)
	dc := newDiskCache(t, p)
	ctx := context.Background()

	data, hash := blob("transient failures")
	if err := dc.Put(ctx, cache.CAS, hash, int64(len(data)), bytes.NewReader(data)); err != nil {
		t.Fatalf("Put after 3 transient failures: %v", err)
	}
	if got := f.puts.Load(); got != 4 {
		t.Errorf("%d PUT attempts, want 4", got)
	}
	// The bucket holds it: a fresh disk cache reads it through the proxy.
	if ok, _ := p.Contains(ctx, cache.CAS, hash, int64(len(data))); !ok {
		t.Fatal("the bucket does not hold the blob after an acknowledged Put")
	}
	rc, size, err := newDiskCache(t, p).Get(ctx, cache.CAS, hash, int64(len(data)), 0)
	if err != nil || rc == nil {
		t.Fatalf("Get from a fresh disk cache: rc=%v err=%v", rc, err)
	}
	defer func() { _ = rc.Close() }()
	if size != int64(len(data)) {
		t.Errorf("size %d, want %d", size, len(data))
	}
}

func TestSyncUploadHonoursContextDeadline(t *testing.T) {
	f := newFakeS3(t)
	f.hangPuts.Store(true)
	dc := newDiskCache(t, newSyncProxy(t, f, 1000))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	data, hash := blob("deadline")
	start := time.Now()
	err := dc.Put(ctx, cache.CAS, hash, int64(len(data)), bytes.NewReader(data))
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Put took %v, want it bounded by the 200ms deadline", elapsed)
	}
	var cerr *cache.Error
	if !errors.As(err, &cerr) || cerr.Code != http.StatusServiceUnavailable {
		t.Fatalf("Put: got %v, want a cache.Error with code 503", err)
	}
	if ok, _ := dc.Contains(context.Background(), cache.CAS, hash, int64(len(data))); ok {
		t.Error("Contains reports a blob whose upload timed out")
	}
}

func TestSyncUploadConcurrentSameDigest(t *testing.T) {
	f := newFakeS3(t)
	dc := newDiskCache(t, newSyncProxy(t, f, 3))
	ctx := context.Background()
	data, hash := blob("concurrent")

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- dc.Put(ctx, cache.CAS, hash, int64(len(data)), bytes.NewReader(data))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := dc.Contains(ctx, cache.CAS, hash, int64(len(data))); !ok {
		t.Error("blob not present after concurrent Puts")
	}
}
