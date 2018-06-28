package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"

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

// NewFsBlobStore returns a new instance of a filesystem-based BlobStore rooted at `dir`,
// with a maximum size of `maxSizeBytes` bytes.
func NewFsBlobStore(dir string, maxSizeBytes int64) *fsBlobStore {
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

	cache := &fsBlobStore{
		dir: filepath.Clean(dir),
		mux: &sync.RWMutex{},
		lru: NewSizedLRU(maxSizeBytes, onEvict),
	}

	cache.loadExistingFiles()

	return cache
}

// fsBlobStore is a BlobStore implementation backed by files on a filesystem.
type fsBlobStore struct {
	dir string
	mux *sync.RWMutex
	lru SizedLRU
}

func (c *fsBlobStore) pathForKey(key string) string {
	return filepath.Join(c.dir, key)
}

// loadExistingFiles lists all files in the blobStore directory, and adds them to the
// LRU index so that they can be served. Files are sorted by access time first,
// so that the eviction behavior is preserved across server restarts.
func (c *fsBlobStore) loadExistingFiles() {
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

func (c *fsBlobStore) Put(key string, size int64, expectedSha256 string, r io.Reader) (err error) {
	c.mux.Lock()

	// If there's an ongoing upload (i.e. the blob key is present in uncommitted state),
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

func (c *fsBlobStore) Get(key string, w http.ResponseWriter) (ok bool, err error) {
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

	fileInfo, err := os.Stat(blobPath)
	if err != nil {
		return
	}

	f, err := os.Open(blobPath)
	if err != nil {
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(fileInfo.Size(), 10))

	_, err = io.Copy(w, f)
	return
}

func (c *fsBlobStore) Contains(key string) (ok bool, err error) {
	c.mux.Lock()
	defer c.mux.Unlock()

	val, found := c.lru.Get(key)
	return found && val.(*lruItem).committed, nil
}

func (c *fsBlobStore) NumItems() int {
	c.mux.Lock()
	defer c.mux.Unlock()
	return c.lru.Len()
}

func (c *fsBlobStore) MaxSize() int64 {
	c.mux.Lock()
	defer c.mux.Unlock()
	return c.lru.MaxSize()
}

func (c *fsBlobStore) CurrentSize() int64 {
	c.mux.Lock()
	defer c.mux.Unlock()
	return c.lru.CurrentSize()
}
