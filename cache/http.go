package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

var blobNameSHA256 = regexp.MustCompile("^/?(ac/|cas/)?([a-f0-9]{64})$")

// HTTPCache ...
type HTTPCache interface {
	Serve()
}

type httpCache struct {
	addr              string
	cache             Cache
	ensureSpacer      EnsureSpacer
	ongoingUploads    map[string]*sync.Mutex
	ongoingUploadsMux *sync.Mutex
}

// NewHTTPCache ...
func NewHTTPCache(listenAddr string, cacheDir string, maxBytes int64, ensureSpacer EnsureSpacer) HTTPCache {
	ensureCacheDir(cacheDir)
	cache := NewCache(cacheDir, maxBytes)
	loadFilesIntoCache(cache)
	return &httpCache{listenAddr, cache, ensureSpacer, make(map[string]*sync.Mutex), &sync.Mutex{}}
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

func loadFilesIntoCache(cache Cache) {
	filepath.Walk(cache.Dir(), func(name string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			cache.AddFile(filepath.Base(name), info.Size())
		}
		return nil
	})
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
		if !h.cache.ContainsFile(hash) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.ServeFile(w, r, h.filePath(hash))
	case http.MethodPut:
		if h.cache.ContainsFile(hash) {
			h.discardUpload(w, r.Body)
			return
		}
		uploadMux := h.startUpload(hash)
		uploadMux.Lock()
		defer h.stopUpload(hash)
		defer uploadMux.Unlock()
		if h.cache.ContainsFile(hash) {
			h.discardUpload(w, r.Body)
			return
		}
		if !h.ensureSpacer.EnsureSpace(h.cache, r.ContentLength) {
			http.Error(w, "The disk is full. File could not be uploaded.",
				http.StatusInsufficientStorage)
			return
		}
		written, err := h.saveToDisk(r.Body, hash, verifyHash)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.cache.AddFile(hash, written)
		w.WriteHeader(http.StatusOK)
	case http.MethodHead:
		if !h.cache.ContainsFile(hash) {
			http.Error(w, err.Error(), http.StatusNotFound)
		}
		w.WriteHeader(http.StatusOK)
	default:
		msg := fmt.Sprintf("Method '%s' not supported.", html.EscapeString(m))
		http.Error(w, msg, http.StatusMethodNotAllowed)
	}
}

func (h *httpCache) startUpload(hash string) *sync.Mutex {
	h.ongoingUploadsMux.Lock()
	defer h.ongoingUploadsMux.Unlock()
	mux, ok := h.ongoingUploads[hash]
	if !ok {
		mux = &sync.Mutex{}
		h.ongoingUploads[hash] = mux
		return mux
	}
	return mux
}

func (h *httpCache) stopUpload(hash string) {
	h.ongoingUploadsMux.Lock()
	defer h.ongoingUploadsMux.Unlock()
	delete(h.ongoingUploads, hash)
}

func (h *httpCache) discardUpload(w http.ResponseWriter, r io.Reader) {
	io.Copy(ioutil.Discard, r)
	w.WriteHeader(http.StatusOK)
}

func parseURL(url string) ([]string, error) {
	m := blobNameSHA256.FindStringSubmatch(url)
	if m == nil {
		msg := fmt.Sprintf("Resource name must be a SHA256 hash in hex. "+
			"Got '%s'.", html.EscapeString(url))
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
				hash, html.EscapeString(actualHash))
			return 0, errors.New(msg)
		}
	} else {
		written, err = io.Copy(f, content)
	}
	if err != nil {
		return 0, err
	}
	err = f.Sync()
	if err != nil {
		log.Fatal(err)
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
