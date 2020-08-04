package disk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/djherbis/atime"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	pb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
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

// lruItem is the type of the values stored in SizedLRU to keep track of items.
// It implements the sizedItem interface.
type lruItem struct {
	size      int64
	committed bool
}

func (i *lruItem) Size() int64 {
	return i.size
}

// Cache is a filesystem-based LRU cache, with an optional backend proxy.
type Cache struct {
	dir   string
	proxy cache.Proxy

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
func New(dir string, maxSizeBytes int64, proxy cache.Proxy) *Cache {
	// Create the directory structure.
	hexLetters := []byte("0123456789abcdef")
	for _, c1 := range hexLetters {
		for _, c2 := range hexLetters {
			subDir := string(c1) + string(c2)
			err := os.MkdirAll(filepath.Join(dir, cache.CAS.String(), subDir), os.ModePerm)
			if err != nil {
				log.Fatal(err)
			}
			err = os.MkdirAll(filepath.Join(dir, cache.AC.String(), subDir), os.ModePerm)
			if err != nil {
				log.Fatal(err)
			}
			err = os.MkdirAll(filepath.Join(dir, cache.RAW.String(), subDir), os.ModePerm)
			if err != nil {
				log.Fatal(err)
			}
		}
	}

	// The eviction callback deletes the file from disk.
	// This function is only called while the lock is held
	// by the current goroutine.
	onEvict := func(key Key, value sizedItem) {

		f := filepath.Join(dir, key.(string))

		if value.(*lruItem).committed {
			// Common case. Just remove the cache file and we're done.
			err := os.Remove(f)
			if err != nil {
				log.Printf("ERROR: failed to remove evicted cache file: %s", f)
			}

			return
		}

		// There is an ongoing upload for the evicted item. The temp
		// file may or may not exist at this point.
		//
		// We should either be able to remove both the temp file and
		// the regular cache file, or to remove just the regular cache
		// file. The temp file is renamed/moved to the regular cache
		// file without holding the lock, so we must try removing the
		// temp file first.

		// Note: if you hit this case, then your cache size might be
		// too small (blobs are moved to the most-recently used end
		// of the index when the upload begins, and these items are
		// still uploading when they reach the least-recently used
		// end of the index).

		tf := f + ".tmp"
		var fErr, tfErr error
		removedCount := 0

		tfErr = os.Remove(tf)
		if tfErr == nil {
			removedCount++
		}

		fErr = os.Remove(f)
		if fErr == nil {
			removedCount++
		}

		// We expect to have removed at least one file at this point.
		if removedCount == 0 {
			if !os.IsNotExist(tfErr) {
				log.Printf("ERROR: failed to remove evicted item: %s / %v",
					tf, tfErr)
			}

			if !os.IsNotExist(fErr) {
				log.Printf("ERROR: failed to remove evicted item: %s / %v",
					f, fErr)
			}
		}
	}

	c := &Cache{
		dir:   filepath.Clean(dir),
		proxy: proxy,
		lru:   NewSizedLRU(maxSizeBytes, onEvict),
	}

	err := c.migrateDirectories()
	if err != nil {
		log.Fatalf("Attempting to migrate the old directory structure to the new structure failed "+
			"with error: %v", err)
	}
	err = c.loadExistingFiles()
	if err != nil {
		log.Fatalf("Loading of existing cache entries failed due to error: %v", err)
	}

	return c
}

func (c *Cache) migrateDirectories() error {
	err := migrateDirectory(filepath.Join(c.dir, cache.AC.String()))
	if err != nil {
		return err
	}
	err = migrateDirectory(filepath.Join(c.dir, cache.CAS.String()))
	if err != nil {
		return err
	}
	// Note: there are no old "RAW" directories (yet).
	return nil
}

func migrateDirectory(dir string) error {
	log.Printf("Migrating files (if any) to new directory structure: %s\n", dir)

	listing, err := ioutil.ReadDir(dir)
	if err != nil {
		return err
	}

	// The v0 directory structure was lowercase sha256 hash filenames
	// stored directly in the ac/ and cas/ directories.
	hashKeyRegex := regexp.MustCompile("^[a-f0-9]{64}$")

	// The v1 directory structure has subdirs for each two lowercase
	// hex character pairs.
	v1DirRegex := regexp.MustCompile("^[a-f0-9]{2}$")

	for _, item := range listing {
		oldName := item.Name()
		oldNamePath := filepath.Join(dir, oldName)

		if item.IsDir() {
			if !v1DirRegex.MatchString(oldName) {
				// Warn about non-v1 subdirectories.
				log.Println("Warning: unexpected directory", oldNamePath)
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

		newName := filepath.Join(dir, oldName[:2], oldName)
		err = os.Rename(filepath.Join(dir, oldName), newName)
		if err != nil {
			return err
		}
	}

	return nil
}

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
		ok := c.lru.Add(relPath, &lruItem{
			size:      f.info.Size(),
			committed: true,
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

// Put stores a stream of `expectedSize` bytes from `r` into the cache.
// If `hash` is not the empty string, and the contents don't match it,
// a non-nil error is returned.
func (c *Cache) Put(kind cache.EntryKind, hash string, expectedSize int64, r io.Reader) error {

	// The hash format is checked properly in the http/grpc code.
	// Just perform a simple/fast check here, to catch bad tests.
	if len(hash) != sha256HashStrSize {
		return fmt.Errorf("Invalid hash size: %d, expected: %d",
			len(hash), sha256.Size)
	}

	if kind == cache.CAS && expectedSize == 0 && hash == emptySha256 {
		io.Copy(ioutil.Discard, r)
		return nil
	}

	key := cacheKey(kind, hash)

	c.mu.Lock()

	// If there's an ongoing upload (i.e. cache key is present in uncommitted state),
	// we drop the upload and discard the incoming stream. We do accept uploads
	// of existing keys, as it should happen relatively rarely (e.g. race
	// condition on the bazel side) but it's useful to overwrite poisoned items.
	if existingItem, found := c.lru.Get(key); found {
		if !existingItem.(*lruItem).committed {
			c.mu.Unlock()
			io.Copy(ioutil.Discard, r)
			return nil
		}
	}

	// Try to add the item to the LRU.
	newItem := &lruItem{
		size:      expectedSize,
		committed: false,
	}
	ok := c.lru.Add(key, newItem)
	c.mu.Unlock()
	if !ok {
		return &cache.Error{
			Code: http.StatusInsufficientStorage,
			Text: fmt.Sprintf("The item is larger (%d) than the cache's maximum size (%d).",
				expectedSize, c.lru.MaxSize()),
		}
	}

	// By the time this function exits, we should either mark the LRU item as committed
	// (if the upload went well), or delete it. Capturing the flag variable is not very nice,
	// but this stuff is really easy to get wrong without defer().
	shouldCommit := false
	filePath := cacheFilePath(kind, c.dir, hash)
	defer func() {
		c.mu.Lock()
		if shouldCommit {
			newItem.committed = true
		} else {
			c.lru.Remove(key)
		}
		c.mu.Unlock()

		if shouldCommit && c.proxy != nil {
			// TODO: buffer in memory, avoid a filesystem round-trip?
			fr, err := os.Open(filePath)
			if err == nil {
				c.proxy.Put(kind, hash, expectedSize, fr)
			}
		}
	}()

	// Download to a temporary file
	tmpFilePath := filePath + ".tmp"
	f, err := os.Create(tmpFilePath)
	if err != nil {
		return err
	}
	defer func() {
		if !shouldCommit {
			// Only delete the temp file if moving it didn't succeed.
			os.Remove(tmpFilePath)
		}
		// Just in case we didn't already close it.  No need to check errors.
		f.Close()
	}()

	var bytesCopied int64
	if kind == cache.CAS {
		hasher := sha256.New()
		if bytesCopied, err = io.Copy(io.MultiWriter(f, hasher), r); err != nil {
			return err
		}
		actualHash := hex.EncodeToString(hasher.Sum(nil))
		if actualHash != hash {
			return fmt.Errorf(
				"hashsums don't match. Expected %s, found %s", key, actualHash)
		}
	} else {
		if bytesCopied, err = io.Copy(f, r); err != nil {
			return err
		}
	}

	if err = f.Sync(); err != nil {
		return err
	}

	if err = f.Close(); err != nil {
		return err
	}

	if bytesCopied != expectedSize {
		return fmt.Errorf(
			"sizes don't match. Expected %d, found %d", expectedSize, bytesCopied)
	}

	// Rename to the final path
	err = os.Rename(tmpFilePath, filePath)
	if err == nil {
		// Only commit if renaming succeeded.
		// This flag is used by the defer() block above.
		shouldCommit = true
	}

	return err
}

// Return two bools, `available` is true if the item is in the local
// cache and ready to use.
//
// `tryProxy` is true if the item is not in the local cache but can
// be requested from the proxy, in which case, a placeholder entry
// has been added to the index and the caller must either replace
// the entry with the actual size, or remove it from the LRU.
func (c *Cache) availableOrTryProxy(key string) (available bool, tryProxy bool) {
	inProgress := false
	tryProxy = false

	c.mu.Lock()

	existingItem, found := c.lru.Get(key)
	if found {
		if !existingItem.(*lruItem).committed {
			inProgress = true
		}
	} else if c.proxy != nil {
		// Reserve a place in the LRU.
		// The caller must replace or remove this!
		tryProxy = c.lru.Add(key, &lruItem{
			size:      0,
			committed: false,
		})
	}

	c.mu.Unlock()

	available = found && !inProgress

	return available, tryProxy
}

// Get returns an io.ReadCloser with the content of the cache item stored
// under `hash` and the number of bytes that can be read from it. If the
// item is not found, the io.ReadCloser will be nil. If some error occurred
// when processing the request, then it is returned. Callers should provide
// the `size` of the item to be retrieved, or -1 if unknown.
func (c *Cache) Get(kind cache.EntryKind, hash string, size int64) (io.ReadCloser, int64, error) {

	// The hash format is checked properly in the http/grpc code.
	// Just perform a simple/fast check here, to catch bad tests.
	if len(hash) != sha256HashStrSize {
		return nil, -1, fmt.Errorf("Invalid hash size: %d, expected: %d",
			len(hash), sha256.Size)
	}

	if kind == cache.CAS && size <= 0 && hash == emptySha256 {
		cacheHits.Inc()
		return ioutil.NopCloser(bytes.NewReader([]byte{})), 0, nil
	}

	var err error
	key := cacheKey(kind, hash)

	available, tryProxy := c.availableOrTryProxy(key)

	if available {
		blobPath := cacheFilePath(kind, c.dir, hash)
		var fileInfo os.FileInfo
		fileInfo, err = os.Stat(blobPath)
		if err == nil {
			foundSize := fileInfo.Size()
			if isSizeMismatch(size, foundSize) {
				cacheMisses.Inc()
				return nil, -1, nil
			}
			var f *os.File
			f, err = os.Open(blobPath)
			if err == nil {
				cacheHits.Inc()
				return f, foundSize, nil
			}
		}

		cacheMisses.Inc()
		return nil, -1, err
	}

	cacheMisses.Inc()

	if !tryProxy {
		return nil, -1, nil
	}

	filePath := cacheFilePath(kind, c.dir, hash)
	tmpFilePath := filePath + ".tmp"
	shouldCommit := false
	tmpFileCreated := false
	foundSize := int64(-1)
	var f *os.File

	// We're allowed to try downloading this blob from the proxy.
	// Before returning, we have to either commit the item and set
	// its size, or remove the item from the LRU.
	defer func() {
		c.mu.Lock()

		if shouldCommit {
			// Overwrite the placeholder inserted by availableOrTryProxy.
			// Call Add instead of updating the entry directly, so we
			// update the currentSize value.
			c.lru.Add(key, &lruItem{
				size:      foundSize,
				committed: true,
			})
		} else {
			// Remove the placeholder.
			c.lru.Remove(key)
		}

		c.mu.Unlock()

		if !shouldCommit && tmpFileCreated {
			os.Remove(tmpFilePath) // No need to check the error.
		}

		f.Close() // No need to check the error.
	}()

	r, foundSize, err := c.proxy.Get(kind, hash)
	if r != nil {
		defer r.Close()
	}
	if err != nil || r == nil {
		return nil, -1, err
	}
	if isSizeMismatch(size, foundSize) {
		return nil, -1, nil
	}

	f, err = os.Create(tmpFilePath)
	if err != nil {
		return nil, -1, err
	}
	tmpFileCreated = true

	written, err := io.Copy(f, r)
	if err != nil {
		return nil, -1, err
	}

	if written != foundSize {
		return nil, -1, err
	}

	if err = f.Sync(); err != nil {
		return nil, -1, err
	}

	if err = f.Close(); err != nil {
		return nil, -1, err
	}

	// Rename to the final path
	err = os.Rename(tmpFilePath, filePath)
	if err == nil {
		// Only commit if renaming succeeded.
		// This flag is used by the defer() block above.
		shouldCommit = true

		var f2 *os.File
		f2, err = os.Open(filePath)
		if err == nil {
			return f2, foundSize, nil
		}
	}

	return nil, -1, err
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
		return false, int64(-1)
	}

	if kind == cache.CAS && size <= 0 && hash == emptySha256 {
		return true, 0
	}

	var found bool
	foundSize := int64(-1)
	key := cacheKey(kind, hash)

	c.mu.Lock()
	val, isInLru := c.lru.Get(key)
	// Uncommitted (i.e. uploading items) should be reported as not ok
	if isInLru {
		item := val.(*lruItem)
		found = item.committed
		foundSize = item.size
	}
	c.mu.Unlock()

	if found {
		if isSizeMismatch(size, foundSize) {
			return false, int64(-1)
		}
		return true, foundSize
	}

	if c.proxy != nil {
		found, foundSize = c.proxy.Contains(kind, hash)
		if isSizeMismatch(size, foundSize) {
			return false, int64(-1)
		}
		return found, foundSize
	}

	return false, int64(-1)
}

// MaxSize returns the maximum cache size in bytes.
func (c *Cache) MaxSize() int64 {
	// The underlying value is never modified, no need to lock.
	return c.lru.MaxSize()
}

// Stats returns the current size of the cache in bytes, and the number of
// items stored in the cache.
func (c *Cache) Stats() (currentSize int64, numItems int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.lru.CurrentSize(), c.lru.Len()
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

func cacheKey(kind cache.EntryKind, hash string) string {
	return filepath.Join(kind.String(), hash[:2], hash)
}

func cacheFilePath(kind cache.EntryKind, cacheDir string, hash string) string {
	return filepath.Join(cacheDir, cacheKey(kind, hash))
}

// GetValidatedActionResult returns a valid ActionResult and its serialized
// value from the CAS if it and all its dependencies are also available. If
// not, nil values are returned. If something unexpected went wrong, return
// an error.
func (c *Cache) GetValidatedActionResult(hash string) (*pb.ActionResult, []byte, error) {

	rc, sizeBytes, err := c.Get(cache.AC, hash, -1)
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
		r, size, err := c.Get(cache.CAS, d.TreeDigest.Hash, d.TreeDigest.SizeBytes)
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
