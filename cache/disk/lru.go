package disk

import (
	"container/list"
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// Key is the type used for identifying cache items. For non-test code,
// this is a string of the form "<keyspace>/<hash>". Some test code uses
// ints for simplicity.
//
// TODO: update the test code to use strings too, then drop all the
// type assertions.
type Key interface{}

// EvictCallback is the type of callbacks that are invoked when items are evicted.
type EvictCallback func(key Key, value lruItem)

// SizedLRU is an LRU cache that will keep its total size below maxSize by evicting
// items.
// SizedLRU is not thread-safe.
type SizedLRU struct {
	// Eviction double-linked list. Most recently accessed elements are at the front.
	ll *list.List
	// Map to access the items in O(1) time
	cache map[interface{}]*list.Element

	// Total cache size including reserved bytes and estimated filesystem overhead.
	currentSize int64

	// Total size of all blobs in uncompressed form.
	// This does not include reserved space.
	uncompressedSize int64

	// Number of bytes reserved for incoming blobs.
	reservedSize int64

	// SizedLRU will evict items as needed to maintain the total size of the
	// cache below maxSize.
	maxSize int64

	// Channel containing evicted entries removed from ll, but not yet
	// removed from file system.
	//
	// The entries are wrapped in a slice to allow the queue to grow
	// dynamically and not being limited by the channel's max size. Note
	// that one single new large file can result in evicting thousands of
	// small old files. And on high load, with many new files, the queue
	// of evicting entries aggregates and can grow quickly.
	//
	// The consumer of the channel does not have to bother about the
	// diskCache.mu mutex.
	//
	// The removal of evicted files asynchronously improves the latency for
	// Put requests that can start writing the new file earlier. And in
	// addition, improves latency for all requests by not having to hold
	// the diskCache.mu mutex during file system remove syscalls.
	queuedEvictionsChan chan []*entry

	onEvict EvictCallback

	gaugeCacheSizeBytes     prometheus.Gauge
	gaugeCacheLogicalBytes  prometheus.Gauge
	counterEvictedBytes     prometheus.Counter
	counterOverwrittenBytes prometheus.Counter
	summaryCacheItemBytes   prometheus.Summary
}

type entry struct {
	key   Key
	value lruItem
}

// Actual disk usage will be estimated by rounding file sizes up to the
// nearest multiple of this number.
const BlockSize = 4096

// NewSizedLRU returns a new SizedLRU cache
func NewSizedLRU(maxSize int64, onEvict EvictCallback, initialCapacity int) SizedLRU {
	return SizedLRU{
		maxSize: maxSize,
		ll:      list.New(),
		cache:   make(map[interface{}]*list.Element, initialCapacity),
		onEvict: onEvict,

		gaugeCacheSizeBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "bazel_remote_disk_cache_size_bytes",
			Help: "The current number of bytes in the disk backend",
		}),
		gaugeCacheLogicalBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "bazel_remote_disk_cache_logical_bytes",
			Help: "The current number of bytes in the disk backend if they were uncompressed",
		}),
		counterEvictedBytes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "bazel_remote_disk_cache_evicted_bytes_total",
			Help: "The total number of bytes evicted from disk backend, due to full cache",
		}),
		counterOverwrittenBytes: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "bazel_remote_disk_cache_overwritten_bytes_total",
			Help: "The total number of bytes removed from disk backend, due to put of already existing key",
		}),
		summaryCacheItemBytes: prometheus.NewSummary(prometheus.SummaryOpts{
			Name: "bazel_remote_disk_cache_entry_bytes",
			Help: "Size of cache entries",
			Objectives: map[float64]float64{
				0.5:  0.05,
				0.9:  0.01,
				0.99: 0.001,
				1:    0,
			},
		}),
		queuedEvictionsChan: make(chan []*entry, 1),
	}
}

func (c *SizedLRU) RegisterMetrics() {
	prometheus.MustRegister(c.gaugeCacheSizeBytes)
	prometheus.MustRegister(c.gaugeCacheLogicalBytes)
	prometheus.MustRegister(c.counterEvictedBytes)
	prometheus.MustRegister(c.counterOverwrittenBytes)
	prometheus.MustRegister(c.summaryCacheItemBytes)
}

// Add adds a (key, value) to the cache, evicting items as necessary.
// Add returns false and does not add the item if the item size is
// larger than the maximum size of the cache, or if the item cannot
// be added to the cache because too much space is reserved.
//
// Note that this function rounds file sizes up to the nearest
// BlockSize (4096) bytes, as an estimate of actual disk usage since
// most linux filesystems default to 4kb blocks.
func (c *SizedLRU) Add(key Key, value lruItem) (ok bool) {

	roundedUpSizeOnDisk := roundUp4k(value.sizeOnDisk)

	if roundedUpSizeOnDisk > c.maxSize {
		return false
	}

	var sizeDelta, uncompressedSizeDelta int64
	if ee, ok := c.cache[key]; ok {
		sizeDelta = roundedUpSizeOnDisk - roundUp4k(ee.Value.(*entry).value.sizeOnDisk)
		if c.reservedSize+sizeDelta > c.maxSize {
			return false
		}
		uncompressedSizeDelta = roundUp4k(value.size) - roundUp4k(ee.Value.(*entry).value.size)
		c.ll.MoveToFront(ee)
		c.counterOverwrittenBytes.Add(float64(ee.Value.(*entry).value.sizeOnDisk))

		kv := ee.Value.(*entry)
		kvCopy := &entry{kv.key, kv.value}
		c.appendEvictionToQueue(kvCopy)

		ee.Value.(*entry).value = value
	} else {
		sizeDelta = roundedUpSizeOnDisk
		if c.reservedSize+sizeDelta > c.maxSize {
			return false
		}
		uncompressedSizeDelta = roundUp4k(value.size)
		ele := c.ll.PushFront(&entry{key, value})
		c.cache[key] = ele
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
	c.uncompressedSize += uncompressedSizeDelta

	c.gaugeCacheSizeBytes.Set(float64(c.currentSize))
	c.gaugeCacheLogicalBytes.Set(float64(c.uncompressedSize))
	c.summaryCacheItemBytes.Observe(float64(sizeDelta))

	return true
}

// Get looks up a key in the cache
func (c *SizedLRU) Get(key Key) (value lruItem, ok bool) {
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
		c.gaugeCacheSizeBytes.Set(float64(c.currentSize))
		c.gaugeCacheLogicalBytes.Set(float64(c.uncompressedSize))
	}
}

// Len returns the number of items in the cache
func (c *SizedLRU) Len() int {
	return len(c.cache)
}

func (c *SizedLRU) TotalSize() int64 {
	return c.currentSize
}

func (c *SizedLRU) UncompressedSize() int64 {
	return c.uncompressedSize
}

func (c *SizedLRU) ReservedSize() int64 {
	return c.reservedSize
}

func (c *SizedLRU) MaxSize() int64 {
	return c.maxSize
}

// This assumes that a is positive, b is non-negative, and c is positive.
func sumLargerThan(a, b, c int64) bool {
	sum := a + b
	if sum > c {
		return true
	}

	if sum <= 0 {
		// This indicates int64 overflow occurred.
		return true
	}

	return false
}

var errReservation = errors.New("internal reservation error")

func (c *SizedLRU) Reserve(size int64) (bool, error) {
	if size == 0 {
		return true, nil
	}

	if size < 0 {
		return false, fmt.Errorf("Invalid negative blob size: %d", size)
	}

	if size > c.maxSize {
		return false, fmt.Errorf("Unable to reserve space for blob (size: %d) larger than cache size %d", size, c.maxSize)
	}

	if sumLargerThan(size, c.reservedSize, c.maxSize) {
		// If size + c.reservedSize is larger than c.maxSize
		// then we cannot evict enough items to make enough
		// space.
		return false, fmt.Errorf("INTERNAL ERROR: unable to reserve enough space for blob with size %d (undersized cache?)", size)
	}

	// Evict elements until we are able to reserve enough space.
	for sumLargerThan(size, c.currentSize, c.maxSize) {
		ele := c.ll.Back()
		if ele != nil {
			c.removeElement(ele)
		} else {
			return false, errReservation // This should have been caught at the start.
		}
	}

	c.currentSize += size
	c.reservedSize += size
	return true, nil
}

func (c *SizedLRU) Unreserve(size int64) error {
	if size == 0 {
		return nil
	}

	if size < 0 {
		return fmt.Errorf("INTERNAL ERROR: should not try to unreserve negative value: %d", size)
	}

	newC := c.currentSize - size
	newR := c.reservedSize - size

	if newC < 0 || newR < 0 {
		return fmt.Errorf("INTERNAL ERROR: failed to unreserve: %d", size)
	}

	c.currentSize = newC
	c.reservedSize = newR

	return nil
}

func (c *SizedLRU) removeElement(e *list.Element) {
	c.ll.Remove(e)
	kv := e.Value.(*entry)
	delete(c.cache, kv.key)
	c.currentSize -= roundUp4k(kv.value.sizeOnDisk)
	c.uncompressedSize -= roundUp4k(kv.value.size)
	c.counterEvictedBytes.Add(float64(kv.value.sizeOnDisk))
	c.appendEvictionToQueue(kv)
}

// Round n up to the nearest multiple of BlockSize (4096).
func roundUp4k(n int64) int64 {
	return (n + BlockSize - 1) & -BlockSize
}

// Get the back item of the LRU cache.
func (c *SizedLRU) getTailItem() (Key, lruItem) {
	ele := c.ll.Back()
	if ele != nil {
		kv := ele.Value.(*entry)
		return kv.key, kv.value
	}
	return nil, lruItem{}
}

// Append an entry to the eviction queue. The entry must have been removed
// from SizedLRU.ll before being sent to this method.
// Note that this method can be invoked without holding the diskCache.mu mutex,
// but it is guaranteed to never block for full channel buffer as long as
// it is invoked only when holding the diskCache.mu mutex and no one else tries
// to send to queuedEvictionsChan concurrently.
func (c *SizedLRU) appendEvictionToQueue(e *entry) {
	select {
	case queuedEvictions := <-c.queuedEvictionsChan:
		c.queuedEvictionsChan <- append(queuedEvictions, e)
	default:
		c.queuedEvictionsChan <- []*entry{e}
	}
}

// Block waiting for a slice of evicted entries and then remove them from
// file system. Note that one single slice could theoretically contain
// millions of entries in overload situations.
// Note that this method may be invoked without holding the diskCache.mu mutex.
func (c *SizedLRU) performQueuedEvictions() {
	for _, kv := range <-c.queuedEvictionsChan {
		c.onEvict(kv.key, kv.value)
	}
}

// Note that this method may be invoked without holding the diskCache.mu mutex.
func (c *SizedLRU) performQueuedEvictionsContinuously() {
	for {
		c.performQueuedEvictions()
	}
}
