package cache

import (
	"sync"
)

// Cache ...
type Cache interface {
	Dir() string
	CurrSize() int64
	MaxSize() int64
	AddCurrSize(bytes int64)
}

type cache struct {
	dir      string
	currSize int64
	maxSize  int64
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

func (c *cache) AddCurrSize(bytes int64) {
	c.mux.Lock()
	defer c.mux.Unlock()
	c.currSize += bytes
}

// NewCache ...
func NewCache(dir string, maxSizeBytes int64, initialSize int64) Cache {
	return &cache{
		dir:      dir,
		currSize: initialSize,
		maxSize:  maxSizeBytes,
		mux:      &sync.RWMutex{},
	}
}
