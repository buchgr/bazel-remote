package disk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk/casblob"
	"github.com/buchgr/bazel-remote/v2/cache/disk/zstdimpl"
	"github.com/buchgr/bazel-remote/v2/cache/hashing"
	"github.com/buchgr/bazel-remote/v2/utils/annotate"
	"github.com/buchgr/bazel-remote/v2/utils/tempfile"
	"github.com/buchgr/bazel-remote/v2/utils/validate"

	"github.com/djherbis/atime"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
	"google.golang.org/protobuf/proto"

	"github.com/prometheus/client_golang/prometheus"

	"golang.org/x/sync/semaphore"
)

var tfc = tempfile.NewCreator()

var emptyZstdBlob = []byte{40, 181, 47, 253, 32, 0, 1, 0, 0}

type Cache interface {
	Get(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, size int64, offset int64) (io.ReadCloser, int64, error)
	GetValidatedActionResult(ctx context.Context, hasher hashing.Hasher, hash string) (*pb.ActionResult, []byte, error)
	GetZstd(ctx context.Context, hasher hashing.Hasher, hash string, size int64, offset int64) (io.ReadCloser, int64, error)
	Put(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, size int64, r io.Reader) error
	Contains(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, size int64) (bool, int64)
	FindMissingCasBlobs(ctx context.Context, hasher hashing.Hasher, blobs []*pb.Digest) ([]*pb.Digest, error)

	MaxSize() int64
	Stats() (totalSize int64, reservedSize int64, numItems int, uncompressedSize int64)
	RegisterMetrics()
}

// lruItem is the type of the values stored in SizedLRU to keep track of items.
type lruItem struct {
	// Size of the blob in uncompressed form.
	size int64

	// Size of the blob on disk (possibly with header + compression).
	sizeOnDisk int64

	// A random string (of digits, for now) that is included in the filename.
	random string

	// If true, the blob is a raw CAS file (no header, uncompressed)
	// with a ".v1" filename suffix.
	legacy bool
}

// diskCache is a filesystem-based LRU cache, with an optional backend proxy.
// It is safe for concurrent use.
type diskCache struct {
	dir              string
	proxy            cache.Proxy
	storageMode      casblob.CompressionType
	zstd             zstdimpl.ZstdImpl
	maxBlobSize      int64
	maxProxyBlobSize int64
	accessLogger     *log.Logger
	containsQueue    chan proxyCheck

	// Limit the number of simultaneous file removals.
	fileRemovalSem *semaphore.Weighted

	mu  sync.Mutex
	lru SizedLRU

	gaugeCacheAge prometheus.Gauge
}

func internalErr(err error) *cache.Error {
	return &cache.Error{
		Code: http.StatusInternalServerError,
		Text: err.Error(),
	}
}

func badReqErr(format string, a ...interface{}) *cache.Error {
	return &cache.Error{
		Code: http.StatusBadRequest,
		Text: fmt.Sprintf(format, a...),
	}
}

// Non-test users must call this to expose metrics.
func (c *diskCache) RegisterMetrics() {
	c.lru.RegisterMetrics()

	prometheus.MustRegister(c.gaugeCacheAge)

	// Update the cache age metric on a static interval
	// Note: this could be modeled as a GuageFunc that updates as needed
	// but since the updater func must lock the cache mu, it was deemed
	// necessary to have greater control of when to get the cache age
	go c.pollCacheAge()
}

// Update metric every minute with the idle time of the least recently used item in the cache
func (c *diskCache) pollCacheAge() {
	ticker := time.NewTicker(60 * time.Second)
	for ; true; <-ticker.C {
		c.updateCacheAgeMetric()
	}
}

// Get the idle time of the least-recently used item in the cache, and store the value in a metric
func (c *diskCache) updateCacheAgeMetric() {
	c.mu.Lock()

	key, value := c.lru.getTailItem()
	age := 0.0
	validAge := true

	if key != nil {
		f := c.getElementPath(key, value)
		ts, err := atime.Stat(f)

		if err != nil {
			log.Printf("ERROR: failed to determine time of least recently used cache item: %v, unable to stat %s", err, f)
			validAge = false
		} else {
			age = time.Since(ts).Seconds()
		}
	}

	c.mu.Unlock()

	if validAge {
		c.gaugeCacheAge.Set(age)
	}
}

func (c *diskCache) getElementPath(key Key, value lruItem) string {
	ks := key.(string)

	parts := strings.Split(ks, "/")

	digestFn := hashing.DigestFunction(parts[1])
	if len(parts) == 2 {
		digestFn = hashing.DefaultFn
	}
	hasher, err := hashing.Get(digestFn)
	if err != nil {
		return ""
	}

	hash := parts[len(parts)-1]

	var kind cache.EntryKind = cache.AC
	if strings.HasPrefix(ks, "cas") {
		kind = cache.CAS
	} else if strings.HasPrefix(ks, "ac") {
		kind = cache.AC
	} else if strings.HasPrefix(ks, "raw") {
		kind = cache.RAW
	}

	return filepath.Join(c.dir, c.FileLocation(kind, value.legacy, hasher, hash, value.size, value.random))
}

func (c *diskCache) removeFile(f string) {
	if err := c.fileRemovalSem.Acquire(context.Background(), 1); err != nil {
		log.Printf("ERROR: failed to aquire semaphore: %v, unable to remove %s", err, f)
		return
	}
	defer c.fileRemovalSem.Release(1)

	err := os.Remove(f)
	if err != nil {
		log.Printf("ERROR: failed to remove evicted cache file: %s", f)
	}
}

func (c *diskCache) FileLocationBase(kind cache.EntryKind, legacy bool, hasher hashing.Hasher, hash string, size int64) string {
	if kind == cache.RAW {
		return path.Join("raw.v2", hasher.Dir(), hash[:2], hash)
	}

	if kind == cache.AC {
		return path.Join("ac.v2", hasher.Dir(), hash[:2], hash)
	}

	if legacy {
		return path.Join("cas.v2", hasher.Dir(), hash[:2], hash)
	}

	return path.Join("cas.v2", hasher.Dir(), hash[:2], fmt.Sprintf("%s-%d", hash, size))
}

func (c *diskCache) FileLocation(kind cache.EntryKind, legacy bool, hasher hashing.Hasher, hash string, size int64, random string) string {
	if kind == cache.RAW {
		return path.Join("raw.v2", hasher.Dir(), hash[:2], hash+"-"+random)
	}

	if kind == cache.AC {
		return path.Join("ac.v2", hasher.Dir(), hash[:2], hash+"-"+random)
	}

	if legacy {
		return path.Join("cas.v2", hasher.Dir(), hash[:2], fmt.Sprintf("%s-%s.v1", hash, random))
	}

	return path.Join("cas.v2", hasher.Dir(), hash[:2], fmt.Sprintf("%s-%d-%s", hash, size, random))
}

// Put stores a stream of `size` bytes from `r` into the cache.
// If `hash` is not the empty string, and the contents don't match it,
// a non-nil error is returned. All data will be read from `r` before
// this function returns.
func (c *diskCache) Put(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, size int64, r io.Reader) (rErr error) {
	defer func() {
		if r != nil {
			_, _ = io.Copy(io.Discard, r)
		}
	}()

	if size < 0 {
		return badReqErr("Invalid (negative) size: %d", size)
	}

	if size > c.maxBlobSize {
		return badReqErr("Blob size %d too large, max blob size is %d", size, c.maxBlobSize)
	}

	// The hash format is checked properly in the http/grpc code.
	// Just perform a simple/fast check here, to catch bad tests.
	if len(hash) != hasher.Size()*2 {
		return badReqErr("Invalid hash size: %d, expected: %d", len(hash), hasher.Size()*2)
	}

	if kind == cache.CAS && size == 0 && hash == hasher.Empty() {
		return nil
	}

	key := cache.LookupKey(kind, hasher, hash)

	var tf *os.File // Tempfile.
	var blobFile string

	// Cleanup intermediate state if something went wrong and we
	// did not successfully commit.
	unreserve := false
	removeTempfile := false
	defer func() {
		// No lock required to remove stray tempfiles.
		if removeTempfile {
			os.Remove(blobFile)
		} else if blobFile != "" {
			// Mark the file as "complete".
			err := os.Chmod(blobFile, tempfile.FinalMode)
			if err != nil {
				log.Println("Failed to mark", blobFile, "as complete:", err)
			}
		}

		if unreserve {
			c.mu.Lock()
			err := c.lru.Unreserve(size)
			if err != nil {
				// Set named return value.
				rErr = internalErr(err)
				log.Println(rErr.Error())
			}
			c.mu.Unlock()
		}
	}()

	if size > 0 {
		c.mu.Lock()
		ok, err := c.lru.Reserve(size)
		if err != nil {
			c.mu.Unlock()
			return &cache.Error{
				Code: http.StatusInsufficientStorage,
				Text: err.Error(),
			}
		}
		if !ok {
			c.mu.Unlock()
			return &cache.Error{
				Code: http.StatusInsufficientStorage,
				Text: fmt.Sprintf("The item (%d) + reserved space is larger than the cache's maximum size (%d).",
					size, c.lru.MaxSize()),
			}
		}
		c.mu.Unlock()
		unreserve = true
	}

	legacy := kind == cache.CAS && c.storageMode == casblob.Identity

	// Final destination, if all goes well.
	filePath := path.Join(c.dir, c.FileLocationBase(kind, legacy, hasher, hash, size))

	// We will download to this temporary file.
	tf, random, err := tfc.Create(filePath, legacy)
	if err != nil {
		return internalErr(err)
	}
	if tf == nil {
		return &cache.Error{
			Code: http.StatusInternalServerError,
			Text: fmt.Sprintf("Failed to create tempfile for %q", filePath),
		}
	}
	blobFile = tf.Name()
	removeTempfile = true

	var sizeOnDisk int64
	sizeOnDisk, err = c.writeAndCloseFile(ctx, r, kind, hasher, hash, size, tf)
	if err != nil {
		return internalErr(err)
	}

	r = nil // We read all the data from r.

	if c.proxy != nil {
		rc, err := os.Open(blobFile)
		if err != nil {
			log.Println("Failed to proxy Put:", err)
		} else {
			// Doesn't block, should be fast.
			c.proxy.Put(ctx, kind, hasher, hash, size, sizeOnDisk, rc)
		}
	}

	unreserve, removeTempfile, err = c.commit(key, legacy, blobFile, size, size, sizeOnDisk, random)
	if err != nil {
		return internalErr(err)
	}

	return nil
}

func (c *diskCache) writeAndCloseFile(ctx context.Context, r io.Reader, kind cache.EntryKind, hasher hashing.Hasher, hash string, size int64, f *os.File) (int64, error) {
	closeFile := true
	defer func() {
		if closeFile {
			f.Close()
		}
	}()

	var err error
	var sizeOnDisk int64

	if kind == cache.CAS && c.storageMode != casblob.Identity {
		sizeOnDisk, err = casblob.WriteAndClose(c.zstd, r, f, c.storageMode, hasher, hash, size)
		if err != nil {
			return -1, annotate.Err(ctx, "Failed to write compressed CAS blob to disk", err)
		}
		closeFile = false
		return sizeOnDisk, nil
	}

	if sizeOnDisk, err = io.Copy(f, r); err != nil {
		return -1, annotate.Err(ctx, "Failed to copy data to disk", err)
	}

	if isSizeMismatch(sizeOnDisk, size) {
		return -1, fmt.Errorf(
			"Sizes don't match. Expected %d, found %d", size, sizeOnDisk)
	}

	if err = f.Sync(); err != nil {
		return -1, fmt.Errorf("Failed to sync file to disk: %w", err)
	}

	if err = f.Close(); err != nil {
		return -1, fmt.Errorf("Failed to close file: %w", err)
	}
	closeFile = false

	return sizeOnDisk, nil
}

// This must be called when the lock is not held.
func (c *diskCache) commit(key string, legacy bool, tempfile string, reservedSize int64, logicalSize int64, sizeOnDisk int64, random string) (unreserve bool, removeTempfile bool, err error) {
	unreserve = reservedSize > 0
	removeTempfile = true

	c.mu.Lock()
	defer c.mu.Unlock()

	if unreserve {
		err = c.lru.Unreserve(reservedSize)
		if err != nil {
			log.Println(err.Error())
			return true, removeTempfile, err
		}
	}
	unreserve = false

	newItem := lruItem{
		size:       logicalSize,
		sizeOnDisk: sizeOnDisk,
		legacy:     legacy,
		random:     random,
	}

	if !c.lru.Add(key, newItem) {
		err = fmt.Errorf("INTERNAL ERROR: failed to add: %s, size %d (on disk: %d)",
			key, logicalSize, sizeOnDisk)
		log.Println(err.Error())
		return unreserve, removeTempfile, err
	}

	removeTempfile = false

	// Commit successful if we made it this far! \o/
	return unreserve, removeTempfile, nil
}

// Return a non-nil io.ReadCloser and non-negative size if the item is available
// locally, and a boolean that indicates if the item is not available locally
// but that we can try the proxy backend.
//
// This function assumes that only CAS blobs are requested in zstd form.
func (c *diskCache) availableOrTryProxy(kind cache.EntryKind, hasher hashing.Hasher, hash string, size int64, offset int64, zstd bool) (io.ReadCloser, int64, bool, error) {
	locked := true
	var err error
	c.mu.Lock()

	key := cache.LookupKey(kind, hasher, hash)
	item, available := c.lru.Get(key)
	if available {
		c.mu.Unlock() // We expect a cache hit below.
		locked = false

		blobPath := path.Join(c.dir, c.FileLocation(kind, item.legacy, hasher, hash, item.size, item.random))

		if !isSizeMismatch(size, item.size) {
			var f *os.File
			f, err = os.Open(blobPath)
			if err != nil && os.IsNotExist(err) {
				// Another request replaced the file before we could open it?
				// Enter slow path.

				c.mu.Lock()
				item, available = c.lru.Get(key)
				if available {
					blobPath = path.Join(c.dir, c.FileLocation(kind, item.legacy, hasher, hash, item.size, item.random))
					f, err = os.Open(blobPath)
				}
				c.mu.Unlock()
			}

			if err != nil {
				// Race condition, was the item purged after we released the lock?
				log.Printf("Warning: expected %q to exist on disk, undersized cache?", blobPath)
			} else if kind == cache.CAS {
				var rc io.ReadCloser
				if item.legacy {
					// The file is uncompressed, without a casblob header.
					_, err = f.Seek(offset, io.SeekStart)
					if zstd && err == nil {
						rc, err = casblob.GetLegacyZstdReadCloser(c.zstd, f)
					} else if err == nil {
						rc = f
					}
				} else {
					// The file is compressed.
					if zstd {
						rc, err = casblob.GetZstdReadCloser(c.zstd, f, size, offset)
					} else {
						rc, err = casblob.GetUncompressedReadCloser(c.zstd, f, size, offset)
					}
				}

				if err != nil {
					log.Printf("Warning: expected item to be on disk, but something happened: %v", err)
					f.Close()
				} else {
					return rc, item.size, false, nil
				}
			} else {
				var fileInfo os.FileInfo
				fileInfo, err = f.Stat()
				if err != nil {
					f.Close()
					return nil, -1, true, err
				}
				foundSize := fileInfo.Size()
				if isSizeMismatch(size, foundSize) {
					// Race condition, was the item replaced after we released the lock?
					log.Printf("Warning: expected %s to on disk to have size %d, found %d",
						blobPath, size, foundSize)
				} else {
					_, err = f.Seek(offset, io.SeekStart)
					return f, foundSize, false, err
				}
			}
		}
	}
	err = nil

	var tryProxy bool

	if c.proxy != nil && size <= c.maxProxyBlobSize {
		if size > 0 {
			// If we know the size, attempt to reserve that much space.
			if !locked {
				c.mu.Lock()
			}
			tryProxy, err = c.lru.Reserve(size)
			c.mu.Unlock()
			locked = false
		} else {
			// If the size is unknown, take a risk and hope it's not
			// too large.
			tryProxy = true
		}
	}

	if locked {
		c.mu.Unlock()
	}

	return nil, -1, tryProxy, err
}

var errOnlyCompressedCAS = &cache.Error{
	Code: http.StatusBadRequest,
	Text: "Only CAS blobs are available in compressed form",
}

// Get returns an io.ReadCloser with the content of the cache item stored
// under `hash` and the number of bytes that can be read from it. If the
// item is not found, the io.ReadCloser will be nil. If some error occurred
// when processing the request, then it is returned. Callers should provide
// the `size` of the item to be retrieved, or -1 if unknown.
func (c *diskCache) Get(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, size int64, offset int64) (rc io.ReadCloser, s int64, rErr error) {
	return c.get(ctx, kind, hasher, hash, size, offset, false)
}

// GetZstd is just like Get, except the data available from rc is zstandard
// compressed. Note that the returned `s` value still refers to the amount
// of data once it has been decompressed.
func (c *diskCache) GetZstd(ctx context.Context, hasher hashing.Hasher, hash string, size int64, offset int64) (rc io.ReadCloser, s int64, rErr error) {
	return c.get(ctx, cache.CAS, hasher, hash, size, offset, true)
}

func (c *diskCache) get(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, size int64, offset int64, zstd bool) (rc io.ReadCloser, s int64, rErr error) {
	// The hash format is checked properly in the http/grpc code.
	// Just perform a simple/fast check here, to catch bad tests.
	if len(hash) != hasher.Size()*2 {
		return nil, -1, badReqErr("Invalid hash size: %d, expected: %d", len(hash), hasher.Size()*2)
	}

	if kind == cache.CAS && size <= 0 && hash == hasher.Empty() {
		if zstd {
			return io.NopCloser(bytes.NewReader(emptyZstdBlob)), 0, nil
		}

		return io.NopCloser(bytes.NewReader([]byte{})), 0, nil
	}

	if kind != cache.CAS && zstd {
		return nil, -1, errOnlyCompressedCAS
	}

	if offset < 0 {
		return nil, -1, badReqErr("Invalid offset: %d", offset)
	}
	if size > 0 && offset >= size {
		return nil, -1, badReqErr("Invalid offset: %d for size %d", offset, size)
	}

	var err error
	key := cache.LookupKey(kind, hasher, hash)

	var tf *os.File // Tempfile we will write to.
	var blobFile string

	// Cleanup intermediate state if something went wrong and we
	// did not successfully commit.
	unreserve := false
	removeTempfile := false
	defer func() {
		// No lock required to remove stray tempfiles.
		if removeTempfile {
			os.Remove(blobFile)
		} else if blobFile != "" {
			// Mark the file as "complete".
			err := os.Chmod(blobFile, tempfile.FinalMode)
			if err != nil {
				log.Println("Failed to mark", blobFile, "as complete:", err)
			}
		}

		if unreserve {
			c.mu.Lock()
			err := c.lru.Unreserve(size)
			if err != nil {
				// Set named return value.
				rErr = internalErr(err)
				log.Println(rErr.Error())
			}
			c.mu.Unlock()
		}
	}()

	f, foundSize, tryProxy, err := c.availableOrTryProxy(kind, hasher, hash, size, offset, zstd)
	if err != nil {
		return nil, -1, internalErr(err)
	}
	if tryProxy && size > 0 {
		unreserve = true
	}
	if f != nil {
		return f, foundSize, nil
	}

	if !tryProxy {
		return nil, -1, nil
	}

	r, foundSize, err := c.proxy.Get(ctx, kind, hasher, hash, size)
	if r != nil {
		defer r.Close()
	}
	if err != nil {
		return nil, -1, internalErr(err)
	}
	if r == nil {
		return nil, -1, nil
	}
	if foundSize > c.maxProxyBlobSize {
		r.Close()
		return nil, -1, nil
	}

	if isSizeMismatch(size, foundSize) || foundSize < 0 {
		return nil, -1, nil
	}

	legacy := kind == cache.CAS && c.storageMode == casblob.Identity

	blobPathBase := path.Join(c.dir, c.FileLocationBase(kind, legacy, hasher, hash, foundSize))
	tf, random, err := tfc.Create(blobPathBase, legacy)
	if err != nil {
		return nil, -1, internalErr(err)
	}
	removeTempfile = true

	blobFile = tf.Name()

	var sizeOnDisk int64
	sizeOnDisk, err = io.Copy(tf, r)
	tf.Close()
	if err != nil {
		return nil, -1, internalErr(err)
	}

	rcf, err := os.Open(blobFile)
	if err != nil {
		return nil, -1, internalErr(err)
	}

	uncompressedOnDisk := (kind != cache.CAS) || (c.storageMode == casblob.Identity)
	if uncompressedOnDisk {
		if offset > 0 {
			_, err = rcf.Seek(offset, io.SeekStart)
			if err != nil {
				return nil, -1, internalErr(err)
			}
		}

		if zstd {
			rc, err = casblob.GetLegacyZstdReadCloser(c.zstd, rcf)
		} else {
			rc = rcf
		}
	} else { // Compressed CAS blob.
		if zstd {
			rc, err = casblob.GetZstdReadCloser(c.zstd, rcf, foundSize, offset)
		} else {
			rc, err = casblob.GetUncompressedReadCloser(c.zstd, rcf, foundSize, offset)
		}
	}
	if err != nil {
		return nil, -1, internalErr(err)
	}

	unreserve, removeTempfile, err = c.commit(key, legacy, blobFile, size, foundSize, sizeOnDisk, random)
	if err != nil {
		rc.Close()
		return nil, -1, internalErr(err)
	}

	return rc, foundSize, nil
}

// Contains returns true if the `hash` key exists in the cache, and
// the size if known (or -1 if unknown).
//
// If there is a local cache miss, the proxy backend (if there is
// one) will be checked.
//
// Callers should provide the `size` of the item, or -1 if unknown.
func (c *diskCache) Contains(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, size int64) (bool, int64) {
	// The hash format is checked properly in the http/grpc code.
	// Just perform a simple/fast check here, to catch bad tests.
	if len(hash) != hasher.Size()*2 {
		return false, -1
	}

	if kind == cache.CAS && size <= 0 && hash == hasher.Empty() {
		return true, 0
	}

	foundSize := int64(-1)
	key := cache.LookupKey(kind, hasher, hash)

	c.mu.Lock()
	item, exists := c.lru.Get(key)
	if exists {
		foundSize = item.size
	}
	c.mu.Unlock()

	if exists && !isSizeMismatch(size, foundSize) {
		return true, foundSize
	}

	if c.proxy != nil && size <= c.maxProxyBlobSize {
		exists, foundSize = c.proxy.Contains(ctx, kind, hasher, hash, size)
		if exists && foundSize <= c.maxProxyBlobSize && !isSizeMismatch(size, foundSize) {
			return true, foundSize
		}
	}

	return false, -1
}

// MaxSize returns the maximum cache size in bytes.
func (c *diskCache) MaxSize() int64 {
	// The underlying value is never modified, no need to lock.
	return c.lru.MaxSize()
}

// Stats returns the current size of the cache in bytes, and the number of
// items stored in the cache.
func (c *diskCache) Stats() (totalSize int64, reservedSize int64, numItems int, uncompressedSize int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.lru.TotalSize(), c.lru.ReservedSize(), c.lru.Len(), c.lru.UncompressedSize()
}

func isSizeMismatch(requestedSize int64, foundSize int64) bool {
	return requestedSize > -1 && foundSize > -1 && requestedSize != foundSize
}

// GetValidatedActionResult returns a valid ActionResult and its serialized
// value from the CAS if it and all its dependencies are also available. If
// not, nil values are returned. If something unexpected went wrong, return
// an error.
func (c *diskCache) GetValidatedActionResult(ctx context.Context, hasher hashing.Hasher, hash string) (*pb.ActionResult, []byte, error) {
	rc, sizeBytes, err := c.Get(ctx, cache.AC, hasher, hash, -1, 0)
	if rc != nil {
		defer rc.Close()
	}
	if err != nil {
		return nil, nil, err
	}

	if rc == nil || sizeBytes <= 0 {
		return nil, nil, nil // aka "not found"
	}

	acdata, err := io.ReadAll(rc)
	if err != nil {
		return nil, nil, err
	}

	result := &pb.ActionResult{}
	err = proto.Unmarshal(acdata, result)
	if err != nil {
		return nil, nil, err
	}

	// Validate the ActionResult's immediate fields, but don't check for dependent blobs.
	err = validate.ActionResult(result, hasher)
	if err != nil {
		return nil, nil, err // Should we return "not found" instead of an error?
	}

	pendingValidations := []*pb.Digest{}

	for _, f := range result.OutputFiles {
		// f was validated in validate.ActionResult but blobs were not checked for existence
		if len(f.Contents) == 0 {
			pendingValidations = append(pendingValidations, f.Digest)
		}
	}

	for _, d := range result.OutputDirectories {
		// d was validated in validate.ActionResult but blobs were not checked for existence
		r, size, err := c.Get(ctx, cache.CAS, hasher, d.TreeDigest.Hash, d.TreeDigest.SizeBytes, 0)
		if r == nil {
			return nil, nil, err // aka "not found", or an err if non-nil
		}
		if err != nil {
			r.Close()
			return nil, nil, err
		}
		if size != d.TreeDigest.SizeBytes {
			r.Close()
			return nil, nil, fmt.Errorf("expected %d bytes, found %d",
				d.TreeDigest.SizeBytes, size)
		}

		var oddata []byte
		oddata, err = io.ReadAll(r)
		r.Close()
		if err != nil {
			return nil, nil, err
		}

		tree := pb.Tree{}
		err = proto.Unmarshal(oddata, &tree)
		if err != nil {
			return nil, nil, err
		}

		for _, f := range tree.Root.GetFiles() {
			if f.Digest != nil {
				pendingValidations = append(pendingValidations, f.Digest)
			}
		}

		for _, child := range tree.GetChildren() {
			for _, f := range child.GetFiles() {
				if f.Digest != nil {
					pendingValidations = append(pendingValidations, f.Digest)
				}
			}
		}
	}

	if result.StdoutDigest != nil {
		pendingValidations = append(pendingValidations, result.StdoutDigest)
	}

	if result.StderrDigest != nil {
		pendingValidations = append(pendingValidations, result.StderrDigest)
	}

	err = c.findMissingCasBlobsInternal(ctx, hasher, pendingValidations, true)
	if errors.Is(err, errMissingBlob) {
		return nil, nil, nil // aka "not found"
	}
	if err != nil {
		return nil, nil, err
	}

	return result, acdata, nil
}
