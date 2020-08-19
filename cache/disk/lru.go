package disk

import (
	"container/list"
)

type sizedItem interface {
	Size() int64
}

// Key is the type used for identifying cache items. For non-test code,
// this is a string of the form "<keyspace>/<hash>". Some test code uses
// ints for simplicity.
//
// TODO: update the test code to use strings too, then drop all the
// type assertions.
type Key interface{}

// EvictCallback is the type of callbacks that are invoked when items are evicted.
type EvictCallback func(key Key, value sizedItem)

// SizedLRU is an LRU cache that will keep its total size below maxSize by evicting
// items.
// SizedLRU is not thread-safe.
type SizedLRU interface {
	Add(key Key, value sizedItem) (ok bool)
	Get(key Key) (value sizedItem, ok bool)
	Remove(key Key)
	Len() int
	CurrentSize() int64 // Returns the current used + reserved size.
	MaxSize() int64

	Reserve(size int64) (ok bool)   // Reserve some amount of cache space.
	Unreserve(size int64) (ok bool) // Release some amount of reserved cache space.
}

type sizedLRU struct {
	// Eviction double-linked list. Most recently accessed elements are at the front.
	ll *list.List
	// Map to access the items in O(1) time
	cache        map[interface{}]*list.Element
	currentSize  int64 // Includes reserved size.
	reservedSize int64
	// SizedLRU will evict items as needed to maintain the total size of the cache
	// below maxSize.
	maxSize int64
	onEvict EvictCallback
}

type entry struct {
	key   Key
	value sizedItem
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
func (c *sizedLRU) Add(key Key, value sizedItem) (ok bool) {
	if value.Size() > c.maxSize {
		return false
	}

	var sizeDelta int64
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
func (c *sizedLRU) Get(key Key) (value sizedItem, ok bool) {
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

func (c *sizedLRU) Reserve(size int64) bool {
	if size < 0 || size > c.maxSize || c.reservedSize+size > c.maxSize {
		return false
	}

	// Evict elements until we are able to reserve enough space.
	for c.currentSize+size > c.maxSize {
		ele := c.ll.Back()
		if ele != nil {
			c.removeElement(ele)
		} else {
			return false // This should have been caught at the start.
		}
	}

	c.currentSize += size
	c.reservedSize += size
	return true
}

func (c *sizedLRU) Unreserve(size int64) bool {
	if size < 0 {
		return false
	}

	newC := c.currentSize - size
	newR := c.reservedSize - size

	if newC < 0 || newR < 0 {
		return false
	}

	c.currentSize = newC
	c.reservedSize = newR

	return true
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
