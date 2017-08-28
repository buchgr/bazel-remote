package cache

import (
	"sync"
)

// Cache ...
type Cache interface {
	Dir() string
	CurrSize() int64
	MaxSize() int64
	AddFile(hash string, size int64)
	RemoveFile(hash string)
	ContainsFile(hash string) bool
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

func (c *cache) RemoveFile(hash string) {
	c.mux.Lock()
	defer c.mux.Unlock()
	c.currSize -= c.files[hash]
	delete(c.files, hash)
}

func (c *cache) ContainsFile(hash string) bool {
	c.mux.Lock()
	defer c.mux.Unlock()
	_, ok := c.files[hash]
	return ok
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
