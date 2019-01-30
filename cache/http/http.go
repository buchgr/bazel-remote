// Package http is a cache implementation that can proxy artifacts from/to another
// HTTP-based remote cache
package http

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"

	"github.com/buchgr/bazel-remote/cache"
)

const numUploaders = 100
const maxQueuedUploads = 1000000

type Mode int

const (
	Read = iota + 1
	Write
	ReadWrite
)

type uploadReq struct {
	hash string
	kind cache.EntryKind
}

type remoteHTTPProxyCache struct {
	remote       *http.Client
	baseURL      *url.URL
	local        cache.Cache
	mode         Mode
	uploadQueue  chan<- (*uploadReq)
	accessLogger cache.Logger
	errorLogger  cache.Logger
}

func uploadFile(remote *http.Client, baseURL *url.URL, local cache.Cache, accessLogger cache.Logger,
	errorLogger cache.Logger, hash string, kind cache.EntryKind) {
	data, size, err := local.Get(kind, hash)
	if err != nil {
		return
	}

	if size == 0 {
		// See https://github.com/golang/go/issues/20257#issuecomment-299509391
		data = http.NoBody
	}
	url := requestURL(baseURL, hash, kind)
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
func New(baseURL *url.URL, local cache.Cache, remote *http.Client, mode Mode, accessLogger cache.Logger,
	errorLogger cache.Logger) cache.Cache {
	uploadQueue := make(chan *uploadReq, maxQueuedUploads)
	for uploader := 0; uploader < numUploaders; uploader++ {
		go func(remote *http.Client, baseURL *url.URL, local cache.Cache, accessLogger cache.Logger,
			errorLogger cache.Logger) {
			for item := range uploadQueue {
				uploadFile(remote, baseURL, local, accessLogger, errorLogger, item.hash, item.kind)
			}
		}(remote, baseURL, local, accessLogger, errorLogger)
	}
	return &remoteHTTPProxyCache{
		remote:       remote,
		baseURL:      baseURL,
		local:        local,
		mode:         mode,
		uploadQueue:  uploadQueue,
		accessLogger: accessLogger,
		errorLogger:  errorLogger,
	}
}

// Helper function for logging responses
func logResponse(log cache.Logger, method string, code int, url string) {
	log.Printf("%4s %d %15s %s", method, code, "", url)
}

func (r *remoteHTTPProxyCache) Put(kind cache.EntryKind, hash string, size int64, data io.Reader) (err error) {
	if r.local.Contains(kind, hash) {
		io.Copy(ioutil.Discard, data)
		return nil
	}
	r.local.Put(kind, hash, size, data)

	if r.mode&Write == 0 {
		return nil
	}

	select {
	case r.uploadQueue <- &uploadReq{
		hash: hash,
		kind: kind,
	}:
	default:
		r.errorLogger.Printf("too many uploads queued")
	}
	return
}

func (r *remoteHTTPProxyCache) Get(kind cache.EntryKind, hash string) (io.ReadCloser, int64, error) {
	if r.local.Contains(kind, hash) {
		return r.local.Get(kind, hash)
	}

	if r.mode&Read == 0 {
		return nil, 0, nil
	}

	url := requestURL(r.baseURL, hash, kind)
	rsp, err := r.remote.Get(url)
	if err != nil {
		return nil, -1, err
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
		return nil, -1, err
	}
	sizeBytesInt, err := strconv.Atoi(sizeBytesStr)
	if err != nil {
		return nil, -1, err
	}
	sizeBytes := int64(sizeBytesInt)

	err = r.local.Put(kind, hash, sizeBytes, rsp.Body)
	if err != nil {
		return nil, -1, err
	}

	return r.local.Get(kind, hash)
}

func (r *remoteHTTPProxyCache) Contains(kind cache.EntryKind, hash string) bool {
	return r.local.Contains(kind, hash)
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

func requestURL(baseURL *url.URL, hash string, kind cache.EntryKind) string {
	return fmt.Sprintf("%s/%s/%s", baseURL, kind, hash)
}
