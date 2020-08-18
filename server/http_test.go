package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/disk"
	"github.com/buchgr/bazel-remote/utils"

	pb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/golang/protobuf/proto"
)

func TestDownloadFile(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	blobSize := int64(1024)

	hash, err := testutils.CreateCacheFile(filepath.Join(cacheDir, "cas"), blobSize)
	if err != nil {
		t.Fatal(err)
	}

	c := disk.New(cacheDir, blobSize, nil)
	h := NewHTTPCache(c, testutils.NewSilentLogger(), testutils.NewSilentLogger(), true, "")

	req, err := http.NewRequest("GET", "/cas/"+hash, bytes.NewReader([]byte{}))
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(h.CacheHandler)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Fatal("Handler returned wrong status code",
			"expected", http.StatusOK,
			"got", status,
		)
	}

	rsp := rr.Result()
	if contentLen := rsp.ContentLength; contentLen != blobSize {
		t.Error("GET request returned wrong content length",
			"expected", blobSize,
			"got", contentLen)
	}

	hasher := sha256.New()
	io.Copy(hasher, rsp.Body)
	if actualHash := hex.EncodeToString(hasher.Sum(nil)); actualHash != hash {
		t.Error("Received the wrong content.",
			"expected hash", hash,
			"actualHash", actualHash,
		)
	}

	req, err = http.NewRequest("HEAD", "/cas/"+hash, bytes.NewReader([]byte{}))
	if err != nil {
		t.Fatal(err)
	}
	handler.ServeHTTP(rr, req)
	rsp = rr.Result()
	if contentLen := rsp.ContentLength; contentLen != blobSize {
		t.Error("HEAD request returned wrong content length",
			"expected", blobSize,
			"got", contentLen)
	}
}

func TestUploadFilesConcurrently(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	const NumUploads = 1000

	var requests [NumUploads]*http.Request
	for i := 0; i < NumUploads; i++ {
		data, hash := testutils.RandomDataAndHash(1024)
		r, err := http.NewRequest("PUT", "/cas/"+hash, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		requests[i] = r
	}

	c := disk.New(cacheDir, 1000*1024, nil)
	h := NewHTTPCache(c, testutils.NewSilentLogger(), testutils.NewSilentLogger(), true, "")
	handler := http.HandlerFunc(h.CacheHandler)

	var wg sync.WaitGroup
	wg.Add(len(requests))
	for _, request := range requests {
		go func(request *http.Request) {
			defer wg.Done()
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, request)

			if status := rr.Code; status != http.StatusOK {
				t.Error("Handler returned wrong status code",
					"expected", http.StatusOK,
					"got", status,
				)
			}
		}(request)
	}

	wg.Wait()

	f, err := os.Open(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	files, err := f.Readdir(-1)
	if err != nil {
		t.Fatal(err)
	}
	var totalSize int64
	for _, fileinfo := range files {
		if !fileinfo.IsDir() {
			size := fileinfo.Size()
			if size != 1024 {
				t.Error("Expected all files to be 1024 bytes.", "Got", size)
			}
			totalSize += fileinfo.Size()
		}
	}

	// Test that purging worked and kept cache size in check.
	if upperBound := int64(NumUploads) * 1024 * 1024; totalSize > upperBound {
		t.Error("Cache size too big. Expected at most", upperBound, "got",
			totalSize)
	}
}

func TestUploadSameFileConcurrently(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	data, hash := testutils.RandomDataAndHash(1024)

	c := disk.New(cacheDir, 1024, nil)
	h := NewHTTPCache(c, testutils.NewSilentLogger(), testutils.NewSilentLogger(), true, "")
	handler := http.HandlerFunc(h.CacheHandler)

	var wg sync.WaitGroup
	wg.Add(100)
	for i := 0; i < 100; i++ {
		go func() {
			defer wg.Done()

			rr := httptest.NewRecorder()

			request, err := http.NewRequest("PUT", "/cas/"+hash, bytes.NewReader(data))
			if err != nil {
				t.Error(err)
				return
			}

			handler.ServeHTTP(rr, request)

			if status := rr.Code; status != http.StatusOK {
				resp, _ := ioutil.ReadAll(rr.Body)
				t.Error("Handler returned wrong status code",
					"expected", http.StatusOK,
					"got", status,
					string(resp),
				)
			}
		}()
	}

	wg.Wait()
}

func TestUploadCorruptedFile(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	data, hash := testutils.RandomDataAndHash(1024)
	corruptedData := data[:999]

	r, err := http.NewRequest("PUT", "/cas/"+hash, bytes.NewReader(corruptedData))
	if err != nil {
		t.Fatal(err)
	}

	c := disk.New(cacheDir, 2048, nil)
	h := NewHTTPCache(c, testutils.NewSilentLogger(), testutils.NewSilentLogger(), true, "")
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(h.CacheHandler)
	handler.ServeHTTP(rr, r)

	if status := rr.Code; status != http.StatusInternalServerError {
		t.Error("Handler returned wrong status code",
			"expected", http.StatusInternalServerError,
			"got", status)
	}

	// Check that no file was saved in the cache
	f, err := os.Open(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	entries, err := f.Readdir(-1)
	if err != nil {
		t.Fatal(err)
	}
	for _, fileEntry := range entries {
		if !fileEntry.IsDir() {
			t.Error("Unexpected file in the cache", fileEntry.Name())
		}
	}
}

func TestUploadEmptyActionResult(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	data, hash := testutils.RandomDataAndHash(0)

	r, err := http.NewRequest("PUT", "/ac/"+hash, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	c := disk.New(cacheDir, 2048, nil)
	validate := true
	h := NewHTTPCache(c, testutils.NewSilentLogger(), testutils.NewSilentLogger(), validate, "")
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(h.CacheHandler)
	handler.ServeHTTP(rr, r)

	if status := rr.Code; status != http.StatusOK {
		t.Fatal("Handler returned wrong status code",
			"expected", http.StatusOK,
			"got", status)
	}

	cacheFile := filepath.Join(cacheDir, "ac", hash[:2], hash)
	cachedData, err := ioutil.ReadFile(cacheFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(cachedData) == 0 {
		t.Fatal("expected non-zero length ActionResult to be cached")
	}

	ar := pb.ActionResult{}
	err = proto.Unmarshal(cachedData, &ar)
	if err != nil {
		t.Fatal(err)
	}
	if ar.ExecutionMetadata == nil {
		t.Fatal("expected non-nil ExecutionMetadata")
	}
	ar.ExecutionMetadata = nil

	remarshaled, err := proto.Marshal(&ar)
	if err != nil {
		t.Fatal(err)
	}
	if len(remarshaled) != 0 {
		t.Fatal("expected zero-length blob once the metadata is stripped")
	}
}

func TestEmptyBlobAvailable(t *testing.T) {
	testEmptyBlobAvailable(t, "HEAD")
	testEmptyBlobAvailable(t, "GET")
}

func testEmptyBlobAvailable(t *testing.T, method string) {
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	data, hash := testutils.RandomDataAndHash(0)
	r, err := http.NewRequest(method, "/cas/"+hash, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	c := disk.New(cacheDir, 2048, nil)
	validate := true
	h := NewHTTPCache(c, testutils.NewSilentLogger(), testutils.NewSilentLogger(), validate, "")
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(h.CacheHandler)
	handler.ServeHTTP(rr, r)

	if status := rr.Code; status != http.StatusOK {
		t.Fatal("Handler returned wrong status code for",
			"expected", http.StatusOK,
			"got", status)
	}
}

func TestStatusPage(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	r, err := http.NewRequest("GET", "/status", bytes.NewReader([]byte{}))
	if err != nil {
		t.Fatal(err)
	}

	c := disk.New(cacheDir, 2048, nil)
	h := NewHTTPCache(c, testutils.NewSilentLogger(), testutils.NewSilentLogger(), true, "")
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(h.StatusPageHandler)
	handler.ServeHTTP(rr, r)

	if status := rr.Code; status != http.StatusOK {
		t.Error("StatusPageHandler returned wrong status code",
			"expected", http.StatusOK,
			"got", status)
	}

	var data statusPageData
	err = json.Unmarshal(rr.Body.Bytes(), &data)
	if err != nil {
		t.Fatal(err)
	}

	if numFiles := data.NumFiles; numFiles != 0 {
		t.Error("StatusPageHandler returned wrong number of files",
			"expected", 0,
			"got", numFiles)
	}
}

func TestParseRequestURL(t *testing.T) {
	{
		_, _, err := parseRequestURL("invalid/url", true)
		if err == nil {
			t.Error("Failed to reject an invalid URL")
		}
	}

	const aSha256sum = "fec3be77b8aa0d307ed840581ded3d114c86f36d4914c81e33a72877020c0603"

	{
		kind, hash, err := parseRequestURL("cas/"+aSha256sum, true)
		if err != nil {
			t.Error("Failed to parse a valid CAS URL")
		}
		if hash != aSha256sum {
			t.Error("Cache key parsed incorrectly")
		}
		if kind != cache.CAS {
			t.Errorf("Expected kind CAS but got %s", kind)
		}
	}

	{
		kind, hash, err := parseRequestURL("ac/"+aSha256sum, true)
		if err != nil {
			t.Error("Failed to parse a valid AC URL")
		}
		if hash != aSha256sum {
			t.Error("Cache key parsed incorrectly")
		}
		if kind != cache.AC {
			t.Errorf("Expected kind AC but got %s", kind)
		}
	}

	{
		kind, hash, err := parseRequestURL("prefix/ac/"+aSha256sum, true)
		if err != nil {
			t.Error("Failed to parse a valid AC URL with prefix")
		}
		if hash != aSha256sum {
			t.Error("Cache key parsed incorrectly")
		}
		if kind != cache.AC {
			t.Errorf("Expected kind AC but got %s", kind)
		}
	}

	{
		kind, hash, err := parseRequestURL("prefix/ac/"+aSha256sum, false)
		if err != nil {
			t.Error("Failed to parse a valid AC URL with prefix")
		}
		if hash != aSha256sum {
			t.Error("Cache key parsed incorrectly")
		}
		if kind != cache.RAW {
			t.Errorf("Expected kind RAW but got %s", kind)
		}
	}
}

type fakeResponseWriter struct {
	statusCode *int
	response   string
}

func (r fakeResponseWriter) Header() http.Header {
	return http.Header{}
}

func (r fakeResponseWriter) Write(data []byte) (int, error) {
	r.response = string(data)
	return 0, nil
}

func (r fakeResponseWriter) WriteHeader(statusCode int) {
	*r.statusCode = statusCode
}

func TestRemoteReturnsNotFound(t *testing.T) {
	cacheDir, err := ioutil.TempDir("", "bazel-remote")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(cacheDir)
	emptyCache := disk.New(cacheDir, 1024, nil)

	h := NewHTTPCache(emptyCache, testutils.NewSilentLogger(), testutils.NewSilentLogger(), true, "")
	// create a fake http.Request
	_, hash := testutils.RandomDataAndHash(1024)
	url, _ := url.Parse(fmt.Sprintf("http://localhost:8080/ac/%s", hash))
	reader := bytes.NewReader([]byte{})
	body := ioutil.NopCloser(reader)
	req := &http.Request{
		Method:     "GET",
		URL:        url,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Body:       body,
	}
	statusCode := 0
	respWriter := fakeResponseWriter{
		statusCode: &statusCode,
	}
	h.CacheHandler(respWriter, req)
	if statusCode != http.StatusNotFound {
		t.Errorf("Wrong status code, expected %d, got %d", http.StatusNotFound, statusCode)
	}
}
