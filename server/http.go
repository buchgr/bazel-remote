package server

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/buchgr/bazel-remote/cache"
)

var blobNameSHA256 = regexp.MustCompile("^/?(.*/)?(ac/|cas/)([a-f0-9]{64})$")

// HTTPCache ...
type HTTPCache interface {
	CacheHandler(w http.ResponseWriter, r *http.Request)
	StatusPageHandler(w http.ResponseWriter, r *http.Request)
}

type httpCache struct {
	cache        cache.Cache
	accessLogger cache.Logger
	errorLogger  cache.Logger
}

type statusPageData struct {
	CurrSize   int64
	MaxSize    int64
	NumFiles   int
	ServerTime int64
}

// NewHTTPCache returns a new instance of the cache.
// accessLogger will print one line for each HTTP request to stdout.
// errorLogger will print unexpected server errors. Inexistent files and malformed URLs will not
// be reported.
func NewHTTPCache(cache cache.Cache, accessLogger cache.Logger, errorLogger cache.Logger) HTTPCache {
	errorLogger.Printf("Loaded %d existing disk cache items.", cache.NumItems())

	hc := &httpCache{
		cache:        cache,
		accessLogger: accessLogger,
		errorLogger:  errorLogger,
	}
	return hc
}

// Parse cache artifact information from the request URL
func parseRequestURL(url string) (cache.EntryKind, string, error) {
	m := blobNameSHA256.FindStringSubmatch(url)
	if m == nil {
		err := fmt.Errorf("resource name must be a SHA256 hash in hex. "+
			"got '%s'", html.EscapeString(url))
		return 0, "", err
	}

	parts := m[2:]
	if len(parts) != 2 {
		err := fmt.Errorf("the path '%s' is invalid. expected (ac/|cas/)sha256",
			html.EscapeString(url))
		return 0, "", err
	}

	// The regex ensures that parts[0] can only be "ac/" or "cas/"
	hash := parts[1]
	if parts[0] == "cas/" {
		return cache.CAS, hash, nil
	}
	return cache.AC, hash, nil
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

	kind, hash, err := parseRequestURL(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		logResponse(http.StatusBadRequest)
		return
	}

	switch m := r.Method; m {
	case http.MethodGet:
		data, sizeBytes, err := h.cache.Get(kind, hash)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			h.errorLogger.Printf("GET %s: %s", path(kind, hash), err)
			return
		}

		if data == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			logResponse(http.StatusNotFound)
			return
		}
		defer data.Close()

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(sizeBytes, 10))
		io.Copy(w, data)

		logResponse(http.StatusOK)
	case http.MethodPut:
		if r.ContentLength == -1 {
			// We need the content-length header to make sure we have enough disk space.
			msg := fmt.Sprintf("PUT without Content-Length (key = %s)", path(kind, hash))
			http.Error(w, msg, http.StatusBadRequest)
			h.errorLogger.Printf("%s", msg)
			return
		}

		err := h.cache.Put(kind, hash, r.ContentLength, r.Body)
		if err != nil {
			if cerr, ok := err.(*cache.Error); ok {
				http.Error(w, err.Error(), cerr.Code)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			h.errorLogger.Printf("PUT %s: %s", path(kind, hash), err)
		} else {
			logResponse(http.StatusOK)
		}

	case http.MethodHead:
		ok := h.cache.Contains(kind, hash)
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
		NumFiles:   h.cache.NumItems(),
		ServerTime: time.Now().Unix(),
	})
}

func path(kind cache.EntryKind, hash string) string {
	if kind == cache.AC {
		return "/ac/" + hash
	}
	return "/cas/" + hash
}
