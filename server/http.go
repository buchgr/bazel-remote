package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk"
	"github.com/buchgr/bazel-remote/v2/utils/validate"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"

	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	syncpool "github.com/mostynb/zstdpool-syncpool"
)

var blobNameSHA256 = regexp.MustCompile("^/?(.*/)?(ac/|cas/)([a-f0-9]{64})$")

var decoder, _ = zstd.NewReader(nil) // TODO: raise WithDecoderConcurrency ?

// HTTPCache ...
type HTTPCache interface {
	CacheHandler(w http.ResponseWriter, r *http.Request)
	StatusPageHandler(w http.ResponseWriter, r *http.Request)
	VerifyClientCertHandler(wrapMe http.Handler) http.Handler
}

type httpCache struct {
	cache                    disk.Cache
	accessLogger             cache.Logger
	errorLogger              cache.Logger
	validateAC               bool
	mangleACKeys             bool
	gitCommit                string
	gitTags                  string
	checkClientCertForReads  bool
	checkClientCertForWrites bool
	maxCasBlobSizeBytes      int64
}

type statusPageData struct {
	CurrSize         int64
	UncompressedSize int64
	ReservedSize     int64
	MaxSize          int64
	NumFiles         int
	ServerTime       int64
	GitCommit        string
	GitTags          string
	NumGoroutines    int
}

// NewHTTPCache returns a new instance of the cache.
// accessLogger will print one line for each HTTP request to stdout.
// errorLogger will print unexpected server errors. Inexistent files and malformed URLs will not
// be reported.
func NewHTTPCache(cache disk.Cache, accessLogger cache.Logger, errorLogger cache.Logger, validateAC bool, mangleACKeys bool, checkClientCertForReads bool, checkClientCertForWrites bool, commit string, gitTags string, maxCasBlobSizeBytes int64) HTTPCache {

	_, _, numItems, _ := cache.Stats()

	errorLogger.Printf("Loaded %d existing disk cache items.", numItems)

	hc := &httpCache{
		cache:                    cache,
		accessLogger:             accessLogger,
		errorLogger:              errorLogger,
		validateAC:               validateAC,
		mangleACKeys:             mangleACKeys,
		checkClientCertForReads:  checkClientCertForReads,
		checkClientCertForWrites: checkClientCertForWrites,
		maxCasBlobSizeBytes:      maxCasBlobSizeBytes,
	}

	if commit != "{STABLE_GIT_COMMIT}" {
		hc.gitCommit = commit
	}

	if gitTags != "{GIT_TAGS}" {
		hc.gitTags = gitTags
	}

	return hc
}

// Parse cache artifact information from the request URL
func parseRequestURL(url string, validateAC bool) (kind cache.EntryKind, hash string, instance string, err error) {
	m := blobNameSHA256.FindStringSubmatch(url)
	if m == nil {
		err := fmt.Errorf("resource name must be a SHA256 hash in hex, "+
			"got '%s'", html.EscapeString(url))
		return 0, "", "", err
	}

	instance = strings.TrimSuffix(m[1], "/")

	parts := m[2:]
	if len(parts) != 2 {
		err := fmt.Errorf("the path '%s' is invalid, expected (ac/|cas/)sha256",
			html.EscapeString(url))
		return 0, "", "", err
	}

	// The regex ensures that parts[0] can only be "ac/" or "cas/"
	hash = parts[1]
	if parts[0] == "cas/" {
		return cache.CAS, hash, instance, nil
	}

	if validateAC {
		return cache.AC, hash, instance, nil
	}

	return cache.RAW, hash, instance, nil
}
func (h *httpCache) handleContainsValidAC(w http.ResponseWriter, r *http.Request, hash string) {
	_, data, err := h.cache.GetValidatedActionResult(r.Context(), hash)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		h.logResponse(http.StatusNotFound, r)
		return
	}

	if data == nil {
		http.Error(w, "Not found", http.StatusNotFound)
		h.logResponse(http.StatusNotFound, r)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(data)), 10))
	w.WriteHeader(http.StatusOK)
	h.logResponse(http.StatusOK, r)
}

func (h *httpCache) handleGetValidAC(w http.ResponseWriter, r *http.Request, hash string) {
	_, data, err := h.cache.GetValidatedActionResult(r.Context(), hash)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		h.logResponse(http.StatusNotFound, r)
		return
	}

	if data == nil {
		http.Error(w, "Not found", http.StatusNotFound)
		h.logResponse(http.StatusNotFound, r)
		return
	}

	if r.Header.Get("Accept") == "application/json" {
		ar := &pb.ActionResult{}
		err = proto.Unmarshal(data, ar)
		if err != nil {
			h.logResponse(http.StatusInternalServerError, r)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		md, err := protojson.Marshal(ar)
		if err != nil {
			h.logResponse(http.StatusInternalServerError, r)
			return
		}
		_, err = w.Write(md)
		if err != nil {
			h.logResponse(http.StatusInternalServerError, r)
			return
		}

		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(data)), 10))
	bytesWritten, err := w.Write(data)

	if err != nil {
		h.logResponse(http.StatusInternalServerError, r)
		return
	}

	if bytesWritten != len(data) {
		h.logResponse(http.StatusInternalServerError, r)
		return
	}

	h.logResponse(http.StatusOK, r)
}

// Helper function for logging responses
func (h *httpCache) logResponse(code int, r *http.Request) {
	remoteAddr := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// XFF format: client, proxy1, proxy2
		parts := strings.Split(xff, ",")
		remoteAddr = strings.TrimSpace(parts[0])
	}
	// Parse the client ip:port
	var clientAddress string
	var err error
	clientAddress, _, err = net.SplitHostPort(remoteAddr)
	if err != nil {
		clientAddress = remoteAddr
	}
	h.accessLogger.Printf("%4s %d %15s %s", r.Method, code, clientAddress, r.URL.Path)
}

func (h *httpCache) CacheHandler(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()

	kind, hash, instance, err := parseRequestURL(r.URL.Path, h.validateAC)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		h.logResponse(http.StatusBadRequest, r)
		return
	}

	if h.mangleACKeys && (kind == cache.AC || kind == cache.RAW) {
		hash = cache.TransformActionCacheKey(hash, instance, h.accessLogger)
	}

	switch m := r.Method; m {
	case http.MethodGet:
		if h.checkClientCertForReads && !h.hasValidClientCert(w, r) {
			http.Error(w, "Authentication required for access", http.StatusUnauthorized)
			h.logResponse(http.StatusUnauthorized, r)
			return
		}

		if h.validateAC && kind == cache.AC {
			h.handleGetValidAC(w, r, hash)
			return
		}

		var rdr io.ReadCloser
		var sizeBytes int64

		zstdCompressed := false
		if kind == cache.CAS && strings.Contains(r.Header.Get("Accept-Encoding"), "zstd") {
			rdr, sizeBytes, err = h.cache.GetZstd(r.Context(), hash, -1, 0)
			zstdCompressed = true
		} else {
			rdr, sizeBytes, err = h.cache.Get(r.Context(), kind, hash, -1, 0)
		}
		if err != nil {
			if e, ok := err.(*cache.Error); ok {
				http.Error(w, e.Error(), e.Code)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			h.errorLogger.Printf("GET %s: %s", path(kind, hash), err)
			return
		}

		if rdr == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			h.logResponse(http.StatusNotFound, r)
			return
		}
		defer func() { _ = rdr.Close() }()

		w.Header().Set("Content-Type", "application/octet-stream")
		if zstdCompressed {
			// TODO: calculate Content-Length for compressed blobs too
			// (unless compressing on the fly).
			w.Header().Set("Content-Encoding", "zstd")
		} else {
			w.Header().Set("Content-Length", strconv.FormatInt(sizeBytes, 10))
		}

		_, err := io.Copy(w, rdr)
		if err != nil {
			// No point calling http.Error here because we've already started writing data
			h.errorLogger.Printf("Error writing %s/%s err: %s", kind.String(), hash, err.Error())
			h.logResponse(http.StatusInternalServerError, r)
			return
		}

		h.logResponse(http.StatusOK, r)

	case http.MethodPut:
		if h.checkClientCertForWrites && !h.hasValidClientCert(w, r) {
			http.Error(w, "Authentication required for write access", http.StatusUnauthorized)
			h.logResponse(http.StatusUnauthorized, r)
			return
		}

		contentLength := r.ContentLength

		// If custom header X-Digest-SizeBytes is set, use that for the
		// size of the blob instead of Content-Length (which only works
		// for uncompressed PUTs).
		sb := r.Header.Get("X-Digest-SizeBytes")
		if sb != "" {
			cl, err := strconv.Atoi(sb)
			if err != nil {
				msg := fmt.Sprintf("PUT with unparseable X-Digest-SizeBytes header: %v", sb)
				http.Error(w, msg, http.StatusBadRequest)
				h.errorLogger.Printf("PUT %s: %s", path(kind, hash), msg)
				return
			}

			contentLength = int64(cl)
		}

		if contentLength == -1 {
			// We need the content-length header to make sure we have enough disk space.
			msg := fmt.Sprintf("PUT without Content-Length (key = %s)", path(kind, hash))
			http.Error(w, msg, http.StatusBadRequest)
			h.errorLogger.Printf("PUT %s: %s", path(kind, hash), msg)
			return
		}

		if contentLength == 0 && kind == cache.CAS && hash != emptySha256 {
			msg := fmt.Sprintf("Invalid empty blob hash: \"%s\"", hash)
			http.Error(w, msg, http.StatusBadRequest)
			h.errorLogger.Printf("PUT %s: %s", path(kind, hash), msg)
			return
		}

		if contentLength > h.maxCasBlobSizeBytes {
			msg := fmt.Sprintf("Blob size %d exceeds maximum allowed size %d",
				contentLength, h.maxCasBlobSizeBytes)
			http.Error(w, msg, http.StatusBadRequest)
			h.errorLogger.Printf("PUT %s: %s", path(kind, hash), msg)
			return
		}

		zstdCompressed := false

		// Content-Encoding must be one of "identity", "zstd" or not present.
		ce := r.Header.Get("Content-Encoding")
		if ce == "zstd" {
			zstdCompressed = true
		} else if ce != "" && ce != "identity" {
			msg := fmt.Sprintf("Unsupported content-encoding: %q", ce)
			http.Error(w, msg, http.StatusBadRequest)
			h.errorLogger.Printf("PUT %s: %s", path(kind, hash), msg)
			return
		}

		var rdr io.Reader = r.Body
		if h.validateAC && kind == cache.AC {
			// verify that this is a valid ActionResult

			data, err := io.ReadAll(rdr)
			if err != nil {
				msg := "failed to read request body"
				http.Error(w, msg, http.StatusInternalServerError)
				h.errorLogger.Printf("PUT %s: %s", path(kind, hash), msg)
				return
			}

			if zstdCompressed {
				uncompressed, err := decoder.DecodeAll(data, nil)
				if err != nil {
					msg := fmt.Sprintf("failed to uncompress zstd-encoded request body: %v", err)
					http.Error(w, msg, http.StatusBadRequest)
					h.errorLogger.Printf("PUT %s: %s", path(kind, hash), msg)
					return
				}

				data = uncompressed
				zstdCompressed = false
			}

			if int64(len(data)) != contentLength {
				msg := fmt.Sprintf("sizes don't match. Expected %d, found %d",
					contentLength, len(data))
				http.Error(w, msg, http.StatusBadRequest)
				h.errorLogger.Printf("PUT %s: %s", path(kind, hash), msg)
				return
			}

			// Ensure that the serialized ActionResult has non-zero length.
			ar, code, err := addWorkerMetadataHTTP(r.RemoteAddr, r.Header.Get("Content-Type"), data)
			if err != nil {
				msg := "Failed to add worker metadata: " + err.Error()
				http.Error(w, msg, code)
				h.errorLogger.Printf("PUT %s: %s", path(kind, hash), msg)
				return
			}

			// Note: we do not currently verify that the blobs exist in the CAS.
			err = validate.ActionResult(ar)
			if err != nil {
				msg := "Failed to marshal ActionResult: " + err.Error()
				http.Error(w, msg, http.StatusBadRequest)
				h.errorLogger.Printf("PUT %s: %s", path(kind, hash), msg)
				return
			}

			data, err = proto.Marshal(ar)
			if err != nil {
				msg := "Failed to marshal ActionResult"
				http.Error(w, msg, http.StatusInternalServerError)
				h.errorLogger.Printf("PUT %s: %s", path(kind, hash), msg)
				return
			}

			contentLength = int64(len(data))

			rdr = bytes.NewReader(data)
		}

		if zstdCompressed {
			z, ok := decoderPool.Get().(*syncpool.DecoderWrapper)
			if !ok {
				err = errDecoderPoolFail
			} else {
				err = z.Reset(rdr)
			}
			if err != nil {
				if z != nil {
					defer z.Close()
				}
				msg := fmt.Sprintf("Failed to create zstd reader: %v", err)
				http.Error(w, msg, http.StatusInternalServerError)
				h.errorLogger.Printf("PUT %s: %s", path(kind, hash), msg)
				return
			}

			rc := z.IOReadCloser()
			defer func() { _ = rc.Close() }()
			rdr = rc
		}

		err := h.cache.Put(r.Context(), kind, hash, contentLength, rdr)
		if err != nil {
			var msg string
			if cerr, ok := err.(*cache.Error); ok {
				msg = cerr.Text
				http.Error(w, msg, cerr.Code)
				if cerr.Code == http.StatusInsufficientStorage {
					// Using accessLogger to prevent too verbose logging
					// to errorLogger.
					h.logResponse(cerr.Code, r)
				} else {
					h.errorLogger.Printf("PUT %s: %s", path(kind, hash), msg)
				}
			} else {
				msg = "Unexpected error adding item to cache: " + err.Error()
				http.Error(w, msg, http.StatusInternalServerError)
				h.errorLogger.Printf("PUT %s: %s", path(kind, hash), msg)
			}
		} else {
			h.logResponse(http.StatusOK, r)
		}

	case http.MethodHead:
		if h.checkClientCertForReads && !h.hasValidClientCert(w, r) {
			http.Error(w, "Authentication required for access", http.StatusUnauthorized)
			h.logResponse(http.StatusUnauthorized, r)
			return
		}

		if h.validateAC && kind == cache.AC {
			h.handleContainsValidAC(w, r, hash)
			return
		}

		// Unvalidated path:

		ok, size := h.cache.Contains(r.Context(), kind, hash, -1)
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			h.logResponse(http.StatusNotFound, r)
			return
		}

		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusOK)
		h.logResponse(http.StatusOK, r)

	default:
		msg := fmt.Sprintf("Method '%s' not supported.", html.EscapeString(m))
		http.Error(w, msg, http.StatusMethodNotAllowed)
		h.logResponse(http.StatusMethodNotAllowed, r)
	}
}

func addWorkerMetadataHTTP(addr string, ct string, orig []byte) (actionResult *pb.ActionResult, code int, err error) {
	ar := &pb.ActionResult{}
	if ct == "application/json" {
		err = protojson.Unmarshal(orig, ar)
	} else {
		err = proto.Unmarshal(orig, ar)
	}
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	if ar.ExecutionMetadata == nil {
		ar.ExecutionMetadata = &pb.ExecutedActionMetadata{}
	} else if ar.ExecutionMetadata.Worker != "" {
		return ar, http.StatusOK, nil
	}

	worker := addr
	if worker == "" {
		worker, _, err = net.SplitHostPort(addr)
		if err != nil || worker == "" {
			worker = "unknown"
		}
	}

	ar.ExecutionMetadata.Worker = worker

	return ar, http.StatusOK, nil
}

// Produce a debugging page with some stats about the cache.
func (h *httpCache) StatusPageHandler(w http.ResponseWriter, r *http.Request) {
	defer func() { _ = r.Body.Close() }()

	totalSize, reservedSize, numItems, uncompressedSize := h.cache.Stats()

	goroutines := runtime.NumGoroutine()

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	err := enc.Encode(statusPageData{
		MaxSize:          h.cache.MaxSize(),
		CurrSize:         totalSize,
		UncompressedSize: uncompressedSize,
		ReservedSize:     reservedSize,
		NumFiles:         numItems,
		ServerTime:       time.Now().Unix(),
		GitCommit:        h.gitCommit,
		GitTags:          h.gitTags,
		NumGoroutines:    goroutines,
	})
	if err != nil {
		h.errorLogger.Printf("Failed to encode status json: %s", err.Error())
	}
}

func path(kind cache.EntryKind, hash string) string {
	return fmt.Sprintf("/%s/%s", kind, hash)
}

// If the http.Request is authenticated with a valid client certificate
// then do nothing and return true. Otherwise, write an error to the
// http.ResponseWriter, log the error and return false.
//
// This is only used when mutual TLS authentication and unauthenticated
// reads are enabled.
func (h *httpCache) hasValidClientCert(w http.ResponseWriter, r *http.Request) bool {
	if r == nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		h.logResponse(http.StatusBadRequest, r)
		return false
	}

	if r.TLS == nil {
		http.Error(w, "missing TLS connection info", http.StatusUnauthorized)
		h.logResponse(http.StatusUnauthorized, r)
		return false
	}

	if len(r.TLS.VerifiedChains) == 0 || len(r.TLS.VerifiedChains[0]) == 0 {
		http.Error(w, "no valid client certificate", http.StatusUnauthorized)
		h.logResponse(http.StatusUnauthorized, r)
		return false
	}

	return true
}

// VerifyClientCertHandler returns a http.Handler which wraps another Handler,
// but only calls the inner Handler if the request has a valid client cert.
// This is only used when mutual TLS authentication is enabled.
func (h *httpCache) VerifyClientCertHandler(wrapMe http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.hasValidClientCert(w, r) {
			return
		}

		wrapMe.ServeHTTP(w, r)
	})
}
