package disk

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/disk/casblob"
	"github.com/buchgr/bazel-remote/utils/tempfile"

	"github.com/djherbis/atime"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	pb "github.com/buchgr/bazel-remote/genproto/build/bazel/remote/execution/v2"
	"github.com/golang/protobuf/proto"
)

var (
	cacheHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bazel_remote_disk_cache_hits",
		Help: "The total number of disk backend cache hits",
	})
	cacheMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bazel_remote_disk_cache_misses",
		Help: "The total number of disk backend cache misses",
	})
)

var tfc = tempfile.NewCreator()

var emptyZstdBlob = []byte{40, 181, 47, 253, 32, 0, 1, 0, 0}

var hashKeyRegex = regexp.MustCompile("^[a-f0-9]{64}$")

// lruItem is the type of the values stored in SizedLRU to keep track of items.
type lruItem struct {
	// Size of the blob in uncompressed form.
	size int64

	// Size of the blob on disk (possibly with header + compression).
	sizeOnDisk int64

	// If true, the blob is a raw CAS file (no header, uncompressed)
	// with a ".v1" filename suffix.
	legacy bool
}

// Cache is a filesystem-based LRU cache, with an optional backend proxy.
type Cache struct {
	dir         string
	proxy       cache.Proxy
	storageMode casblob.CompressionType

	mu  sync.Mutex
	lru SizedLRU
}

type nameAndInfo struct {
	name string // relative path
	info os.FileInfo
}

const sha256HashStrSize = sha256.Size * 2 // Two hex characters per byte.
const emptySha256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// New returns a new instance of a filesystem-based cache rooted at `dir`,
// with a maximum size of `maxSizeBytes` bytes and an optional backend `proxy`.
// Cache is safe for concurrent use.
func New(dir string, maxSizeBytes int64, storageMode string, proxy cache.Proxy) (*Cache, error) {
	// Create the directory structure.
	hexLetters := []byte("0123456789abcdef")
	for _, c1 := range hexLetters {
		for _, c2 := range hexLetters {
			subDir := string(c1) + string(c2)
			err := os.MkdirAll(filepath.Join(dir, cache.CAS.DirName(), subDir), os.ModePerm)
			if err != nil {
				return nil, err
			}
			err = os.MkdirAll(filepath.Join(dir, cache.AC.DirName(), subDir), os.ModePerm)
			if err != nil {
				return nil, err
			}
			err = os.MkdirAll(filepath.Join(dir, cache.RAW.DirName(), subDir), os.ModePerm)
			if err != nil {
				return nil, err
			}
		}
	}

	// The eviction callback deletes the file from disk.
	// This function is only called while the lock is held
	// by the current goroutine.
	onEvict := func(key Key, value lruItem) {
		ks := key.(string)
		hash := ks[len(ks)-sha256.Size*2:]
		var kind cache.EntryKind = cache.AC
		if strings.HasPrefix(ks, "cas") {
			kind = cache.CAS
		} else if strings.HasPrefix(ks, "ac") {
			kind = cache.AC
		} else if strings.HasPrefix(ks, "raw") {
			kind = cache.RAW
		}

		f := filepath.Join(dir, cache.FileLocation(kind, hash))

		err := os.Remove(f)
		if err != nil {
			log.Printf("ERROR: failed to remove evicted cache file: %s", f)
		}
	}

	compressionType := casblob.Zstandard
	if storageMode == "uncompressed" {
		compressionType = casblob.Identity
	}

	c := &Cache{
		dir:         filepath.Clean(dir),
		storageMode: compressionType,
		proxy:       proxy,
		lru:         NewSizedLRU(maxSizeBytes, onEvict),
	}

	err := c.migrateDirectories()
	if err != nil {
		return nil, fmt.Errorf("Attempting to migrate the old directory structure failed: %w", err)
	}
	err = c.loadExistingFiles()
	if err != nil {
		return nil, fmt.Errorf("Loading of existing cache entries failed due to error: %w", err)
	}

	return c, nil
}

func (c *Cache) migrateDirectories() error {
	err := migrateDirectory(c.dir, cache.AC)
	if err != nil {
		return err
	}
	err = migrateDirectory(c.dir, cache.CAS)
	if err != nil {
		return err
	}
	// Note: there are no old "RAW" directories (yet).
	return nil
}

func migrateDirectory(baseDir string, kind cache.EntryKind) error {
	sourceDir := path.Join(baseDir, kind.String())

	_, err := os.Stat(sourceDir)
	if os.IsNotExist(err) {
		return nil
	}

	log.Println("Migrating files (if any) to new directory structure:", sourceDir)

	listing, err := ioutil.ReadDir(sourceDir)
	if err != nil {
		return err
	}

	// The v0 directory structure was lowercase sha256 hash filenames
	// stored directly in the ac/ and cas/ directories.

	// The v1 directory structure has subdirs for each two lowercase
	// hex character pairs.
	v1DirRegex := regexp.MustCompile("^[a-f0-9]{2}$")

	targetDir := path.Join(baseDir, kind.DirName())

	itemChan := make(chan os.FileInfo)
	errChan := make(chan error)

	var wg sync.WaitGroup
	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for item := range itemChan {

				oldName := item.Name()
				oldNamePath := filepath.Join(sourceDir, oldName)

				if item.IsDir() {
					if !v1DirRegex.MatchString(oldName) {
						// Warn about non-v1 subdirectories.
						log.Println("Warning: unexpected directory", oldNamePath)
					}

					destDir := filepath.Join(targetDir, oldName[:2])
					err := migrateV1Subdir(oldNamePath, destDir, kind)
					if err != nil {
						log.Printf("Warning: failed to read subdir %q: %s",
							oldNamePath, err)
						continue
					}

					continue
				}

				if !item.Mode().IsRegular() {
					log.Println("Warning: skipping non-regular file:", oldNamePath)
					continue
				}

				if !hashKeyRegex.MatchString(oldName) {
					log.Println("Warning: skipping unexpected file:", oldNamePath)
					continue
				}

				src := filepath.Join(sourceDir, oldName)
				dest := filepath.Join(targetDir, oldName[:2], oldName)
				if kind == cache.CAS {
					dest += ".v1"
				}

				// TODO: make this work across filesystems?
				err := os.Rename(src, dest)
				if err != nil {
					errChan <- err
					return
				}
			}
		}()
	}

	err = nil
	numItems := len(listing)
	i := 1
	for _, item := range listing {
		select {
		case itemChan <- item:
			log.Printf("Migrating %s item(s) %d/%d, %s\n", sourceDir, i, numItems, item.Name())
			i++
		case err = <-errChan:
			log.Println("Encountered error while migrating files:", err)
			close(itemChan)
		}
	}
	close(itemChan)
	wg.Wait()

	return err
}

func migrateV1Subdir(oldDir string, destDir string, kind cache.EntryKind) error {

	listing, err := ioutil.ReadDir(oldDir)
	if err != nil {
		return err
	}

	if kind == cache.CAS {
		for _, item := range listing {

			oldPath := path.Join(oldDir, item.Name())

			if !hashKeyRegex.MatchString(item.Name()) {
				return fmt.Errorf("Unexpected file: %s", oldPath)
			}

			destPath := path.Join(destDir, item.Name()) + ".v1"
			err = os.Rename(oldPath, destPath)
			if err != nil {
				return fmt.Errorf("Failed to migrate CAS blob %s: %w",
					oldPath, err)
			}
		}

		return os.Remove(oldDir)
	}

	for _, item := range listing {
		oldPath := path.Join(oldDir, item.Name())

		if !hashKeyRegex.MatchString(item.Name()) {
			return fmt.Errorf("Unexpected file: %s", oldPath)
		}

		destPath := path.Join(destDir, item.Name())

		// TODO: support cross-filesystem migration.
		err = os.Rename(oldPath, destPath)
		if err != nil {
			return fmt.Errorf("Failed to migrate blob %s: %w", oldPath, err)
		}
	}

	return nil
}

/* TODO: remove this dead code?
func migrateCASFile(src string, dest string, hash string) error {

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	fi, err := srcFile.Stat()
	if err != nil {
		return err
	}

	origSize := fi.Size()
	accessTime := atime.Get(fi)

	destFile, err := os.Create(dest)
	if err != nil {
		return err
	}
	_, err = casblob.WriteAndClose(srcFile, destFile,
		casblob.Zstandard, hash, origSize)
	if err != nil {
		return err
	}
	os.Chtimes(dest, accessTime, fi.ModTime()) // Best effort, ignore errors.

	err = os.Remove(src)
	if err != nil {
		return fmt.Errorf("Failed to remove %s: %w", src, err)
	}

	return err
}
*/

// loadExistingFiles lists all files in the cache directory, and adds them to the
// LRU index so that they can be served. Files are sorted by access time first,
// so that the eviction behavior is preserved across server restarts.
func (c *Cache) loadExistingFiles() error {
	log.Printf("Loading existing files in %s.\n", c.dir)

	// Walk the directory tree
	var files []nameAndInfo
	err := filepath.Walk(c.dir, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			log.Println("Error while walking directory:", err)
			return err
		}

		if !info.IsDir() {
			files = append(files, nameAndInfo{name: name, info: info})
		}
		return nil
	})
	if err != nil {
		return err
	}

	log.Println("Sorting cache files by atime.")
	// Sort in increasing order of atime
	sort.Slice(files, func(i int, j int) bool {
		return atime.Get(files[i].info).Before(atime.Get(files[j].info))
	})

	log.Println("Building LRU index.")
	for _, f := range files {
		relPath := f.name[len(c.dir)+1:]

		legacy := strings.HasSuffix(f.name, ".v1")

		fields := strings.Split(relPath, "/")

		hash := fields[len(fields)-1]
		if legacy {
			hash = strings.TrimSuffix(hash, ".v1")
		}

		if !hashKeyRegex.MatchString(hash) {
			return fmt.Errorf("Invalid hash: %q", hash)
		}

		sizeOnDisk := f.info.Size()
		size := sizeOnDisk

		var lookupKey string

		if strings.HasPrefix(relPath, "cas.v2/") {
			if !legacy {
				size, err = casblob.GetLogicalSize(f.name)
				if err != nil {
					return err
				}
			}
			lookupKey = "cas/" + hash
		} else if strings.HasPrefix(relPath, "ac/") {
			lookupKey = "ac/" + hash
		} else if strings.HasPrefix(relPath, "raw/") {
			lookupKey = "raw/" + hash
		} else {
			return fmt.Errorf("Unrecognised file in cache dir: %q", relPath)
		}

		ok := c.lru.Add(lookupKey, lruItem{
			size:       size,
			sizeOnDisk: sizeOnDisk,
			legacy:     legacy,
		})
		if !ok {
			err = os.Remove(filepath.Join(c.dir, relPath))
			if err != nil {
				return err
			}
		}
	}

	log.Println("Finished loading disk cache files.")

	return nil
}

// Put stores a stream of `size` bytes from `r` into the cache.
// If `hash` is not the empty string, and the contents don't match it,
// a non-nil error is returned.
func (c *Cache) Put(kind cache.EntryKind, hash string, size int64, r io.Reader) (rErr error) {
	if size < 0 {
		return fmt.Errorf("Invalid (negative) size: %d", size)
	}

	// The hash format is checked properly in the http/grpc code.
	// Just perform a simple/fast check here, to catch bad tests.
	if len(hash) != sha256HashStrSize {
		return fmt.Errorf("Invalid hash size: %d, expected: %d",
			len(hash), sha256.Size)
	}

	if kind == cache.CAS && size == 0 && hash == emptySha256 {
		io.Copy(ioutil.Discard, r)
		return nil
	}

	key := cache.LookupKey(kind, hash)

	var tf *os.File // Tempfile.

	// Cleanup intermediate state if something went wrong and we
	// did not successfully commit.
	unreserve := false
	removeTempfile := false
	defer func() {
		// No lock required to remove stray tempfiles.
		if removeTempfile {
			os.Remove(tf.Name())
		}

		if unreserve {
			c.mu.Lock()
			err := c.lru.Unreserve(size)
			if err != nil {
				// Set named return value.
				rErr = err
				log.Printf(rErr.Error())
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
				Code: http.StatusInternalServerError,
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

	// Final destination, if all goes well.
	filePath := cacheFilePath(kind, c.dir, hash)

	// We will download to this temporary file.
	tf, err := tfc.Create(filePath)
	if err != nil {
		return err
	}
	removeTempfile = true

	var sizeOnDisk int64
	sizeOnDisk, err = c.writeAndCloseFile(r, kind, hash, size, tf)
	if err != nil {
		return err
	}

	if c.proxy != nil {
		rc, err := os.Open(tf.Name())
		if err != nil {
			log.Println("Failed to proxy Put:", err)
		} else {
			// Doesn't block, should be fast.
			c.proxy.Put(kind, hash, sizeOnDisk, rc)
		}
	}

	unreserve, removeTempfile, err = c.commit(key, tf.Name(), filePath, size, size, sizeOnDisk)

	// Might be nil.
	return err
}

func (c *Cache) writeAndCloseFile(r io.Reader, kind cache.EntryKind, hash string, size int64, f *os.File) (int64, error) {
	closeFile := true
	defer func() {
		if closeFile {
			f.Close()
		}
	}()

	var err error
	var sizeOnDisk int64

	if kind == cache.CAS && c.storageMode != casblob.Identity {
		sizeOnDisk, err = casblob.WriteAndClose(r, f, c.storageMode, hash, size)
		if err != nil {
			return -1, err
		}
		closeFile = false
		return sizeOnDisk, nil
	}

	if sizeOnDisk, err = io.Copy(f, r); err != nil {
		return -1, err
	}

	if isSizeMismatch(sizeOnDisk, size) {
		return -1, fmt.Errorf(
			"sizes don't match. Expected %d, found %d", size, sizeOnDisk)
	}

	if err = f.Sync(); err != nil {
		return -1, err
	}

	if err = f.Close(); err != nil {
		return -1, err
	}
	closeFile = false

	return sizeOnDisk, nil
}

// This must be called when the lock is not held.
func (c *Cache) commit(key string, tempfile string, finalPath string, reservedSize int64, logicalSize int64, sizeOnDisk int64) (unreserve bool, removeTempfile bool, err error) {
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

	if !c.lru.Add(key, lruItem{size: logicalSize, sizeOnDisk: sizeOnDisk}) {
		err = fmt.Errorf("INTERNAL ERROR: failed to add: %s, size %d (on disk: %d): %w",
			key, logicalSize, sizeOnDisk, err)
		log.Println(err.Error())
		return unreserve, removeTempfile, err
	}

	err = syscall.Rename(tempfile, finalPath)
	if err != nil {
		log.Printf("INTERNAL ERROR: failed to rename \"%s\" to \"%s\": %v",
			tempfile, finalPath, err)
		log.Println("Removing", key)
		c.lru.Remove(key)
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
func (c *Cache) availableOrTryProxy(key string, size int64, blobPath string, kind cache.EntryKind, offset int64, zstd bool) (rc io.ReadCloser, foundSize int64, tryProxy bool, err error) {
	locked := true
	c.mu.Lock()

	item, available := c.lru.Get(key)
	if available {
		c.mu.Unlock() // We expect a cache hit below.
		locked = false

		if !isSizeMismatch(size, item.size) {
			if item.legacy {
				blobPath += ".v1"
			}

			var f *os.File
			f, err = os.Open(blobPath)
			if err != nil {
				// Race condition, was the item purged after we released the lock?
				log.Printf("Warning: expected %s to exist on disk, undersized cache?", blobPath)
			} else if kind == cache.CAS {
				if item.legacy {
					_, err = f.Seek(offset, io.SeekStart)
					if !zstd && err == nil {
						rc = f
					} else if err == nil {
						rc, err = casblob.GetLegacyZstdReadCloser(f)
					}
				} else if zstd {
					rc, err = casblob.GetZstdReadCloser(f, size, offset)
				} else {
					rc, err = casblob.GetUncompressedReadCloser(f, size, offset)
				}

				if err != nil {
					log.Printf("Warning: expected item to be on disk, but something happened: %v", err)
					f.Close()
					rc = nil
				} else {
					return rc, item.size, false, nil
				}
			} else {
				var fileInfo os.FileInfo
				fileInfo, err = f.Stat()
				foundSize := fileInfo.Size()
				if isSizeMismatch(size, foundSize) {
					// Race condition, was the item replaced after we released the lock?
					log.Printf("Warning: expected %s to on disk to have size %d, found %d",
						blobPath, size, foundSize)
				} else {
					f.Seek(offset, io.SeekStart)
					return f, foundSize, false, nil
				}
			}
		}
	}
	err = nil

	if c.proxy != nil {
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

var errOnlyCompressedCAS = errors.New("Only CAS blobs are available in compressed form")

// Get returns an io.ReadCloser with the content of the cache item stored
// under `hash` and the number of bytes that can be read from it. If the
// item is not found, the io.ReadCloser will be nil. If some error occurred
// when processing the request, then it is returned. Callers should provide
// the `size` of the item to be retrieved, or -1 if unknown.
func (c *Cache) Get(kind cache.EntryKind, hash string, size int64, offset int64) (rc io.ReadCloser, s int64, rErr error) {
	return c.get(kind, hash, size, offset, false)
}

// GetZstd is just like Get, except the data available from rc is zstandard
// compressed. Note that the returned `s` value still refers to the amount
// of data once it has been decompressed.
func (c *Cache) GetZstd(hash string, size int64, offset int64) (rc io.ReadCloser, s int64, rErr error) {
	return c.get(cache.CAS, hash, size, offset, true)
}

func (c *Cache) get(kind cache.EntryKind, hash string, size int64, offset int64, zstd bool) (rc io.ReadCloser, s int64, rErr error) {
	// The hash format is checked properly in the http/grpc code.
	// Just perform a simple/fast check here, to catch bad tests.
	if len(hash) != sha256HashStrSize {
		return nil, -1, fmt.Errorf("Invalid hash size: %d, expected: %d",
			len(hash), sha256.Size)
	}

	if kind == cache.CAS && size <= 0 && hash == emptySha256 {
		cacheHits.Inc()

		if zstd {
			return ioutil.NopCloser(bytes.NewReader(emptyZstdBlob)), 0, nil
		}

		return ioutil.NopCloser(bytes.NewReader([]byte{})), 0, nil
	}

	if kind != cache.CAS && zstd {
		return nil, -1, errOnlyCompressedCAS
	}

	if offset < 0 {
		return nil, -1, fmt.Errorf("Invalid offset: %d", offset)
	}
	if size > 0 && offset >= size {
		return nil, -1, fmt.Errorf("Invalid offset: %d for size %d", offset, size)
	}

	var err error
	key := cache.LookupKey(kind, hash)

	var tf *os.File // Tempfile we will write to.

	// Cleanup intermediate state if something went wrong and we
	// did not successfully commit.
	unreserve := false
	removeTempfile := false
	defer func() {
		// No lock required to remove stray tempfiles.
		if removeTempfile {
			os.Remove(tf.Name())
		}

		if unreserve {
			c.mu.Lock()
			err := c.lru.Unreserve(size)
			if err != nil {
				// Set named return value.
				rErr = err
				log.Printf(rErr.Error())
			}
			c.mu.Unlock()
		}
	}()

	blobPath := cacheFilePath(kind, c.dir, hash)
	f, foundSize, tryProxy, err := c.availableOrTryProxy(key, size, blobPath, kind, offset, zstd)
	if err != nil {
		return nil, -1, err
	}
	if tryProxy && size > 0 {
		unreserve = true
	}
	if f != nil {
		cacheHits.Inc()
		return f, foundSize, nil
	}

	cacheMisses.Inc()

	if !tryProxy {
		return nil, -1, nil
	}

	r, foundSize, err := c.proxy.Get(kind, hash)
	if r != nil {
		defer r.Close()
	}
	if err != nil || r == nil {
		return nil, -1, err
	}

	if kind != cache.CAS && isSizeMismatch(size, foundSize) {
		return nil, -1, nil
	}

	tf, err = tfc.Create(blobPath)
	if err != nil {
		return nil, -1, err
	}
	removeTempfile = true

	tfName := tf.Name()

	var sizeOnDisk int64
	sizeOnDisk, err = io.Copy(tf, r)
	tf.Close()
	if err != nil {
		return nil, -1, err
	}

	logicalSize := foundSize

	if kind == cache.CAS {
		logicalSize, err = casblob.GetLogicalSize(tfName)
		if err != nil {
			return nil, -1, err
		}
		if isSizeMismatch(size, logicalSize) {
			return nil, -1, nil
		}
	}

	rcf, err := os.Open(tfName)
	if err != nil {
		return nil, -1, err
	}
	if kind != cache.CAS {
		if offset > 0 {
			rcf.Seek(offset, io.SeekStart)
		}
		rc = rcf
	} else if zstd {
		rc, err = casblob.GetZstdReadCloser(rcf, size, offset)
		if err != nil {
			return nil, -1, err
		}
	} else {
		rc, err = casblob.GetUncompressedReadCloser(rcf, size, offset)
		if err != nil {
			return nil, -1, err
		}
	}

	unreserve, removeTempfile, err = c.commit(key, tfName, blobPath, size, logicalSize, sizeOnDisk)
	if err != nil {
		rc.Close()
		rc = nil
		foundSize = -1
	}

	return rc, logicalSize, err
}

// Contains returns true if the `hash` key exists in the cache, and
// the size if known (or -1 if unknown).
//
// If there is a local cache miss, the proxy backend (if there is
// one) will be checked.
//
// Callers should provide the `size` of the item, or -1 if unknown.
func (c *Cache) Contains(kind cache.EntryKind, hash string, size int64) (bool, int64) {

	// The hash format is checked properly in the http/grpc code.
	// Just perform a simple/fast check here, to catch bad tests.
	if len(hash) != sha256HashStrSize {
		return false, -1
	}

	if kind == cache.CAS && size <= 0 && hash == emptySha256 {
		return true, 0
	}

	foundSize := int64(-1)
	key := cache.LookupKey(kind, hash)

	c.mu.Lock()
	item, exists := c.lru.Get(key)
	if exists {
		foundSize = item.size
	}
	c.mu.Unlock()

	if exists && !isSizeMismatch(size, foundSize) {
		return true, foundSize
	}

	if c.proxy != nil {
		exists, foundSize = c.proxy.Contains(kind, hash)
		if exists && !isSizeMismatch(size, foundSize) {
			return true, foundSize
		}
	}

	return false, -1
}

// MaxSize returns the maximum cache size in bytes.
func (c *Cache) MaxSize() int64 {
	// The underlying value is never modified, no need to lock.
	return c.lru.MaxSize()
}

// Stats returns the current size of the cache in bytes, and the number of
// items stored in the cache.
func (c *Cache) Stats() (totalSize int64, reservedSize int64, numItems int, uncompressedSize int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.lru.TotalSize(), c.lru.ReservedSize(), c.lru.Len(), c.lru.UncompressedSize()
}

func isSizeMismatch(requestedSize int64, foundSize int64) bool {
	return requestedSize > -1 && foundSize > -1 && requestedSize != foundSize
}

func ensureDirExists(path string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		err = os.MkdirAll(path, os.ModePerm)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func cacheFilePath(kind cache.EntryKind, cacheDir string, hash string) string {
	return filepath.Join(cacheDir, cache.FileLocation(kind, hash))
}

// GetValidatedActionResult returns a valid ActionResult and its serialized
// value from the CAS if it and all its dependencies are also available. If
// not, nil values are returned. If something unexpected went wrong, return
// an error.
func (c *Cache) GetValidatedActionResult(hash string) (*pb.ActionResult, []byte, error) {

	rc, sizeBytes, err := c.Get(cache.AC, hash, -1, 0)
	if rc != nil {
		defer rc.Close()
	}
	if err != nil {
		return nil, nil, err
	}

	if rc == nil || sizeBytes <= 0 {
		return nil, nil, nil // aka "not found"
	}

	acdata, err := ioutil.ReadAll(rc)
	if err != nil {
		return nil, nil, err
	}

	result := &pb.ActionResult{}
	err = proto.Unmarshal(acdata, result)
	if err != nil {
		return nil, nil, err
	}

	for _, f := range result.OutputFiles {
		if len(f.Contents) == 0 {
			found, _ := c.Contains(cache.CAS, f.Digest.Hash, f.Digest.SizeBytes)
			if !found {
				return nil, nil, nil // aka "not found"
			}
		}
	}

	for _, d := range result.OutputDirectories {
		r, size, err := c.Get(cache.CAS, d.TreeDigest.Hash, d.TreeDigest.SizeBytes, 0)
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
		oddata, err = ioutil.ReadAll(r)
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
			if f.Digest == nil {
				continue
			}
			found, _ := c.Contains(cache.CAS, f.Digest.Hash, f.Digest.SizeBytes)
			if !found {
				return nil, nil, nil // aka "not found"
			}
		}

		for _, child := range tree.GetChildren() {
			for _, f := range child.GetFiles() {
				if f.Digest == nil {
					continue
				}
				found, _ := c.Contains(cache.CAS, f.Digest.Hash, f.Digest.SizeBytes)
				if !found {
					return nil, nil, nil // aka "not found"
				}
			}
		}
	}

	if result.StdoutDigest != nil {
		found, _ := c.Contains(cache.CAS, result.StdoutDigest.Hash, result.StdoutDigest.SizeBytes)
		if !found {
			return nil, nil, nil // aka "not found"
		}
	}

	if result.StderrDigest != nil {
		found, _ := c.Contains(cache.CAS, result.StderrDigest.Hash, result.StderrDigest.SizeBytes)
		if !found {
			return nil, nil, nil // aka "not found"
		}
	}

	return result, acdata, nil
}
