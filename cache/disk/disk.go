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
	mux *sync.RWMutex
	lru SizedLRU
}

// New returns a new instance of a filesystem-based cache rooted at `dir`,
// with a maximum size of `maxSizeBytes` bytes.
func New(dir string, maxSizeBytes int64) cache.Cache {
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

	cache := &diskCache{
		dir: filepath.Clean(dir),
		mux: &sync.RWMutex{},
		lru: NewSizedLRU(maxSizeBytes, onEvict),
	}

	cache.loadExistingFiles()

	return cache
}

func (c *diskCache) pathForKey(key string) string {
	return filepath.Join(c.dir, key)
}

// loadExistingFiles lists all files in the cache directory, and adds them to the
// LRU index so that they can be served. Files are sorted by access time first,
// so that the eviction behavior is preserved across server restarts.
func (c *diskCache) loadExistingFiles() {
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

func (c *diskCache) Put(key string, size int64, expectedSha256 string, r io.Reader) (err error) {
	c.mux.Lock()

	// If there's an ongoing upload (i.e. cache key is present in uncommitted state),
	// we drop the upload and discard the incoming stream. We do accept uploads
	// of existing keys, as it should happen relatively rarely (e.g. race
	// condition on the bazel side) but it's useful to overwrite poisoned items.
	if existingItem, found := c.lru.Get(key); found {
		if !existingItem.(*lruItem).committed {
			c.mux.Unlock()
			io.Copy(ioutil.Discard, r)
			return
		}
	}

	// Try to add the item to the LRU.
	newItem := &lruItem{
		size:      size,
		committed: false,
	}
	ok := c.lru.Add(key, newItem)
	c.mux.Unlock()
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
			err = fmt.Errorf(
				"hashsums don't match. Expected %s, found %s", expectedSha256, actualHash)
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

func (c *diskCache) Get(key string, actionCache bool) (data io.ReadCloser, sizeBytes int64, err error) {
	if !c.Contains(key, actionCache) {
		return
	}

	blobPath := c.pathForKey(key)

	fileInfo, err := os.Stat(blobPath)
	if err != nil {
		return
	}
	sizeBytes = fileInfo.Size()

	data, err = os.Open(blobPath)
	if err != nil {
		return
	}

	return
}

func (c *diskCache) Contains(key string, actionCache bool) (ok bool) {
	c.mux.Lock()
	defer c.mux.Unlock()

	val, found := c.lru.Get(key)
	// Uncommitted (i.e. uploading items) should be reported as not ok
	return found && val.(*lruItem).committed
}

func (c *diskCache) NumItems() int {
	c.mux.Lock()
	defer c.mux.Unlock()
	return c.lru.Len()
}

func (c *diskCache) MaxSize() int64 {
	c.mux.Lock()
	defer c.mux.Unlock()
	return c.lru.MaxSize()
}

func (c *diskCache) CurrentSize() int64 {
	c.mux.Lock()
	defer c.mux.Unlock()
	return c.lru.CurrentSize()
}

func ensureDirExists(path string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		err = os.MkdirAll(path, os.FileMode(0744))
		if err != nil {
			log.Fatal(err)
		}
	}
}
