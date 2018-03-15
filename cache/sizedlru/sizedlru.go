// Package sizedlru implements an LRU cache that bounds the total space occupied
// by the items it contains.

package sizedlru

import (
	"container/list"
)

type SizedItem interface {
	Size() int64
}

// SizedLRU is an LRU cache that will keep its total size below maxSize by evicting
// items. If maxSize is zero, eviction is disabled.
// SizedLRU is not thread-safe.
type SizedLRU struct {
	// Eviction double-linked list. Most recently accessed elements are at the front.
	ll *list.List
	// Map to access the items in O(1) time
	cache       map[interface{}]*list.Element
	currentSize int64
	// SizedLRU will evict items as needed to maintain the total size of the cache
	// below maxSize.
	maxSize int64
}

type Key interface{}

type entry struct {
	key   Key
	value SizedItem
}

// NewSizedLRU returns a new SizedLRU cache
func NewSizedLRU(maxSize int64) *SizedLRU {
	return &SizedLRU{
		maxSize: maxSize,
		ll:      list.New(),
		cache:   make(map[interface{}]*list.Element),
	}
}

// Add adds a (key, value) to the cache, evicting items as necessary. Add returns false (
// and does not add the item) if the item size is larger than the maximum size of the cache.
func (c *SizedLRU) Add(key Key, value SizedItem) (ok bool) {
	if c.maxSize != 0 && value.Size() > c.maxSize {
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
	for c.maxSize != 0 && c.currentSize+sizeDelta > c.maxSize {
		ele := c.ll.Back()
		if ele != nil {
			c.removeElement(ele)
		}
	}

	c.currentSize += sizeDelta

	return true
}

// Get looks up a key in the cache
func (c *SizedLRU) Get(key Key) (value SizedItem, ok bool) {
	if ele, hit := c.cache[key]; hit {
		c.ll.MoveToFront(ele)
		return ele.Value.(*entry).value, true
	}

	return
}

// Remove removes a (key, value) from the cache
func (c *SizedLRU) Remove(key Key) {
	if ele, hit := c.cache[key]; hit {
		c.removeElement(ele)
	}
}

// Len returns the number of items in the cache
func (c *SizedLRU) Len() int {
	return len(c.cache)
}

func (c *SizedLRU) CurrentSize() int64 {
	return c.currentSize
}

func (c *SizedLRU) MaxSize() int64 {
	return c.maxSize
}

func (c *SizedLRU) removeElement(e *list.Element) {
	c.ll.Remove(e)
	kv := e.Value.(*entry)
	delete(c.cache, kv.key)
	c.currentSize -= kv.value.Size()
}
