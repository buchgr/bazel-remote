package http

import (
	"bytes"
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

	kindMap := s.ac
	if fields[1] == "ac" {
		kindMap = s.ac
	} else if fields[1] == "cas" {
		kindMap = s.cas
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
		w.Write(data)

	case http.MethodPut:
		data, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusInternalServerError)
		}
		kindMap[hash] = data

	case http.MethodHead:
		data, ok := kindMap[hash]
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(int64(len(data)), 10))
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

	proxyCache := New(url, &http.Client{}, accessLogger, errorLogger)
	diskCacheSize := int64(len(casData) + 1024)
	diskCache := disk.New(cacheDir, diskCacheSize, proxyCache)

	// PUT two different values with the same key in ac and cas.

	err = diskCache.Put(cache.AC, hash, int64(len(acData)), bytes.NewReader(acData))
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

	err = diskCache.Put(cache.CAS, hash, int64(len(casData)), bytes.NewReader(casData))
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
		if !bytes.Equal(v, casData) {
			t.Fatal("Proxied CAS value does not match")
		}
	}
	s.mu.Unlock()

	// Confirm that we can HEAD both values succesfully.

	var found bool
	var size int64

	found, size = diskCache.Contains(cache.AC, hash)
	if !found {
		t.Fatalf("Expected to find AC item %s", hash)
	}
	if size != int64(len(acData)) {
		t.Fatalf("Expected to find AC item with size %d, got %d",
			len(acData), size)
	}

	found, size = diskCache.Contains(cache.CAS, hash)
	if !found {
		t.Fatalf("Expected to find CAS item %s", hash)
	}
	if size != int64(len(casData)) {
		t.Fatalf("Expected to find CAS item with size %d, got %d",
			len(casData), size)
	}

	// Confirm that we can GET both values succesfully.

	var data []byte
	var rc io.ReadCloser

	rc, size, err = diskCache.Get(cache.AC, hash)
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

	rc, size, err = diskCache.Get(cache.CAS, hash)
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

	diskCache = disk.New(cacheDir2, diskCacheSize, proxyCache)

	_, numItems := diskCache.Stats()
	if numItems != 0 {
		t.Fatalf("Expected an empty disk cache, found %d items", numItems)
	}

	// Confirm that we can HEAD both values succesfully.

	found, size = diskCache.Contains(cache.AC, hash)
	if !found {
		t.Fatalf("Expected to find AC item %s", hash)
	}
	if size != int64(len(acData)) {
		t.Fatalf("Expected to find AC item with size %d, got %d",
			len(acData), size)
	}

	found, size = diskCache.Contains(cache.CAS, hash)
	if !found {
		t.Fatalf("Expected to find CAS item %s", hash)
	}
	if size != int64(len(casData)) {
		t.Fatalf("Expected to find CAS item with size %d, got %d",
			len(casData), size)
	}

	// Confirm that we can GET both values succesfully.

	rc, size, err = diskCache.Get(cache.AC, hash)
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

	rc, size, err = diskCache.Get(cache.CAS, hash)
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
