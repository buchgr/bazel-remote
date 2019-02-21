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
	"strconv"
	"strings"
	"time"
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
			err := os.MkdirAll(filepath.Join(dir, cache.CAS.String(), subDir), os.FileMode(0777))
			if err != nil {
				log.Fatal(err)
			}
			err = os.MkdirAll(filepath.Join(dir, cache.AC.String(), subDir), os.FileMode(0777))
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
		mux: &sync.RWMutex{},
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
	return nil
}

func migrateDirectory(dir string) error {
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

	// Sort in increasing order of atime
	sort.Slice(files, func(i int, j int) bool {
		return atime.Get(files[i].info).Before(atime.Get(files[j].info))
	})

	for _, f := range files {
		relPath := f.name[len(c.dir)+1:]
		c.lru.Add(relPath, &lruItem{
			size:      f.info.Size(),
			committed: true,
		})
	}
	return nil
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
	f, err := tempFile(c.dir, "upload")
	if err != nil {
		return
	}
	defer func() {
		if !shouldCommit {
			// Only delete the temp file if moving it didn't succeed.
			os.Remove(f.Name())
		}
	}()

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
		err = os.MkdirAll(path, os.FileMode(0777))
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

// These next functions come from ioutil.TempFile() but respect umask
// https://golang.org/src/io/ioutil/tempfile.go?s=1419:1477#L40

var rand uint32
var randmu sync.Mutex

func nextRandom() string {
	randmu.Lock()
	r := rand
	if r == 0 {
		r = reseed()
	}
	r = r*1664525 + 1013904223 // constants from Numerical Recipes
	rand = r
	randmu.Unlock()
	return strconv.Itoa(int(1e9 + r%1e9))[1:]
}

func reseed() uint32 {
	return uint32(time.Now().UnixNano() + int64(os.Getpid()))
}

func tempFile(dir, pattern string) (f *os.File, err error) {
	if dir == "" {
		dir = os.TempDir()
	}

	var prefix, suffix string
	if pos := strings.LastIndex(pattern, "*"); pos != -1 {
		prefix, suffix = pattern[:pos], pattern[pos+1:]
	} else {
		prefix = pattern
	}

	nconflict := 0
	for i := 0; i < 10000; i++ {
		name := filepath.Join(dir, prefix+nextRandom()+suffix)
		f, err = os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0666)
		if os.IsExist(err) {
			if nconflict++; nconflict > 10 {
				randmu.Lock()
				rand = reseed()
				randmu.Unlock()
			}
			continue
		}
		break
	}
	return
}
