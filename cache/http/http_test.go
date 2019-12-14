package http

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/disk"
	"github.com/buchgr/bazel-remote/utils"
)

func TestProxyReadWorks(t *testing.T) {
	// Test that reading a blob from a proxy works and also populates the local
	// disk cache.

	expectedData := []byte("hello world")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(expectedData)
	}))

	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)
	diskCache := disk.New(cacheDir, 100)

	baseURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Error(err)
	}

	proxy := New(baseURL, diskCache, &http.Client{}, testutils.NewSilentLogger(),
		testutils.NewSilentLogger())

	hashBytes := sha256.Sum256(expectedData)
	hash := hex.EncodeToString(hashBytes[:])
	if diskCache.Contains(cache.CAS, hash) {
		t.Fatalf("Expected the local cache to be empty")
	}

	readBytes, actualSizeBytes, err := proxy.Get(cache.CAS, hash)
	if err != nil {
		t.Fatalf("Failed to get the blob via the http proxy: '%v'", err)
	}

	actualData, err := ioutil.ReadAll(readBytes)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Compare(actualData, expectedData) != 0 {
		t.Fatalf("Expected '%v' but received '%v", actualData, expectedData)
	}

	if actualSizeBytes != int64(len(expectedData)) {
		t.Fatalf("Expected '%d' bytes of expected data, but received '%d'", actualSizeBytes,
			len(expectedData))
	}

	if !diskCache.Contains(cache.CAS, hash) {
		t.Fatalf("Expected the blob to be cached locally.")
	}
}

func TestProxyWriteWorks(t *testing.T) {
	// Test that writing to the proxy works and also populates the local
	// disk cache.

	data := []byte("hello world")
	hashBytes := sha256.Sum256(data)
	hash := hex.EncodeToString(hashBytes[:])

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		if !strings.Contains(r.URL.Path, hash) {
			http.Error(w, fmt.Sprintf("Expected the request URL to contain the key '%s' but was '%s'",
				hash, r.URL.Path), http.StatusInternalServerError)
			return
		}

		actualData, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("Expected '%v' but received '%v'", data, actualData),
				http.StatusInternalServerError)
			return
		}
	}))

	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)
	diskCache := disk.New(cacheDir, 100)

	baseURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Error(err)
	}

	proxy := New(baseURL, diskCache, &http.Client{}, testutils.NewSilentLogger(),
		testutils.NewSilentLogger())

	if diskCache.Contains(cache.CAS, hash) {
		t.Fatalf("Expected the local cache to be empty")
	}

	err = proxy.Put(cache.CAS, hash, int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Errorf("Failed to write to the proxy: '%v'", err)
	}

	if !diskCache.Contains(cache.CAS, hash) {
		t.Fatalf("Expected the local cache to contain '%s'", hash)
	}
}

func TestProxyReadErrorsArePropagated(t *testing.T) {
	// Test that if the proxy errors, the error is passed through to
	// the client.

	expectedData := []byte("hello world")
	hashBytes := sha256.Sum256(expectedData)
	hash := hex.EncodeToString(hashBytes[:])

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Foo bar error", http.StatusForbidden)
	}))

	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)
	diskCache := disk.New(cacheDir, 100)

	baseURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Error(err)
	}

	proxy := New(baseURL, diskCache, &http.Client{}, testutils.NewSilentLogger(),
		testutils.NewSilentLogger())

	_, _, err = proxy.Get(cache.CAS, hash)
	if cerr, ok := err.(*cache.Error); ok {
		if cerr.Code != http.StatusForbidden {
			t.Errorf("Expected error code '%d' but got '%d'", http.StatusForbidden, cerr.Code)
		}
		if strings.Compare(cerr.Text, "Foo bar error\n") != 0 {
			t.Errorf("Expected error text 'Foo bar error' but got '%s'", cerr.Text)
		}
	} else {
		t.Error("Expected the proxy read to have failed with a CacheError")
	}
}

func TestProxyWriteErrorsAreNotPropagated(t *testing.T) {
	// Test that when there is an error writing to the remote proxy
	// then the error is not propagated to the client. This is because
	// the writes to the proxy happen asynchronously and on a best effort
	// basis.

	data := []byte("hello world")
	hashBytes := sha256.Sum256(data)
	hash := hex.EncodeToString(hashBytes[:])

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Foo bar error", http.StatusForbidden)
	}))

	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)
	diskCache := disk.New(cacheDir, 100)

	baseURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Error(err)
	}

	proxy := New(baseURL, diskCache, &http.Client{}, testutils.NewSilentLogger(),
		testutils.NewSilentLogger())

	err = proxy.Put(cache.CAS, hash, int64(len(data)), bytes.NewReader(data))
	if err != nil {
		t.Error("Expected the error on put to not be propagated")
	}

	if !diskCache.Contains(cache.CAS, hash) {
		t.Error("Expected the blob to be stored locally")
	}
}

func TestProxyLocalPutFailuresNotRelayed(t *testing.T) {
	// Test that when there is an error writing to the remote proxy
	// then the error is not propagated to the client. This is because
	// the writes to the proxy happen asynchronously and on a best effort
	// basis.

	data := []byte("hello world")
	hashBytes := sha256.Sum256(data)
	hash := hex.EncodeToString(hashBytes[:])

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Size miss-matched Put request should not have been forwarded")
	}))

	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)
	diskCache := disk.New(cacheDir, 100)

	baseURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Error(err)
	}

	proxy := New(baseURL, diskCache, &http.Client{}, testutils.NewSilentLogger(),
		testutils.NewSilentLogger())

	err = proxy.Put(cache.AC, hash, int64(len(data)+1), bytes.NewReader(data))
	if err == nil {
		t.Error("Expected Put to error with size miss-match")
	}

	if diskCache.Contains(cache.AC, hash) {
		t.Error("Expected not to be stored locally")
	}
}
