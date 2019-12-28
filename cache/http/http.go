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

type uploadReq struct {
	instanceName string
	hash         string
	kind         cache.EntryKind
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
	errorLogger cache.Logger, instanceName string, hash string, kind cache.EntryKind) {
	rdr, size, err := local.Get(kind, instanceName, hash)
	if err != nil {
		return
	}

	if size == 0 {
		// See https://github.com/golang/go/issues/20257#issuecomment-299509391
		rdr = http.NoBody
	}
	url := requestURL(baseURL, instanceName, hash, kind)
	req, err := http.NewRequest(http.MethodPut, url, rdr)
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = size

	rsp, err := remote.Do(req)
	if err != nil {
		return
	}
	io.Copy(ioutil.Discard, rsp.Body)
	rsp.Body.Close()

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
				uploadFile(remote, baseURL, local, accessLogger, errorLogger, item.instanceName, item.hash, item.kind)
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

func (r *remoteHTTPProxyCache) Put(kind cache.EntryKind, instanceName string, hash string, size int64, rdr io.Reader) error {
	if r.local.Contains(kind, instanceName, hash) {
		io.Copy(ioutil.Discard, rdr)
		return nil
	}
	err := r.local.Put(kind, instanceName, hash, size, rdr)
	if err != nil {
		return err
	}

	select {
	case r.uploadQueue <- &uploadReq{
		instanceName: instanceName,
		hash:         hash,
		kind:         kind,
	}:
	default:
		r.errorLogger.Printf("too many uploads queued")
	}
	return err
}

func (r *remoteHTTPProxyCache) Get(kind cache.EntryKind, instanceName string, hash string) (io.ReadCloser, int64, error) {
	if r.local.Contains(kind, instanceName, hash) {
		return r.local.Get(kind, instanceName, hash)
	}

	url := requestURL(r.baseURL, instanceName, hash, kind)
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

	err = r.local.Put(kind, instanceName, hash, sizeBytes, rsp.Body)
	if err != nil {
		return nil, -1, err
	}

	return r.local.Get(kind, instanceName, hash)
}

func (r *remoteHTTPProxyCache) Contains(kind cache.EntryKind, instanceName string, hash string) bool {
	return r.local.Contains(kind, instanceName, hash)
}

func (r *remoteHTTPProxyCache) MaxSize() int64 {
	return r.local.MaxSize()
}

func (r *remoteHTTPProxyCache) Stats() (currentSize int64, numItems int) {
	return r.local.Stats()
}

func requestURL(baseURL *url.URL, instanceName string, hash string, kind cache.EntryKind) string {
	if kind == cache.CAS || instanceName == "" {
		return fmt.Sprintf("%s/%s/%s", baseURL, kind, hash)
	}
	return fmt.Sprintf("/%s/%s/%s_%s", baseURL, kind, hash, instanceName)
}
