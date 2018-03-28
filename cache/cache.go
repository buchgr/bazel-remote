package cache

import (
	"os"
	"path/filepath"
	"sync"
)

// Cache ...
type Cache interface {
	Dir() string
	CurrSize() int64
	MaxSize() int64
	AddFile(hash string, size int64)
	RemoveFile(hash string) int64
	ContainsFile(hash string) bool
	NumFiles() int
	LoadExistingFiles()
}

type cache struct {
	dir      string
	currSize int64
	maxSize  int64
	files    map[string]int64
	mux      *sync.RWMutex
}

func (c *cache) Dir() string {
	return c.dir
}

func (c *cache) CurrSize() int64 {
	c.mux.RLock()
	defer c.mux.RUnlock()
	return c.currSize
}

func (c *cache) MaxSize() int64 {
	return c.maxSize
}

func (c *cache) AddFile(hash string, size int64) {
	c.mux.Lock()
	defer c.mux.Unlock()
	_, ok := c.files[hash]
	if !ok {
		c.files[hash] = size
		c.currSize += size
	}
}

func (c *cache) RemoveFile(hash string) int64 {
	c.mux.Lock()
	defer c.mux.Unlock()
	size := c.files[hash]
	c.currSize -= size
	delete(c.files, hash)
	return size
}

func (c *cache) ContainsFile(hash string) bool {
	c.mux.Lock()
	defer c.mux.Unlock()
	_, ok := c.files[hash]
	return ok
}

func (c *cache) NumFiles() int {
	c.mux.Lock()
	defer c.mux.Unlock()
	return len(c.files)
}

// LoadExistingFiles walks the filesystem for existing files, and adds them to the
// in-memory index.
func (c *cache) LoadExistingFiles() {
	filepath.Walk(c.Dir(), func(name string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			c.AddFile(name, info.Size())
		}
		return nil
	})
}

// NewCache ...
func NewCache(dir string, maxSizeBytes int64) Cache {
	return &cache{
		dir:      dir,
		currSize: 0,
		maxSize:  maxSizeBytes,
		files:    make(map[string]int64),
		mux:      &sync.RWMutex{},
	}
}
