package httpproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/disk"
	"github.com/buchgr/bazel-remote/cache/disk/casblob"
	"github.com/buchgr/bazel-remote/cache/disk/zstdimpl"
	testutils "github.com/buchgr/bazel-remote/utils"
)

type testServer struct {
	srv *httptest.Server

	mu  sync.Mutex
	ac  map[string][]byte
	cas map[string][]byte
}

func (s *testServer) handler(w http.ResponseWriter, r *http.Request) {

	fields := strings.Split(r.URL.Path, "/")

	kind := fields[1]
	var kindMap map[string][]byte
	if kind == "ac" {
		kindMap = s.ac
	} else if kind == "cas.v2" {
		kindMap = s.cas
	} else {
		msg := fmt.Sprintf("unsupported URL: %q", r.URL.Path)
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	hash := fields[2]

	s.mu.Lock()
	defer s.mu.Unlock()

	switch method := r.Method; method {
	case http.MethodGet:
		data, ok := kindMap[hash]
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
		return

	case http.MethodPut:
		data, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusInternalServerError)
			return
		}
		kindMap[hash] = data
		return

	case http.MethodHead:
		data, ok := kindMap[hash]
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(int64(len(data)), 10))
		return
	}
}

func newTestServer() *testServer {
	ts := testServer{
		ac:  make(map[string][]byte),
		cas: make(map[string][]byte),
	}
	ts.srv = httptest.NewServer(http.HandlerFunc(ts.handler))

	return &ts
}

func TestEverything(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := newTestServer()
	defer s.srv.Close()

	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	logFlags := log.Ldate | log.Ltime | log.LUTC
	accessLogger := log.New(os.Stdout, "", logFlags)
	errorLogger := log.New(os.Stderr, "", logFlags)

	var err error

	url, err := url.Parse(s.srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	casData, hash := testutils.RandomDataAndHash(1024)
	t.Log("cas HASH:", hash)
	acData := []byte{1, 2, 3, 4}

	proxyCache, err := New(url, "zstd", &http.Client{}, accessLogger, errorLogger, 100, 10000)
	if err != nil {
		t.Fatal(err)
	}

	diskCacheSize := int64(len(casData) + disk.BlockSize)
	diskCache, err := disk.New(cacheDir, diskCacheSize, disk.WithProxyBackend(proxyCache), disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	// PUT two different values with the same key in ac and cas.

	err = diskCache.Put(ctx, cache.AC, hash, int64(len(acData)), bytes.NewReader(acData))
	if err != nil {
		t.Error(err)
	}

	time.Sleep(time.Second)
	s.mu.Lock()
	if len(s.ac) != 1 {
		t.Fatalf("Expected 1 item in the AC cache on the backend, found: %d",
			len(s.ac))
	}
	if len(s.cas) != 0 {
		t.Fatalf("Expected 0 items in the CAS cache on the backend, found: %d",
			len(s.cas))
	}
	s.mu.Unlock()

	err = diskCache.Put(ctx, cache.CAS, hash, int64(len(casData)), bytes.NewReader(casData))
	if err != nil {
		t.Error(err)
	}

	time.Sleep(time.Second)
	s.mu.Lock()
	if len(s.ac) != 1 {
		t.Fatalf("Expected 1 item in the AC cache on the backend, found: %d",
			len(s.ac))
	}
	if len(s.cas) != 1 {
		t.Fatalf("Expected 1 item in the CAS cache on the backend, found: %d",
			len(s.cas))
	}

	for _, v := range s.ac {
		if !bytes.Equal(v, acData) {
			t.Fatal("Proxied AC value does not match")
		}
	}

	for _, v := range s.cas {

		// TODO: tweak the GetUncompressedReadCloser API to accept more than os.File.

		tmpfile, err := ioutil.TempFile("", "bazel-remote-httpproxy-test")
		if err != nil {
			t.Fatal(err)
		}
		tfn := tmpfile.Name()
		defer os.Remove(tfn)

		_, err = io.Copy(tmpfile, bytes.NewReader(v))
		if err != nil {
			t.Fatal(err)
		}
		tmpfile2, err := os.Open(tfn)
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(tmpfile2.Name())

		var zi zstdimpl.ZstdImpl
		zi, err = zstdimpl.Get("go")
		if err != nil {
			t.Fatal(err)
		}

		rc, err := casblob.GetUncompressedReadCloser(
			zi, tmpfile2, int64(len(casData)), 0)
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		vData, err := ioutil.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(vData, casData) {
			t.Fatal("Proxied CAS value does not match")
		}
	}

	s.mu.Unlock()

	// Confirm that we can HEAD both values successfully.

	var found bool
	var size int64

	found, size = diskCache.Contains(ctx, cache.AC, hash, int64(len(acData)))
	if !found {
		t.Fatalf("Expected to find AC item %s", hash)
	}
	if size != int64(len(acData)) {
		t.Fatalf("Expected to find AC item with size %d, got %d",
			len(acData), size)
	}

	found, size = diskCache.Contains(ctx, cache.CAS, hash, int64(len(casData)))
	if !found {
		t.Fatalf("Expected to find CAS item %s", hash)
	}
	if size != int64(len(casData)) {
		t.Fatalf("Expected to find CAS item with size %d, got %d",
			len(casData), size)
	}

	// Confirm that we can GET both values successfully.

	var data []byte
	var rc io.ReadCloser

	rc, size, err = diskCache.Get(ctx, cache.AC, hash, int64(len(acData)), 0)
	if err != nil {
		t.Error(err)
	}

	if size != int64(len(acData)) {
		t.Fatalf("Expected to find AC item with size %d, got %d",
			len(acData), size)
	}

	data, err = ioutil.ReadAll(rc)
	if err != nil {
		t.Error(err)
	}

	if !bytes.Equal(data, acData) {
		t.Error("Different AC data returned")
	}
	rc.Close()

	rc, size, err = diskCache.Get(ctx, cache.CAS, hash, int64(len(casData)), 0)
	if err != nil {
		t.Error(err)
	}

	if size != int64(len(casData)) {
		t.Fatalf("Expected to find CAS item with size %d, got %d",
			len(casData), size)
	}

	data, err = ioutil.ReadAll(rc)
	if err != nil {
		t.Error(err)
	}

	if !bytes.Equal(data, casData) {
		t.Error("Different CAS data returned")
	}
	rc.Close()

	// Create a new empty cache, and check that we can fill it
	// from the backend.

	cacheDir2 := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir2)

	diskCache, err = disk.New(cacheDir2, diskCacheSize, disk.WithProxyBackend(proxyCache), disk.WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	_, _, numItems, _ := diskCache.Stats()
	if numItems != 0 {
		t.Fatalf("Expected an empty disk cache, found %d items", numItems)
	}

	// Confirm that we can HEAD both values successfully.

	found, size = diskCache.Contains(ctx, cache.AC, hash, int64(len(acData)))
	if !found {
		t.Fatalf("Expected to find AC item %s", hash)
	}
	if size != int64(len(acData)) {
		t.Fatalf("Expected to find AC item with size %d, got %d",
			len(acData), size)
	}

	found, size = diskCache.Contains(ctx, cache.CAS, hash, int64(len(casData)))
	if !found {
		t.Fatalf("Expected to find CAS item %s", hash)
	}
	if size != -1 && size != int64(len(casData)) {
		t.Fatalf("Expected to find CAS item with size %d, got %d",
			len(casData), size)
	}

	// Confirm that we can GET both values successfully.

	rc, size, err = diskCache.Get(ctx, cache.AC, hash, int64(len(acData)), 0)
	if err != nil {
		t.Error(err)
	}

	if size != int64(len(acData)) {
		t.Fatalf("Expected to find AC item with size %d, got %d",
			len(acData), size)
	}

	data, err = ioutil.ReadAll(rc)
	if err != nil {
		t.Error(err)
	}

	if !bytes.Equal(data, acData) {
		t.Error("Different AC data returned")
	}
	rc.Close()

	rc, size, err = diskCache.Get(ctx, cache.CAS, hash, int64(len(casData)), 0)
	if err != nil {
		t.Error(err)
	}

	if size != int64(len(casData)) {
		t.Fatalf("Expected to find CAS item with size %d, got %d",
			len(casData), size)
	}

	data, err = ioutil.ReadAll(rc)
	if err != nil {
		t.Error(err)
	}

	if !bytes.Equal(data, casData) {
		t.Error("Different CAS data returned")
	}
	rc.Close()
}
