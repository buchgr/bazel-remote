package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk"
	testutils "github.com/buchgr/bazel-remote/v2/utils"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
	"google.golang.org/protobuf/proto"
)

func TestDownloadFile(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer func() { _ = os.RemoveAll(cacheDir) }()

	blobSize := int64(1024)

	data, hash := testutils.RandomDataAndHash(blobSize)

	// Add some overhead for likely CAS blob storage expansion.
	cacheSize := blobSize*2 + disk.BlockSize

	c, err := disk.New(cacheDir, cacheSize, disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	h := NewHTTPCache(c, testutils.NewSilentLogger(), testutils.NewSilentLogger(), true, false, false, false, "", "", math.MaxInt64)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(h.CacheHandler)

	pr := httptest.NewRequest("PUT", "/cas/"+hash, bytes.NewReader(data))
	handler.ServeHTTP(rr, pr)

	gr := httptest.NewRequest("GET", "/cas/"+hash, nil)
	handler.ServeHTTP(rr, gr)

	if status := rr.Code; status != http.StatusOK {
		t.Fatal("Handler returned wrong status code for", hash,
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
	_, err = io.Copy(hasher, rsp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if actualHash := hex.EncodeToString(hasher.Sum(nil)); actualHash != hash {
		t.Error("Received the wrong content.",
			"expected hash", hash,
			"actualHash", actualHash,
		)
	}

	hr := httptest.NewRequest("HEAD", "/cas/"+hash, nil)
	handler.ServeHTTP(rr, hr)
	rsp = rr.Result()
	if contentLen := rsp.ContentLength; contentLen != blobSize {
		t.Error("HEAD request returned wrong content length",
			"expected", blobSize,
			"got", contentLen)
	}
}

func TestUploadFilesConcurrently(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer func() { _ = os.RemoveAll(cacheDir) }()

	const NumUploads = 1000
	const blobSize = 1024

	var requests [NumUploads]*http.Request
	for i := 0; i < NumUploads; i++ {
		data, hash := testutils.RandomDataAndHash(blobSize)
		r := httptest.NewRequest("PUT", "/cas/"+hash, bytes.NewReader(data))
		requests[i] = r
	}

	// Add some overhead for likely CAS blob storage expansion.
	cacheSize := int64(NumUploads * blobSize * 2)

	c, err := disk.New(cacheDir, cacheSize, disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	h := NewHTTPCache(c, testutils.NewSilentLogger(), testutils.NewSilentLogger(), true, false, false, false, "", "", math.MaxInt64)
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
	defer func() { _ = f.Close() }()
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
	defer func() { _ = os.RemoveAll(cacheDir) }()

	data, hash := testutils.RandomDataAndHash(1024)

	numWorkers := 100

	// Add some overhead for likely CAS blob storage expansion.
	cacheSize := int64(len(data) * numWorkers * 2)

	c, err := disk.New(cacheDir, cacheSize, disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	h := NewHTTPCache(c, testutils.NewSilentLogger(), testutils.NewSilentLogger(), true, false, false, false, "", "", math.MaxInt64)
	handler := http.HandlerFunc(h.CacheHandler)

	var wg sync.WaitGroup
	wg.Add(numWorkers)
	for i := 0; i < numWorkers; i++ {
		go func() {
			defer wg.Done()

			request := httptest.NewRequest("PUT", "/cas/"+hash, bytes.NewReader(data))

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, request)

			if status := rr.Code; status != http.StatusOK {
				resp, _ := io.ReadAll(rr.Body)
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
	defer func() { _ = os.RemoveAll(cacheDir) }()

	data, hash := testutils.RandomDataAndHash(1024)
	corruptedData := data[:999]

	r := httptest.NewRequest("PUT", "/cas/"+hash, bytes.NewReader(corruptedData))

	c, err := disk.New(cacheDir, 2048, disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	h := NewHTTPCache(c, testutils.NewSilentLogger(), testutils.NewSilentLogger(), true, false, false, false, "", "", math.MaxInt64)
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
	defer func() { _ = f.Close() }()
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
	defer func() { _ = os.RemoveAll(cacheDir) }()

	data, hash := testutils.RandomDataAndHash(0)

	r := httptest.NewRequest("PUT", "/ac/"+hash, bytes.NewReader(data))

	c, err := disk.New(cacheDir, disk.BlockSize, disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	validate := true
	mangle := false
	checkClientCertForReads := false
	checkClientCertForWrites := false
	h := NewHTTPCache(c, testutils.NewSilentLogger(), testutils.NewSilentLogger(), validate, mangle, checkClientCertForReads, checkClientCertForWrites, "", "", math.MaxInt64)
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(h.CacheHandler)
	handler.ServeHTTP(rr, r)

	if status := rr.Code; status != http.StatusOK {
		t.Fatal("Handler returned wrong status code",
			"expected", http.StatusOK,
			"got", status)
	}

	getReq := httptest.NewRequest("GET", "/ac/"+hash, nil)
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, getReq)

	cachedData, err := io.ReadAll(rr2.Result().Body)
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
	defer func() { _ = os.RemoveAll(cacheDir) }()

	data, hash := testutils.RandomDataAndHash(0)
	r := httptest.NewRequest(method, "/cas/"+hash, bytes.NewReader(data))

	c, err := disk.New(cacheDir, 2048, disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	validate := true
	mangle := false
	checkClientCertForReads := false
	checkClientCertForWrites := false
	h := NewHTTPCache(c, testutils.NewSilentLogger(), testutils.NewSilentLogger(), validate, mangle, checkClientCertForReads, checkClientCertForWrites, "", "", math.MaxInt64)
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(h.CacheHandler)
	handler.ServeHTTP(rr, r)

	if status := rr.Code; status != http.StatusOK {
		t.Fatal("Handler returned wrong status code for method",
			method,
			"expected", http.StatusOK,
			"got", status)
	}
}

func TestStatusPage(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer func() { _ = os.RemoveAll(cacheDir) }()

	r := httptest.NewRequest("GET", "/status", nil)

	c, err := disk.New(cacheDir, 2048, disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	h := NewHTTPCache(c, testutils.NewSilentLogger(), testutils.NewSilentLogger(), true, false, false, false, "", "", math.MaxInt64)
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
		_, _, _, err := parseRequestURL("invalid/url", true)
		if err == nil {
			t.Error("Failed to reject an invalid URL")
		}
	}

	const aSha256sum = "fec3be77b8aa0d307ed840581ded3d114c86f36d4914c81e33a72877020c0603"

	{
		kind, hash, instance, err := parseRequestURL("cas/"+aSha256sum, true)
		if err != nil {
			t.Error("Failed to parse a valid CAS URL")
		}
		if hash != aSha256sum {
			t.Error("Cache key parsed incorrectly")
		}
		if kind != cache.CAS {
			t.Errorf("Expected kind CAS but got %s", kind)
		}
		if instance != "" {
			t.Errorf("Expected empty instance, got %s", instance)
		}
	}

	{
		kind, hash, instance, err := parseRequestURL("ac/"+aSha256sum, true)
		if err != nil {
			t.Error("Failed to parse a valid AC URL")
		}
		if hash != aSha256sum {
			t.Error("Cache key parsed incorrectly")
		}
		if kind != cache.AC {
			t.Errorf("Expected kind AC but got %s", kind)
		}
		if instance != "" {
			t.Errorf("Expected empty instance, got %s", instance)
		}
	}

	{
		kind, hash, instance, err := parseRequestURL("prefix/ac/"+aSha256sum, true)
		if err != nil {
			t.Error("Failed to parse a valid AC URL with prefix")
		}
		if hash != aSha256sum {
			t.Error("Cache key parsed incorrectly")
		}
		if kind != cache.AC {
			t.Errorf("Expected kind AC but got %s", kind)
		}
		if instance != "prefix" {
			t.Errorf("Expected instance \"prefix\", got %s", instance)
		}
	}

	{
		kind, hash, instance, err := parseRequestURL("prefix/ac/"+aSha256sum, false)
		if err != nil {
			t.Error("Failed to parse a valid AC URL with prefix")
		}
		if hash != aSha256sum {
			t.Error("Cache key parsed incorrectly")
		}
		if kind != cache.RAW {
			t.Errorf("Expected kind RAW but got %s", kind)
		}
		if instance != "prefix" {
			t.Errorf("Expected instance \"prefix\", got %s", instance)
		}
	}

	{
		kind, hash, instance, err := parseRequestURL("prefix/with/slashes/ac/"+aSha256sum, false)
		if err != nil {
			t.Error("Failed to parse a valid AC URL with prefix containing slashes")
		}
		if hash != aSha256sum {
			t.Error("Cache key parsed incorrectly")
		}
		if kind != cache.RAW {
			t.Errorf("Expected kind RAW but got %s", kind)
		}
		if instance != "prefix/with/slashes" {
			t.Errorf("Expected instance \"prefix/with/slashes\", got %s", instance)
		}
	}
}

type fakeResponseWriter struct {
	statusCode *int
	response   string
}

func (r *fakeResponseWriter) Header() http.Header {
	return http.Header{}
}

func (r *fakeResponseWriter) Write(data []byte) (int, error) {
	r.response = string(data)
	return 0, nil
}

func (r *fakeResponseWriter) WriteHeader(statusCode int) {
	*r.statusCode = statusCode
}

func TestRemoteReturnsNotFound(t *testing.T) {
	cacheDir, err := os.MkdirTemp("", "bazel-remote")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(cacheDir) }()
	emptyCache, err := disk.New(cacheDir, 1024, disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	h := NewHTTPCache(emptyCache, testutils.NewSilentLogger(), testutils.NewSilentLogger(), true, false, false, false, "", "", math.MaxInt64)
	// create a fake http.Request
	_, hash := testutils.RandomDataAndHash(1024)
	url, _ := url.Parse(fmt.Sprintf("http://localhost:8080/ac/%s", hash))
	reader := bytes.NewReader([]byte{})
	body := io.NopCloser(reader)
	req := &http.Request{
		Method:     "GET",
		URL:        url,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Body:       body,
	}
	statusCode := 0
	respWriter := &fakeResponseWriter{
		statusCode: &statusCode,
	}
	h.CacheHandler(respWriter, req)
	if statusCode != http.StatusNotFound {
		t.Errorf("Wrong status code, expected %d, got %d", http.StatusNotFound, statusCode)
	}
}

func TestManglingACKeys(t *testing.T) {
	cacheDir, err := os.MkdirTemp("", "bazel-remote")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(cacheDir) }()

	blobSize := int64(1024)
	cacheSize := blobSize*2 + disk.BlockSize
	diskCache, err := disk.New(cacheDir, cacheSize, disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	h := NewHTTPCache(diskCache, testutils.NewSilentLogger(), testutils.NewSilentLogger(), false, true, false, false, "", "", math.MaxInt64)
	// create a fake http.Request
	data, hash := testutils.RandomDataAndHash(blobSize)
	err = diskCache.Put(context.Background(), cache.RAW, hash, int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	url, _ := url.Parse(fmt.Sprintf("http://localhost:8080/ac/%s", hash))
	reader := bytes.NewReader([]byte{})
	body := io.NopCloser(reader)
	req := &http.Request{
		Method:     "GET",
		URL:        url,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Body:       body,
	}
	statusCode := 0
	respWriter := &fakeResponseWriter{
		statusCode: &statusCode,
	}
	h.CacheHandler(respWriter, req)
	if statusCode != 0 {
		t.Errorf("Wrong status code, expected %d, got %d", 0, statusCode)
	}

	url, _ = url.Parse(fmt.Sprintf("http://localhost:8080/test-instance/ac/%s", hash))
	reader.Reset([]byte{})
	_ = body.Close()
	body = io.NopCloser(reader)
	req.URL = url
	req.Body = body
	statusCode = 0

	h.CacheHandler(respWriter, req)
	if statusCode != http.StatusNotFound {
		t.Errorf("Wrong status code, expected %d, got %d", http.StatusNotFound, statusCode)
	}
}

func TestResponseLog(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer func() { _ = os.RemoveAll(cacheDir) }()

	blobSize := int64(1024)

	data, hash := testutils.RandomDataAndHash(blobSize)

	// Add some overhead for likely CAS blob storage expansion.
	cacheSize := blobSize*2 + disk.BlockSize

	c, err := disk.New(cacheDir, cacheSize, disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	var w bytes.Buffer
	logger := log.New(&w, "bz-remote", 0)
	h := NewHTTPCache(c, logger, testutils.NewSilentLogger(), true, false, false, false, "", "", math.MaxInt64)

	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(h.CacheHandler)

	pr := httptest.NewRequest("PUT", "/cas/"+hash, bytes.NewReader(data))
	pr.Header.Add("X-Forwarded-For", "10.11.12.13")
	handler.ServeHTTP(rr, pr)

	logLine := w.String()
	if !strings.Contains(logLine, "10.11.12.13") {
		t.Errorf("expected logged IP to use X-Forwarded-For header but saw `%s`", logLine)
	}
}
