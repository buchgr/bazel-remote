package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/djherbis/atime"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"net/http"
)

// ErrTooBig is returned by Cache::Put when when the item size is bigger than the
// cache size limit.
type ErrTooBig struct{}

func (e *ErrTooBig) Error() string {
	return "item bigger than the cache size limit"
}

// lruItem is the type of the values stored in SizedLRU to keep track of items.
// It implements the SizedItem interface.
type lruItem struct {
	size      int64
	committed bool
}

func (i *lruItem) Size() int64 {
	return i.size
}

// Cache is the interface for a generic blob storage backend. Implementers should handle
// locking internally.
type Cache interface {
	// Put stores a stream of `size` bytes from `r` into the cache. If `expectedSha256` is
	// not the empty string, and the contents don't match it, an error is returned
	Put(key string, size int64, expectedSha256 string, r io.Reader) error
	// Get writes the content of the cache item stored under `key` to `w`. If the item is
	// not found, it returns ok = false.
	Get(key string, w http.ResponseWriter) (ok bool, err error)
	Contains(key string) (ok bool, err error)

	// Stats
	MaxSize() int64
	CurrentSize() int64
	NumFiles() int
}

// NewFsCache returns a new instance of a filesystem-based cache rooted at `dir`,
// with a maximum size of `maxSizeBytes` bytes.
func NewFsCache(dir string, maxSizeBytes int64) *fsCache {
	// Create the directory structure
	ensureDirExists(filepath.Join(dir, "cas"))
	ensureDirExists(filepath.Join(dir, "ac"))

	// The eviction callback deletes the file from disk.
	onEvict := func(key Key, value SizedItem) {
		// Only remove committed items (as temporary files have a different filename)
		if value.(*lruItem).committed {
			blobPath := filepath.Join(dir, key.(string))
			err := os.Remove(blobPath)
			if err != nil {
				log.Println(err)
			}
		}
	}

	cache := &fsCache{
		dir: dir,
		mux: &sync.RWMutex{},
		lru: NewSizedLRU(maxSizeBytes, onEvict),
	}

	cache.loadExistingFiles()

	return cache
}

// fsCache is an implementation of the cache backed by files on a filesystem.
type fsCache struct {
	dir string
	mux *sync.RWMutex
	lru SizedLRU
}

func (c *fsCache) pathForKey(key string) string {
	return filepath.Join(c.dir, key)
}

// loadExistingFiles lists all files in the cache directory, and adds them to the
// LRU index so that they can be served. Files are sorted by access time first,
// so that the eviction behavior is preserved across server restarts.
func (c *fsCache) loadExistingFiles() {
	// Walk the directory tree
	type NameAndInfo struct {
		info os.FileInfo
		name string
	}
	var files []NameAndInfo
	filepath.Walk(c.dir, func(name string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			files = append(files, NameAndInfo{info, name})
		}
		return nil
	})

	// Sort in increasing order of atime
	sort.Slice(files, func(i int, j int) bool {
		return atime.Get(files[i].info).Before(atime.Get(files[j].info))
	})

	for _, f := range files {
		key := f.name[len(c.dir)+1:]
		c.lru.Add(key, &lruItem{
			size:      f.info.Size(),
			committed: true,
		})
	}
}

func (c *fsCache) Put(key string, size int64, expectedSha256 string, r io.Reader) (err error) {
	c.mux.Lock()

	// If `key` is already in the LRU, do nothing. This applied to both
	// committed an uncommitted files (we don't want to upload again if an upload
	// of the same file is already in progress).
	if _, exists := c.lru.Get(key); exists {
		c.mux.Unlock()
		return
	}

	// Try to add the item to the LRU.
	newItem := &lruItem{
		size:      size,
		committed: false,
	}
	ok := c.lru.Add(key, newItem)
	c.mux.Unlock()
	if !ok {
		return &ErrTooBig{}
	}

	// By the time this function exits, we should either mark the LRU item as committed
	// (if the upload went well), or delete it. Capturing the flag variable is not very nice,
	// but this stuff is really easy to get wrong without defer().
	shouldCommit := false
	defer func() {
		c.mux.Lock()
		defer c.mux.Unlock()

		if shouldCommit {
			newItem.committed = true
		} else {
			c.lru.Remove(key)
		}
	}()

	// Download to a temporary file
	f, err := ioutil.TempFile(c.dir, "upload")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())

	if expectedSha256 != "" {
		hasher := sha256.New()
		if _, err = io.Copy(io.MultiWriter(f, hasher), r); err != nil {
			return
		}
		actualHash := hex.EncodeToString(hasher.Sum(nil))
		if actualHash != expectedSha256 {
			err = errors.New(fmt.Sprintf(
				"hashsums don't match. Expected %s, found %s", expectedSha256, actualHash))
			return
		}
	} else {
		if _, err = io.Copy(f, r); err != nil {
			return
		}
	}

	if err := f.Sync(); err != nil {
		return err
	}

	if err := f.Close(); err != nil {
		return err
	}

	// Rename to the final path
	blobPath := c.pathForKey(key)
	err = os.Rename(f.Name(), blobPath)
	// Only commit if renaming succeeded
	if err == nil {
		// This flag is used by the defer() block above.
		shouldCommit = true
	}

	return
}

func (c *fsCache) Get(key string, w http.ResponseWriter) (ok bool, err error) {
	ok = func() bool {
		c.mux.Lock()
		defer c.mux.Unlock()

		val, found := c.lru.Get(key)
		// Uncommitted (i.e. uploading items) should be reported as not ok
		return found && val.(*lruItem).committed
	}()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	blobPath := c.pathForKey(key)
	f, err := os.Open(blobPath)
	if err != nil {
		return
	}
	defer f.Close()

	_, err = io.Copy(w, f)
	return
}

func (c *fsCache) Contains(key string) (ok bool, err error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	val, found := c.lru.Get(key)
	return found && val.(*lruItem).committed, nil
}

func (c *fsCache) NumFiles() int {
	c.mux.Lock()
	defer c.mux.Unlock()
	return c.lru.Len()
}

func (c *fsCache) MaxSize() int64 {
	c.mux.Lock()
	defer c.mux.Unlock()
	return c.lru.MaxSize()
}

func (c *fsCache) CurrentSize() int64 {
	c.mux.Lock()
	defer c.mux.Unlock()
	return c.lru.CurrentSize()
}
