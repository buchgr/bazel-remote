package cache

import (
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

var blobNameSHA256 = regexp.MustCompile("^/?([a-f0-9]{64})$")

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
	hash, err := blobName(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch m := r.Method; m {
	case http.MethodGet:
		http.ServeFile(w, r, h.filePath(hash))
	case http.MethodPut:
		if !h.ensureSpacer.EnsureSpace(h.cache, r.ContentLength) {
			http.Error(w, "Cache full.", http.StatusInsufficientStorage)
			return
		}
		written, err := h.saveToDisk(r.Body, hash)
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

func blobName(name string) (string, error) {
	m := blobNameSHA256.FindStringSubmatch(name)
	if m == nil {
		msg := fmt.Sprintf("Resource name must be a SHA256 hash in hex. "+
			"Got '%s'.", name)
		return "", errors.New(msg)
	}
	return m[1], nil
}

func (h *httpCache) saveToDisk(content io.Reader, hash string) (written int64,
	err error) {
	f, err := ioutil.TempFile(h.cache.Dir(), "upload")
	if err != nil {
		return 0, err
	}
	tmpName := f.Name()
	written, err = io.Copy(f, content)
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
