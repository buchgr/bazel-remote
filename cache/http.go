package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var blobNameSHA256 = regexp.MustCompile("^/?(.*/)?(ac/|cas/)([a-f0-9]{64})$")

// HTTPCache ...
type HTTPCache interface {
	CacheHandler(w http.ResponseWriter, r *http.Request)
	StatusPageHandler(w http.ResponseWriter, r *http.Request)
}

// The logger interface is designed to be satisfied by log.Logger
type logger interface {
	Printf(format string, v ...interface{})
}

type httpCache struct {
	cache        Cache
	accessLogger logger
	errorLogger  logger
}

type statusPageData struct {
	CurrSize   int64
	MaxSize    int64
	NumFiles   int
	ServerTime int64
}

// NewHTTPCache returns a new instance of the cache.
// accessLogger will print one line for each HTTP request to the cache.
// errorLogger will print unexpected server errors. Inexistent files and malformed URLs will not
// be reported.
func NewHTTPCache(cacheDir string, maxBytes int64, accessLogger logger, errorLogger logger) HTTPCache {
	cache := NewFsCache(cacheDir, maxBytes)

	hc := &httpCache{
		cache:        cache,
		accessLogger: accessLogger,
		errorLogger:  errorLogger,
	}
	return hc
}

// Parse cache artifact information from the request URL
func cacheKeyFromRequestPath(url string) (cacheKey string, sha256sum string, err error) {
	m := blobNameSHA256.FindStringSubmatch(url)
	if m == nil {
		err = errors.New(fmt.Sprintf("Resource name must be a SHA256 hash in hex. "+
			"Got '%s'.", html.EscapeString(url)))
		return
	}

	parts := m[2:]
	if len(parts) != 2 {
		err = errors.New(fmt.Sprintf("The path '%s' is invalid. Expected (ac/|cas/)SHA256.",
			html.EscapeString(url)))
		return
	}

	cacheKey = filepath.Join(parts...)
	if parts[0] == "cas/" {
		sha256sum = parts[1]
	}
	return
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

	// Helper function for logging responses
	logResponse := func(code int) {
		// Parse the client ip:port
		var clientAddress string
		var err error
		clientAddress, _, err = net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			clientAddress = r.RemoteAddr
		}
		h.accessLogger.Printf("%4s %d %15s %s", r.Method, code, clientAddress, r.URL.Path)
	}

	// Extract cache key from request URL
	cacheKey, expectedHash, err := cacheKeyFromRequestPath(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		logResponse(http.StatusBadRequest)
		return
	}

	switch m := r.Method; m {
	case http.MethodGet:
		found, err := h.cache.Get(cacheKey, w)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			h.errorLogger.Printf("get %s: %s", cacheKey, err)
			return
		}

		if !found {
			w.WriteHeader(http.StatusNotFound)
			logResponse(http.StatusNotFound)
			return
		}

		w.WriteHeader(http.StatusOK)
		logResponse(http.StatusOK)
	case http.MethodPut:
		if r.ContentLength == 0 {
			// We need the content-length header to make sure we have enough disk space.
			msg := "put without content-length header"
			http.Error(w, msg, http.StatusBadRequest)
			h.errorLogger.Printf("%s", msg)
			return
		}

		err := h.cache.Put(cacheKey, r.ContentLength, expectedHash, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			h.errorLogger.Printf("put %s: %s", cacheKey, err)
			return
		}

		w.WriteHeader(http.StatusOK)
		logResponse(http.StatusOK)
	case http.MethodHead:
		ok, err := h.cache.Contains(cacheKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			h.errorLogger.Printf("head %s: %s", cacheKey, err)
			return
		}

		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			logResponse(http.StatusNotFound)
			return
		}

		w.WriteHeader(http.StatusOK)
		logResponse(http.StatusOK)
	default:
		msg := fmt.Sprintf("Method '%s' not supported.", html.EscapeString(m))
		http.Error(w, msg, http.StatusMethodNotAllowed)
		logResponse(http.StatusMethodNotAllowed)
	}
}

// Produce a debugging page with some stats about the cache.
func (h *httpCache) StatusPageHandler(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	enc.Encode(statusPageData{
		CurrSize:   h.cache.CurrentSize(),
		MaxSize:    h.cache.MaxSize(),
		NumFiles:   h.cache.NumFiles(),
		ServerTime: time.Now().Unix(),
	})
}
