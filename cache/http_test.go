package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
)

func TestDownloadFile(t *testing.T) {
	cacheDir := createTmpDir(t)
	defer os.RemoveAll(cacheDir)

	hash := createRandomFile(cacheDir, 1024)

	e := NewEnsureSpacer(1, 1)
	h := NewHTTPCache(cacheDir, 1024, e)

	req, err := http.NewRequest("GET", "/cas/"+hash, nil)
	if err != nil {
		t.Error(err)
	}
	rr := httptest.NewRecorder()
	handler := http.HandlerFunc(h.CacheHandler)
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Error("Handler returned wrong status code",
			"expected", http.StatusOK,
			"got", status,
		)
	}

	rsp := rr.Result()
	if contentLen := rsp.ContentLength; contentLen != 1024 {
		t.Error("Handler returned file with wrong content length",
			"expected", 1024,
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
}

func TestUploadFilesConcurrently(t *testing.T) {
	cacheDir := createTmpDir(t)
	defer os.RemoveAll(cacheDir)

	const NumUploads = 1000

	var requests [NumUploads]*http.Request
	for i := 0; i < NumUploads; i++ {
		data, hash := randomDataAndHash(1024)
		r, err := http.NewRequest("PUT", "/cas/"+hash, bytes.NewReader(data))
		if err != nil {
			t.Error(err)
		}
		requests[i] = r
	}

	e := NewEnsureSpacer(0.3, 0.1)
	h := NewHTTPCache(cacheDir, 10000*1024, e)
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
}
