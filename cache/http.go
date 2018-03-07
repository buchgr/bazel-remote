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

var blobNameSHA256 = regexp.MustCompile("^/?(.*/)?(ac/|cas/)([a-f0-9]{64})$")

// HTTPCache ...
type HTTPCache interface {
	CacheHandler(w http.ResponseWriter, r *http.Request)
}

type httpCache struct {
	cache             Cache
	ensureSpacer      EnsureSpacer
	ongoingUploads    map[string]*sync.Mutex
	ongoingUploadsMux *sync.Mutex
}

// NewHTTPCache ...
func NewHTTPCache(cacheDir string, maxBytes int64, ensureSpacer EnsureSpacer) HTTPCache {
	ensureDirExists(filepath.Join(cacheDir, "ac"))
	ensureDirExists(filepath.Join(cacheDir, "cas"))
	cache := NewCache(cacheDir, maxBytes)
	cache.LoadExistingFiles()
	return &httpCache{cache, ensureSpacer, make(map[string]*sync.Mutex), &sync.Mutex{}}
}

type cacheItem struct {
	hash        string
	absFilePath string // Absolute filesystem path
	verifyHash  bool   // true for CAS items, false for AC items
}

// Parse cache artifact information from the request URL
func cacheItemFromRequestPath(url string, baseDir string) (*cacheItem, error) {
	m := blobNameSHA256.FindStringSubmatch(url)
	if m == nil {
		msg := fmt.Sprintf("Resource name must be a SHA256 hash in hex. "+
			"Got '%s'.", html.EscapeString(url))
		return nil, errors.New(msg)
	}

	parts := m[2:]
	if len(parts) != 2 {
		msg := fmt.Sprintf("The path '%s' is invalid. Expected (ac/|cas/)SHA256.",
			html.EscapeString(url))
		return nil, errors.New(msg)
	}

	return &cacheItem{
		verifyHash:  parts[0] == "cas/",
		absFilePath: filepath.Join(baseDir, parts[0], parts[1]),
		hash:        parts[1],
	}, nil
}

func ensureDirExists(path string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		err = os.MkdirAll(path, os.FileMode(0744))
		if err != nil {
			log.Fatal(err)
		}
	}
}

func (h *httpCache) CacheHandler(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	cacheItem, err := cacheItemFromRequestPath(r.URL.Path, h.cache.Dir())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch m := r.Method; m {
	case http.MethodGet:
		if !h.cache.ContainsFile(cacheItem.absFilePath) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		http.ServeFile(w, r, cacheItem.absFilePath)
	case http.MethodPut:
		if h.cache.ContainsFile(cacheItem.absFilePath) {
			h.discardUpload(w, r.Body)
			return
		}
		uploadMux := h.startUpload(cacheItem.absFilePath)
		uploadMux.Lock()
		defer h.stopUpload(cacheItem.absFilePath)
		defer uploadMux.Unlock()
		if h.cache.ContainsFile(cacheItem.absFilePath) {
			h.discardUpload(w, r.Body)
			return
		}
		if !h.ensureSpacer.EnsureSpace(h.cache, r.ContentLength) {
			http.Error(w, "The disk is full. File could not be uploaded.",
				http.StatusInsufficientStorage)
			return
		}
		written, err := h.saveToDisk(r.Body, *cacheItem)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.cache.AddFile(cacheItem.absFilePath, written)
		w.WriteHeader(http.StatusOK)
	case http.MethodHead:
		if !h.cache.ContainsFile(cacheItem.absFilePath) {
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

func (h *httpCache) saveToDisk(content io.Reader, info cacheItem) (written int64, err error) {
	f, err := ioutil.TempFile(h.cache.Dir(), "upload")
	if err != nil {
		return 0, err
	}
	tmpName := f.Name()
	if info.verifyHash {
		hasher := sha256.New()
		written, err = io.Copy(io.MultiWriter(f, hasher), content)
		actualHash := hex.EncodeToString(hasher.Sum(nil))
		if info.hash != actualHash {
			os.Remove(tmpName)
			msg := fmt.Sprintf("Hashes don't match. Provided '%s', Actual '%s'.",
				info.hash, html.EscapeString(actualHash))
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

	// Rename to the final path
	err2 := os.Rename(tmpName, info.absFilePath)
	if err2 != nil {
		log.Printf("Failed renaming %s to its final destination %s: %v", tmpName, info.absFilePath, err2)
		// Last-ditch attempt to delete the temporary file. No need to report
		// this failure.
		err := os.Remove(info.absFilePath)
		if err != nil {
			log.Printf("Failed cleaning up %s after a failure to rename it to its final destination: %v", tmpName, err)
		}
		return 0, err2
	}
	return written, nil
}
