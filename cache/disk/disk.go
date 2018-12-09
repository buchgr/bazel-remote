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
	"strings"
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
	// Create the directory structure.
	hexLetters := []byte("0123456789abcdef")
	for _, c1 := range hexLetters {
		for _, c2 := range hexLetters {
			subDir := string(c1) + string(c2)
			err := os.MkdirAll(filepath.Join(dir, "cas", subDir), os.FileMode(0744))
			if err != nil {
				log.Fatal(err)
			}
			err = os.MkdirAll(filepath.Join(dir, "ac", subDir), os.FileMode(0744))
			if err != nil {
				log.Fatal(err)
			}
		}
	}

	// The eviction callback deletes the file from disk.
	onEvict := func(key Key, value SizedItem) {
		// Only remove committed items (as temporary files have a different filename)
		if value.(*lruItem).committed {
			components := strings.SplitN(key.(string), "-", 2)
			filePath := cacheFilePath(strToKind(components[0]), dir, components[1])
			err := os.Remove(filePath)
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
		relPath := f.name[len(c.dir)+1:]
		var key string
		if strings.HasPrefix(relPath, "ac/") {
			key = "ac-"
		} else {
			key = "cas-"
		}
		key = key + relPath[len(key)+3:]
		c.lru.Add(key, &lruItem{
			size:      f.info.Size(),
			committed: true,
		})
	}
}

func (c *diskCache) Put(kind cache.EntryKind, hash string, size int64, r io.Reader) (err error) {
	c.mux.Lock()

	key := cacheKey(kind, hash)

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

	if kind == cache.CAS {
		hasher := sha256.New()
		if _, err = io.Copy(io.MultiWriter(f, hasher), r); err != nil {
			return
		}
		actualHash := hex.EncodeToString(hasher.Sum(nil))
		if actualHash != hash {
			err = fmt.Errorf(
				"hashsums don't match. Expected %s, found %s", key, actualHash)
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
	filePath := cacheFilePath(kind, c.dir, hash)
	err = os.Rename(f.Name(), filePath)
	// Only commit if renaming succeeded
	if err == nil {
		// This flag is used by the defer() block above.
		shouldCommit = true
	}

	return
}

func (c *diskCache) Get(kind cache.EntryKind, hash string) (data io.ReadCloser, sizeBytes int64, err error) {
	if !c.Contains(kind, hash) {
		return
	}

	blobPath := cacheFilePath(kind, c.dir, hash)

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

func (c *diskCache) Contains(kind cache.EntryKind, hash string) (ok bool) {
	c.mux.Lock()
	defer c.mux.Unlock()

	val, found := c.lru.Get(cacheKey(kind, hash))
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

func cacheKey(kind cache.EntryKind, hash string) string {
	if kind == cache.AC {
		return kindToStr(kind) + "-" + hash
	}
	return kindToStr(kind) + "-" + hash
}

func cacheDirPath(cacheDir string, kind cache.EntryKind, hash string) string {
	return filepath.Join(cacheDir, kindToStr(kind), hash[:2])
}

func cacheFilePath(kind cache.EntryKind, cacheDir string, hash string) string {
	return filepath.Join(cacheDirPath(cacheDir, kind, hash), hash)
}

func strToKind(str string) cache.EntryKind {
	if str == "ac" {
		return cache.AC
	}
	return cache.CAS
}

func kindToStr(kind cache.EntryKind) string {
	if kind == cache.AC {
		return "ac"
	}
	return "cas"
}
