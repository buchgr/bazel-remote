// A Cache implementation that can read and write through artifacts to another remote http cache.
package http

import (
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/buchgr/bazel-remote/cache"
)

const numUploaders = 100
const maxQueuedUploads = 1000000

type uploadReq struct {
	key string
	// true if it's an action cache entry.
	ac bool
}

type remoteHTTPProxyCache struct {
	remote       *http.Client
	baseURL      *url.URL
	local        cache.Cache
	uploadQueue  chan<- (*uploadReq)
	accessLogger cache.Logger
	errorLogger  cache.Logger
}

func uploadFile(remote *http.Client, baseURL *url.URL, local cache.Cache, accessLogger cache.Logger,
	errorLogger cache.Logger, key string, ac bool) {
	data, size, err := local.Get(key, ac)
	if err != nil {
		return
	}

	if size == 0 {
		// See https://github.com/golang/go/issues/20257#issuecomment-299509391
		data = http.NoBody
	}
	url := requestURL(baseURL, key, ac)
	req, err := http.NewRequest(http.MethodPut, url, data)
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = size

	rsp, err := remote.Do(req)
	if err != nil {
		return
	}
	logResponse(accessLogger, "PUT", rsp.StatusCode, url)
	return
}

// New creates a cache that proxies requests to a HTTP remote cache.
func New(baseURL *url.URL, local cache.Cache, remote *http.Client, accessLogger cache.Logger,
	errorLogger cache.Logger) cache.Cache {
	uploadQueue := make(chan *uploadReq, maxQueuedUploads)
	for uploader := 0; uploader < numUploaders; uploader++ {
		go func(remote *http.Client, baseURL *url.URL, local cache.Cache, accessLogger cache.Logger,
			errorLogger cache.Logger) {
			for item := range uploadQueue {
				uploadFile(remote, baseURL, local, accessLogger, errorLogger, item.key, item.ac)
			}
		}(remote, baseURL, local, accessLogger, errorLogger)
	}
	return &remoteHTTPProxyCache{
		remote:       remote,
		baseURL:      baseURL,
		local:        local,
		uploadQueue:  uploadQueue,
		accessLogger: accessLogger,
		errorLogger:  errorLogger,
	}
}

// Helper function for logging responses
func logResponse(log cache.Logger, method string, code int, url string) {
	log.Printf("%4s %d %15s %s", method, code, "", url)
}

func (r *remoteHTTPProxyCache) Put(key string, size int64, expectedSha256 string, data io.Reader) (err error) {
	actionCache := expectedSha256 == ""
	if r.local.Contains(key, actionCache) {
		io.Copy(ioutil.Discard, data)
		return nil
	}
	r.local.Put(key, size, expectedSha256, data)

	select {
	case r.uploadQueue <- &uploadReq{
		key: key,
		ac:  actionCache,
	}:
	default:
		r.errorLogger.Printf("too many uploads queued")
	}
	return
}

func (r *remoteHTTPProxyCache) Get(key string, actionCache bool) (data io.ReadCloser, sizeBytes int64, err error) {
	if r.local.Contains(key, actionCache) {
		return r.local.Get(key, actionCache)
	}

	url := requestURL(r.baseURL, key, actionCache)
	rsp, err := r.remote.Get(url)
	if err != nil {
		return
	}
	defer rsp.Body.Close()

	logResponse(r.accessLogger, "GET", rsp.StatusCode, url)

	if rsp.StatusCode != http.StatusOK {
		// If the failed http response contains some data then
		// forward up to 1 KiB.
		errorBytes, err := ioutil.ReadAll(io.LimitReader(rsp.Body, 1024))
		var errorText string
		if err == nil {
			errorText = string(errorBytes)
		}

		return nil, -1, &cache.Error{
			Code: rsp.StatusCode,
			Text: errorText,
		}
	}

	sizeBytesStr := rsp.Header.Get("Content-Length")
	if sizeBytesStr == "" {
		err = errors.New("Missing Content-Length header")
		return
	}
	sizeBytesInt, err := strconv.Atoi(sizeBytesStr)
	if err != nil {
		return
	}
	sizeBytes = int64(sizeBytesInt)

	err = r.local.Put(key, sizeBytes, "", rsp.Body)
	if err != nil {
		return
	}

	return r.local.Get(key, actionCache)
}

func (r *remoteHTTPProxyCache) Contains(key string, actionCache bool) (ok bool) {
	return r.local.Contains(key, actionCache)
}

func (r *remoteHTTPProxyCache) MaxSize() int64 {
	return r.local.MaxSize()
}

func (r *remoteHTTPProxyCache) CurrentSize() int64 {
	return r.local.CurrentSize()
}

func (r *remoteHTTPProxyCache) NumItems() int {
	return r.local.NumItems()
}

func requestURL(baseURL *url.URL, key string, actionCache bool) string {
	url := baseURL.String()
	if !strings.HasSuffix(url, "/") {
		url += "/"
	}
	url += key
	return url
}
