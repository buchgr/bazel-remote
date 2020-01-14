package disk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/djherbis/atime"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
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
// It implements the SizedItem interface.
type lruItem struct {
	size      int64
	committed bool
}

func (i *lruItem) Size() int64 {
	return i.size
}

// diskCache is an implementation of the cache backed by files on a filesystem.
type diskCache struct {
	dir string
	mu  *sync.Mutex
	lru SizedLRU
}

// New returns a new instance of a filesystem-based cache rooted at `dir`,
// with a maximum size of `maxSizeBytes` bytes.
func New(dir string, maxSizeBytes int64) cache.Cache {
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
	onEvict := func(key Key, value SizedItem) {
		// Only remove committed items (as temporary files have a different filename)
		if value.(*lruItem).committed {
			err := os.Remove(filepath.Join(dir, key.(string)))
			if err != nil {
				log.Println(err)
			}
		}
	}

	cache := &diskCache{
		dir: filepath.Clean(dir),
		mu:  &sync.Mutex{},
		lru: NewSizedLRU(maxSizeBytes, onEvict),
	}

	err := cache.migrateDirectories()
	if err != nil {
		log.Fatalf("Attempting to migrate the old directory structure to the new structure failed "+
			"with error: %v", err)
	}
	err = cache.loadExistingFiles()
	if err != nil {
		log.Fatalf("Loading of existing cache entries failed due to error: %v", err)
	}

	return cache
}

func (c *diskCache) migrateDirectories() error {
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
	return filepath.Walk(dir, func(name string, info os.FileInfo, err error) error {
		if info.IsDir() {
			if name == dir {
				return nil
			}
			return filepath.SkipDir
		}
		hash := filepath.Base(name)
		newName := filepath.Join(filepath.Dir(name), hash[:2], hash)
		return os.Rename(name, newName)
	})
}

// loadExistingFiles lists all files in the cache directory, and adds them to the
// LRU index so that they can be served. Files are sorted by access time first,
// so that the eviction behavior is preserved across server restarts.
func (c *diskCache) loadExistingFiles() error {
	log.Printf("Loading existing files in %s.\n", c.dir)

	// Walk the directory tree
	type NameAndInfo struct {
		info os.FileInfo
		name string
	}
	var files []NameAndInfo
	err := filepath.Walk(c.dir, func(name string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			files = append(files, NameAndInfo{info, name})
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
		c.lru.Add(relPath, &lruItem{
			size:      f.info.Size(),
			committed: true,
		})
	}

	log.Println("Finished loading disk cache files.")
	return nil
}

func (c *diskCache) Put(kind cache.EntryKind, hash string, expectedSize int64, r io.Reader) error {
	c.mu.Lock()

	key := cacheKey(kind, hash)

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
			Text: "The item that has been tried to insert was too big.",
		}
	}

	// By the time this function exits, we should either mark the LRU item as committed
	// (if the upload went well), or delete it. Capturing the flag variable is not very nice,
	// but this stuff is really easy to get wrong without defer().
	shouldCommit := false
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()

		if shouldCommit {
			newItem.committed = true
		} else {
			c.lru.Remove(key)
		}
	}()

	// Download to a temporary file
	filePath := cacheFilePath(kind, c.dir, hash)
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

	var bytesCopied int64 = 0
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

	if err := f.Sync(); err != nil {
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	if bytesCopied != expectedSize {
		return fmt.Errorf(
			"sizes don't match. Expected %d, found %d", expectedSize, bytesCopied)
	}

	// Rename to the final path
	err = os.Rename(tmpFilePath, filePath)
	// Only commit if renaming succeeded
	if err == nil {
		// This flag is used by the defer() block above.
		shouldCommit = true
	}

	return err
}

func (c *diskCache) Get(kind cache.EntryKind, hash string) (rdr io.ReadCloser, sizeBytes int64, err error) {
	if !c.Contains(kind, hash) {
		cacheMisses.Inc()
		return nil, 0, nil
	}

	blobPath := cacheFilePath(kind, c.dir, hash)

	fileInfo, err := os.Stat(blobPath)
	if err != nil {
		cacheMisses.Inc()
		return nil, 0, err
	}
	sizeBytes = fileInfo.Size()

	rdr, err = os.Open(blobPath)
	if err != nil {
		cacheMisses.Inc()
		return nil, 0, err
	}

	cacheHits.Inc()
	return rdr, sizeBytes, nil
}

func (c *diskCache) Contains(kind cache.EntryKind, hash string) (ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	val, found := c.lru.Get(cacheKey(kind, hash))
	// Uncommitted (i.e. uploading items) should be reported as not ok
	return found && val.(*lruItem).committed
}

func (c *diskCache) MaxSize() int64 {
	// The underlying value is never modified, no need to lock.
	return c.lru.MaxSize()
}

func (c *diskCache) Stats() (currentSize int64, numItems int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.lru.CurrentSize(), c.lru.Len()
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
