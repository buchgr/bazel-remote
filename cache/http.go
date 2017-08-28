package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
)

var blobNameSHA256 = regexp.MustCompile("^/?(actioncache/|cas/)?([a-f0-9]{64})$")

// HTTPCache ...
type HTTPCache interface {
	Serve()
}

type httpCache struct {
	addr         string
	cache        Cache
	ensureSpacer EnsureSpacer
}

// NewHTTPCache ...
func NewHTTPCache(listenAddr string, cacheDir string, maxBytes int64, ensureSpacer EnsureSpacer) HTTPCache {
	ensureCacheDir(cacheDir)
	initialSize := directorySize(cacheDir)
	cache := NewCache(cacheDir, maxBytes, initialSize)
	return &httpCache{listenAddr, cache, ensureSpacer}
}

// Serve ...
func (h *httpCache) Serve() {
	s := &http.Server{
		Addr:    h.addr,
		Handler: h,
	}
	log.Fatal(s.ListenAndServe())
}

func ensureCacheDir(path string) {
	d, err := os.Open(path)
	if err != nil {
		err := os.MkdirAll(path, os.FileMode(0644))
		if err != nil {
			log.Fatal(err)
		}
	}
	d.Close()
}

func directorySize(path string) (size int64) {
	size = 0
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size
}

func (h *httpCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts, err := parseURL(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var hash string
	var verifyHash bool
	if len(parts) == 1 {
		// For backwards compatibiliy with older Bazel version's that don't
		// support {cas,actioncache} prefixes.
		verifyHash = false
		hash = parts[0]
	} else {
		verifyHash = parts[0] == "cas/"
		hash = parts[1]
	}

	switch m := r.Method; m {
	case http.MethodGet:
		http.ServeFile(w, r, h.filePath(hash))
	case http.MethodPut:
		if !h.ensureSpacer.EnsureSpace(h.cache, r.ContentLength) {
			http.Error(w, "Cache full.", http.StatusInsufficientStorage)
			return
		}
		written, err := h.saveToDisk(r.Body, hash, verifyHash)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			// TODO: Fix if the same file is uploaded twice, the size is incorrect.
			h.cache.AddCurrSize(written)
			w.WriteHeader(http.StatusOK)
		}
	case http.MethodHead:
		// TODO: Use a map or so
		f, err := os.Open(h.filePath(hash))
		if err != nil {
			if os.IsNotExist(err) {
				http.Error(w, err.Error(), http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		f.Close()
		w.WriteHeader(http.StatusOK)
	default:
		msg := fmt.Sprintf("Method '%s' not supported.", m)
		http.Error(w, msg, http.StatusMethodNotAllowed)
	}
}

func parseURL(url string) ([]string, error) {
	m := blobNameSHA256.FindStringSubmatch(url)
	if m == nil {
		msg := fmt.Sprintf("Resource name must be a SHA256 hash in hex. "+
			"Got '%s'.", url)
		return nil, errors.New(msg)
	}
	return m[1:], nil
}

func (h *httpCache) saveToDisk(content io.Reader, hash string, verifyHash bool) (written int64, err error) {
	f, err := ioutil.TempFile(h.cache.Dir(), "upload")
	if err != nil {
		return 0, err
	}
	tmpName := f.Name()
	if verifyHash {
		hasher := sha256.New()
		written, err = io.Copy(io.MultiWriter(f, hasher), content)
		actualHash := hex.EncodeToString(hasher.Sum(nil))
		if hash != actualHash {
			os.Remove(tmpName)
			msg := fmt.Sprintf("Hashes don't match. Provided '%s', Actual '%s'.",
				hash, actualHash)
			return 0, errors.New(msg)
		}
	} else {
		written, err = io.Copy(f, content)
	}
	if err != nil {
		return 0, err
	}
	f.Close()
	err2 := os.Rename(tmpName, h.filePath(hash))
	if err2 != nil {
		return 0, err2
	}
	return written, nil
}

func (h httpCache) filePath(hash string) string {
	return fmt.Sprintf("%s%c%s", h.cache.Dir(), os.PathSeparator, hash)
}
