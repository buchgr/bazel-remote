package s3proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchgr/bazel-remote/v2/cache"
	testutils "github.com/buchgr/bazel-remote/v2/utils"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestObjectKey(t *testing.T) {
	testCases := []struct {
		prefix     string
		key        string
		kind       cache.EntryKind
		expectedV1 string
		expectedV2 string
	}{
		{"", "1234", cache.CAS, "cas/12/1234", "cas.v2/12/1234"},
		{"test", "1234", cache.CAS, "test/cas/12/1234", "test/cas.v2/12/1234"},
		{"foo/bar/grok", "1234", cache.CAS, "foo/bar/grok/cas/12/1234", "foo/bar/grok/cas.v2/12/1234"},
		{"", "1234", cache.AC, "ac/12/1234", "ac/12/1234"},
		{"", "1234", cache.RAW, "raw/12/1234", "raw/12/1234"},
		{"foo/bar", "1234", cache.AC, "foo/bar/ac/12/1234", "foo/bar/ac/12/1234"},
	}

	for _, tc := range testCases {
		result := objectKeyV2(tc.prefix, tc.key, tc.kind)
		if result != tc.expectedV2 {
			t.Errorf("objectKeyV2 did not match. (result: '%s' expected: '%s'",
				result, tc.expectedV2)
		}

		result = objectKeyV1(tc.prefix, tc.key, tc.kind)
		if result != tc.expectedV1 {
			t.Errorf("objectKeyV1 did not match. (result: '%s' expected: '%s'",
				result, tc.expectedV1)
		}
	}
}

const testBucketName = "bazel-remote"

// fakeBucket serves HEAD, GET and copy-onto-itself requests, and records the
// keys that were copied.
type fakeBucket struct {
	mu       sync.Mutex
	modified map[string]time.Time
	copied   []string
}

func (b *fakeBucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/"+testBucketName+"/")

	b.mu.Lock()
	defer b.mu.Unlock()

	modified, ok := b.modified[key]
	if !ok {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	switch {
	case r.Method == http.MethodPut && r.Header.Get("x-amz-copy-source") != "":
		b.copied = append(b.copied, key)
		b.modified[key] = time.Now()
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<CopyObjectResult><ETag>"1"</ETag></CopyObjectResult>`)
	case r.Method == http.MethodHead || r.Method == http.MethodGet:
		w.Header().Set("Content-Length", "4")
		w.Header().Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", `"1"`)
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, "data")
		}
	default:
		http.Error(w, "unsupported", http.StatusBadRequest)
	}
}

func (b *fakeBucket) copiedKeys(t *testing.T) []string {
	t.Helper()

	// Refreshes are queued, so give the workers a moment to drain.
	time.Sleep(200 * time.Millisecond)

	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.copied...)
}

func newTestProxy(t *testing.T, bucket *fakeBucket, casRefreshAge time.Duration) cache.Proxy {
	t.Helper()

	srv := httptest.NewServer(bucket)
	t.Cleanup(srv.Close)

	endpoint, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	return New(
		endpoint.Host,
		testBucketName,
		minio.BucketLookupPath,
		"",
		credentials.New(&credentials.Static{}),
		true,  // DisableSSL
		false, // UpdateTimestamps
		casRefreshAge,
		"us-east-1",
		1,
		"uncompressed",
		testutils.NewSilentLogger(),
		testutils.NewSilentLogger(),
		1, 10,
	)
}

func TestCASRefreshAge(t *testing.T) {
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)

	oldCAS := objectKeyV1("", "aaaa", cache.CAS)
	newCAS := objectKeyV1("", "bbbb", cache.CAS)
	oldAC := objectKeyV1("", "cccc", cache.AC)

	bucket := &fakeBucket{modified: map[string]time.Time{
		oldCAS: old,
		newCAS: time.Now(),
		oldAC:  old,
	}}
	proxy := newTestProxy(t, bucket, 24*time.Hour)

	for _, hash := range []string{"aaaa", "bbbb"} {
		if ok, _ := proxy.Contains(ctx, cache.CAS, hash, 4); !ok {
			t.Fatalf("expected CAS blob %s to exist", hash)
		}
	}
	if ok, _ := proxy.Contains(ctx, cache.AC, "cccc", -1); !ok {
		t.Fatal("expected AC entry to exist")
	}

	// Only the CAS blob older than casRefreshAge is refreshed. AC entries are
	// never refreshed, or they would outlive the blobs they reference.
	copied := bucket.copiedKeys(t)
	if len(copied) != 1 || copied[0] != oldCAS {
		t.Fatalf("refreshed %v, want [%s]", copied, oldCAS)
	}

	// The refresh reset the blob's age, so a second lookup does not copy it again.
	_, _ = proxy.Contains(ctx, cache.CAS, "aaaa", 4)
	if copied := bucket.copiedKeys(t); len(copied) != 1 {
		t.Fatalf("refreshed %v, want one refresh", copied)
	}
}

func TestCASRefreshAgeOnGet(t *testing.T) {
	oldCAS := objectKeyV1("", "aaaa", cache.CAS)
	bucket := &fakeBucket{modified: map[string]time.Time{
		oldCAS: time.Now().Add(-48 * time.Hour),
	}}
	proxy := newTestProxy(t, bucket, 24*time.Hour)

	rc, _, err := proxy.Get(context.Background(), cache.CAS, "aaaa", 4)
	if err != nil || rc == nil {
		t.Fatalf("expected CAS blob to be fetched, got err %v", err)
	}
	_ = rc.Close()

	if copied := bucket.copiedKeys(t); len(copied) != 1 || copied[0] != oldCAS {
		t.Fatalf("refreshed %v, want [%s]", copied, oldCAS)
	}
}

func TestCASRefreshAgeDisabled(t *testing.T) {
	bucket := &fakeBucket{modified: map[string]time.Time{
		objectKeyV1("", "aaaa", cache.CAS): time.Now().Add(-48 * time.Hour),
	}}
	proxy := newTestProxy(t, bucket, 0)

	_, _ = proxy.Contains(context.Background(), cache.CAS, "aaaa", 4)

	if copied := bucket.copiedKeys(t); len(copied) != 0 {
		t.Fatalf("refreshed %v with cas_refresh_age disabled", copied)
	}
}
