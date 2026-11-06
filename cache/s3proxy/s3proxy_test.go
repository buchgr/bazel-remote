package s3proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
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

// mock bucket with request counters
type testBucket struct {
	srv *httptest.Server

	mu       sync.Mutex
	objects  map[string][]byte
	requests int
	heads    int
	puts     int
	copies   []objectCopy
}

// One copy the bucket was asked to make, as its destination key and its `bucket/key` source.
type objectCopy struct {
	destination string
	source      string
}

func (b *testBucket) handler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/"+testBucketName+"/")

	b.mu.Lock()
	defer b.mu.Unlock()
	b.requests++

	switch r.Method {
	case http.MethodHead:
		b.heads++
		data, ok := b.objects[key]
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		// minio rejects a stat response without a parseable one, and calls the object absent.
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("ETag", `"`+key+`"`)
	case http.MethodGet:
		data, ok := b.objects[key]
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
	case http.MethodPost:
		// Refreshing an object's timestamp is a copy onto itself, which minio performs as a
		// multipart upload: initiate, copy the single part, complete.
		if r.URL.Query().Has("uploads") {
			b.reply(w, `<InitiateMultipartUploadResult><UploadId>1</UploadId></InitiateMultipartUploadResult>`)
			return
		}
		if r.URL.Query().Has("uploadId") {
			b.reply(w, `<CompleteMultipartUploadResult><ETag>"1"</ETag></CompleteMultipartUploadResult>`)
			return
		}
		http.Error(w, "unsupported POST "+r.URL.RawQuery, http.StatusBadRequest)
	case http.MethodPut:
		// One part of that copy, which names its source in a header and carries no body.
		if source := r.Header.Get("x-amz-copy-source"); source != "" {
			b.copies = append(b.copies, objectCopy{destination: key, source: source})
			b.reply(w, `<CopyPartResult><ETag>"1"</ETag></CopyPartResult>`)
			return
		}
		b.puts++
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusInternalServerError)
			return
		}
		b.objects[key] = data
	default:
		http.Error(w, "unsupported method "+r.Method, http.StatusBadRequest)
	}
}

func (b *testBucket) reply(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(body))
}

const testBucketName = "bazel-remote"

func newTestBucket(t *testing.T, updateTimestamps bool) (*testBucket, cache.Proxy) {
	t.Helper()

	bucket := &testBucket{objects: make(map[string][]byte)}
	bucket.srv = httptest.NewServer(http.HandlerFunc(bucket.handler))
	t.Cleanup(bucket.srv.Close)

	endpoint, err := url.Parse(bucket.srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	proxy := New(
		endpoint.Host,
		testBucketName,
		minio.BucketLookupPath,
		"",
		credentials.New(&credentials.Static{}), // unsigned, so the body arrives unframed
		true,                                   // DisableSSL, because httptest serves plain http
		updateTimestamps,
		"us-east-1", // set, so minio does not ask the bucket where it lives
		1,
		"zstd",
		testutils.NewSilentLogger(),
		testutils.NewSilentLogger(),
		1, 10,
	)

	return bucket, proxy
}

// Uploads are queued and the caller is never told when one finishes, so wait for the bucket to stop
// getting requests. Don't wait on a particular expected count, as the counts are what we want to assert.
func (b *testBucket) awaitQuiet(t *testing.T) {
	t.Helper()

	settled := 0
	for previous := -1; settled < 3; time.Sleep(50 * time.Millisecond) {
		b.mu.Lock()
		requests := b.requests
		b.mu.Unlock()
		if requests == previous {
			settled++
		} else {
			settled = 0
		}
		previous = requests
	}
}

func TestUploadSkipsExistingObject(t *testing.T) {
	ctx := context.Background()
	data, hash := testutils.RandomDataAndHash(256)

	bucket, proxy := newTestBucket(t, false)

	// A blob several actions produce is offered once per action. Only the first should be sent.
	for range 3 {
		proxy.Put(ctx, cache.CAS, hash, int64(len(data)), int64(len(data)),
			io.NopCloser(bytes.NewReader(data)))
	}
	bucket.awaitQuiet(t)

	bucket.mu.Lock()
	defer bucket.mu.Unlock()
	if bucket.puts != 1 {
		t.Errorf("uploaded the same blob %d times, want 1", bucket.puts)
	}
	if bucket.heads != 3 {
		t.Errorf("asked the bucket %d times whether it had the blob, want 3", bucket.heads)
	}
	if got := bucket.objects[objectKeyV2("", hash, cache.CAS)]; !bytes.Equal(got, data) {
		t.Errorf("stored %d bytes under %s, want the %d offered", len(got), hash, len(data))
	}
}

func TestUploadRefreshesSkippedObjectWithUpdateTimestamps(t *testing.T) {
	ctx := context.Background()
	data, hash := testutils.RandomDataAndHash(256)

	bucket, proxy := newTestBucket(t, true)

	proxy.Put(ctx, cache.CAS, hash, int64(len(data)), int64(len(data)),
		io.NopCloser(bytes.NewReader(data)))
	bucket.awaitQuiet(t)

	// Skipping the upload still marks the object as refreshed
	proxy.Put(ctx, cache.CAS, hash, int64(len(data)), int64(len(data)),
		io.NopCloser(bytes.NewReader(data)))
	bucket.awaitQuiet(t)

	bucket.mu.Lock()
	defer bucket.mu.Unlock()
	if bucket.puts != 1 {
		t.Errorf("uploaded the blob %d times, want 1", bucket.puts)
	}
	// With update_timestamps set, a skipped upload must still copy the object onto itself, which
	// is how this backend renews an object's age for a lifecycle rule.
	key := objectKeyV2("", hash, cache.CAS)
	want := []objectCopy{{destination: key, source: testBucketName + "/" + key}}
	if !slices.Equal(bucket.copies, want) {
		t.Errorf("refreshed %v, want %v", bucket.copies, want)
	}
}
