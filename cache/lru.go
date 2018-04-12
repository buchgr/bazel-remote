package cache

import (
	"container/list"
)

type SizedItem interface {
	Size() int64
}

type Key interface{}

// EvictCallback is the type of callbacks that are invoked when items are evicted.
type EvictCallback func(key Key, value SizedItem)

// SizedLRU is an LRU cache that will keep its total size below maxSize by evicting
// items.
// SizedLRU is not thread-safe.
type SizedLRU interface {
	Add(key Key, value SizedItem) (ok bool)
	Get(key Key) (value SizedItem, ok bool)
	Remove(key Key)
	Len() int
	CurrentSize() int64
	MaxSize() int64
}

type sizedLRU struct {
	// Eviction double-linked list. Most recently accessed elements are at the front.
	ll *list.List
	// Map to access the items in O(1) time
	cache       map[interface{}]*list.Element
	currentSize int64
	// SizedLRU will evict items as needed to maintain the total size of the cache
	// below maxSize.
	maxSize int64
	onEvict EvictCallback
}

type entry struct {
	key   Key
	value SizedItem
}

// NewSizedLRU returns a new sizedLRU cache
func NewSizedLRU(maxSize int64, onEvict EvictCallback) SizedLRU {
	return &sizedLRU{
		maxSize: maxSize,
		ll:      list.New(),
		cache:   make(map[interface{}]*list.Element),
		onEvict: onEvict,
	}
}

// Add adds a (key, value) to the cache, evicting items as necessary. Add returns false (
// and does not add the item) if the item size is larger than the maximum size of the cache.
func (c *sizedLRU) Add(key Key, value SizedItem) (ok bool) {
	if value.Size() > c.maxSize {
		return false
	}

	sizeDelta := int64(0)
	if ee, ok := c.cache[key]; ok {
		c.ll.MoveToFront(ee)
		sizeDelta = value.Size() - ee.Value.(*entry).value.Size()
		ee.Value.(*entry).value = value
	} else {
		ele := c.ll.PushFront(&entry{key, value})
		c.cache[key] = ele
		sizeDelta = value.Size()
	}

	// Eviction. This is needed even if the key was already present, since the size of the
	// value might have changed, pushing the total size over maxSize.
	for c.currentSize+sizeDelta > c.maxSize {
		ele := c.ll.Back()
		if ele != nil {
			c.removeElement(ele)
		}
	}

	c.currentSize += sizeDelta

	return true
}

// Get looks up a key in the cache
func (c *sizedLRU) Get(key Key) (value SizedItem, ok bool) {
	if ele, hit := c.cache[key]; hit {
		c.ll.MoveToFront(ele)
		return ele.Value.(*entry).value, true
	}

	return
}

// Remove removes a (key, value) from the cache
func (c *sizedLRU) Remove(key Key) {
	if ele, hit := c.cache[key]; hit {
		c.removeElement(ele)
	}
}

// Len returns the number of items in the cache
func (c *sizedLRU) Len() int {
	return len(c.cache)
}

func (c *sizedLRU) CurrentSize() int64 {
	return c.currentSize
}

func (c *sizedLRU) MaxSize() int64 {
	return c.maxSize
}

func (c *sizedLRU) removeElement(e *list.Element) {
	c.ll.Remove(e)
	kv := e.Value.(*entry)
	delete(c.cache, kv.key)
	c.currentSize -= kv.value.Size()

	if c.onEvict != nil {
		c.onEvict(kv.key, kv.value)
	}
}
