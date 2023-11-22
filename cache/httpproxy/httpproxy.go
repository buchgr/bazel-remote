// Package httpproxy is a cache implementation that can proxy artifacts
// from/to another HTTP-based remote cache.
package httpproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk/casblob"
	"github.com/buchgr/bazel-remote/v2/cache/hashing"
	"github.com/buchgr/bazel-remote/v2/utils/backendproxy"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type remoteHTTPProxyCache struct {
	remote       *http.Client
	baseURL      string
	uploadQueue  chan<- backendproxy.UploadReq
	accessLogger cache.Logger
	errorLogger  cache.Logger
	requestURL   func(hash string, kind cache.EntryKind) string
	v2mode       bool
}

var (
	cacheHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bazel_remote_http_cache_hits",
		Help: "The total number of HTTP backend cache hits",
	})
	cacheMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bazel_remote_http_cache_misses",
		Help: "The total number of HTTP backend cache misses",
	})
)

func (r *remoteHTTPProxyCache) UploadFile(item backendproxy.UploadReq) {

	if item.LogicalSize == 0 {
		item.Rc.Close()
		// See https://github.com/golang/go/issues/20257#issuecomment-299509391
		item.Rc = http.NoBody
	}

	url := r.requestURL(item.Hash, item.Kind)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodHead, url, nil)
	if err != nil {
		r.errorLogger.Printf("INTERNAL ERROR, FAILED TO SETUP HTTP PROXY UPLOAD %s: %s", url, err)
		item.Rc.Close()
		return
	}

	rsp, err := r.remote.Do(req)
	if err == nil && rsp.StatusCode == http.StatusOK {
		r.accessLogger.Printf("SKIP UPLOAD %s", item.Hash)
		item.Rc.Close()
		return
	}

	req, err = http.NewRequestWithContext(context.Background(), http.MethodPut, url, item.Rc)
	if err != nil {
		r.errorLogger.Printf("INTERNAL ERROR, FAILED TO SETUP HTTP PROXY UPLOAD %s: %s", url, err)

		// item.Rc will be closed if we call req.Do(), but not if we
		// return earlier.
		item.Rc.Close()

		return
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = item.SizeOnDisk

	req.Header.Set("X-Digest-Function", item.Hasher.DigestFunction().String())

	rsp, err = r.remote.Do(req)
	if err != nil {
		r.errorLogger.Printf("HTTP %s UPLOAD: %s", url, err.Error())
		return
	}
	_, err = io.Copy(io.Discard, rsp.Body)
	if err != nil {
		r.errorLogger.Printf("HTTP %s UPLOAD: %s", url, err.Error())
		return
	}
	rsp.Body.Close()

	logResponse(r.accessLogger, "UPLOAD", rsp.StatusCode, url)
}

// New creates a cache that proxies requests to a HTTP remote cache.
// `storageMode` must be one of "uncompressed" (which expects legacy
// CAS blobs) or "zstd" (which expects cas.v2 blobs).
func New(baseURL *url.URL, storageMode string, remote *http.Client,
	accessLogger cache.Logger, errorLogger cache.Logger,
	numUploaders, maxQueuedUploads int) (cache.Proxy, error) {

	proxy := &remoteHTTPProxyCache{
		remote:       remote,
		baseURL:      strings.TrimRight(baseURL.String(), "/"),
		accessLogger: accessLogger,
		errorLogger:  errorLogger,
		v2mode:       storageMode == "zstd",
	}

	if storageMode == "zstd" {
		proxy.requestURL = func(hash string, kind cache.EntryKind) string {
			if kind == cache.CAS {
				return fmt.Sprintf("%s/cas.v2/%s", proxy.baseURL, hash)
			}

			return fmt.Sprintf("%s/%s/%s", proxy.baseURL, kind, hash)
		}
	} else if storageMode == "uncompressed" {
		proxy.requestURL = func(hash string, kind cache.EntryKind) string {
			return fmt.Sprintf("%s/%s/%s", proxy.baseURL, kind, hash)
		}
	} else {
		return nil, fmt.Errorf("Invalid http_proxy.mode specified: %q",
			storageMode)
	}

	proxy.uploadQueue = backendproxy.StartUploaders(proxy, numUploaders, maxQueuedUploads)

	return proxy, nil
}

// Helper function for logging responses
func logResponse(logger cache.Logger, method string, code int, url string) {
	logger.Printf("HTTP %s %d %s", method, code, url)
}

func (r *remoteHTTPProxyCache) Put(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, logicalSize int64, sizeOnDisk int64, rc io.ReadCloser) {
	if r.uploadQueue == nil {
		rc.Close()
		return
	}

	item := backendproxy.UploadReq{
		Hasher:      hasher,
		Hash:        hash,
		LogicalSize: logicalSize,
		SizeOnDisk:  sizeOnDisk,
		Kind:        kind,
		Rc:          rc,
	}

	select {
	case r.uploadQueue <- item:
	default:
		r.errorLogger.Printf("too many uploads queued")
		rc.Close()
	}
}

func (r *remoteHTTPProxyCache) Get(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, _ int64) (io.ReadCloser, int64, error) {
	url := r.requestURL(hash, kind)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cacheMisses.Inc()
		return nil, -1, err
	}

	req.Header.Set("X-Digest-Function", hasher.DigestFunction().String())

	rsp, err := r.remote.Do(req)
	if err != nil {
		cacheMisses.Inc()
		return nil, -1, err
	}

	logResponse(r.accessLogger, "DOWNLOAD", rsp.StatusCode, url)

	if rsp.StatusCode == http.StatusNotFound {
		cacheMisses.Inc()
		return nil, -1, nil
	}

	if rsp.StatusCode != http.StatusOK {
		// If the failed http response contains some data then
		// forward up to 1 KiB.
		var errorBytes []byte
		errorBytes, err = io.ReadAll(io.LimitReader(rsp.Body, 1024))
		var errorText string
		if err == nil {
			errorText = string(errorBytes)
		}

		cacheMisses.Inc()
		return nil, -1, &cache.Error{
			Code: rsp.StatusCode,
			Text: errorText,
		}
	}

	if kind == cache.CAS && r.v2mode {
		cacheHits.Inc()
		return casblob.ExtractLogicalSize(rsp.Body)
	}

	sizeBytesStr := rsp.Header.Get("Content-Length")
	if sizeBytesStr == "" {
		err = errors.New("Missing Content-Length header")
		cacheMisses.Inc()
		return nil, -1, err
	}

	sizeBytesInt, err := strconv.Atoi(sizeBytesStr)
	if err != nil {
		cacheMisses.Inc()
		return nil, -1, err
	}
	sizeBytes := int64(sizeBytesInt)

	cacheHits.Inc()

	return rsp.Body, sizeBytes, nil
}

func (r *remoteHTTPProxyCache) Contains(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, _ int64) (bool, int64) {

	url := r.requestURL(hash, kind)

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false, -1
	}

	req.Header.Set("X-Digest-Function", hasher.DigestFunction().String())

	rsp, err := r.remote.Do(req)
	if err == nil && rsp.StatusCode == http.StatusOK {
		if kind != cache.CAS || !r.v2mode {
			return true, rsp.ContentLength
		}

		// We don't know the content size without reading the file header
		// and that could be very costly for the backend server. So return
		// "unknown size".
		return true, -1
	}

	return false, -1
}
