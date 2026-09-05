package disk

import (
	"container/list"
	"sync"
	"time"

	"github.com/buchgr/bazel-remote/v2/cache"

	"github.com/prometheus/client_golang/prometheus"
)

// Track which blobs the proxy backend has, so that they are not HEADed again on every request.
//
// Only positive results are cached. A blob can appear at any time from an upload, so a cached
// "missing" would hide it until expiry. A cached "present" for a deleted blob just fails the
// following fetch, same as without the cache.
//
// Entries expire a fixed time after the check, and a read does not renew them: the point of the
// TTL is to re-check the backend. Insertion order is thus expiry order, so the size limit drops
// the oldest entries.
type containsCache struct {
	ttl        time.Duration
	maxEntries int

	// Replaced in tests.
	now func() time.Time

	mu      sync.Mutex
	order   *list.List // Of *containsEntry, newest at the front.
	entries map[string]*list.Element

	counter *prometheus.CounterVec
}

type containsEntry struct {
	key     string
	size    int64
	expires time.Time
}

func newContainsCache(maxEntries int, ttl time.Duration) *containsCache {
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "bazel_remote_proxy_contains_cache_requests_total",
		Help: "Proxy existence checks answered from the cache (hit) or sent to the backend (miss)",
	}, []string{"status"})
	counter.WithLabelValues(hitStatus).Add(0)
	counter.WithLabelValues(missStatus).Add(0)

	return &containsCache{
		ttl:        ttl,
		maxEntries: maxEntries,
		now:        time.Now,
		order:      list.New(),
		entries:    make(map[string]*list.Element),
		counter:    counter,
	}
}

// Returns the cached size of a blob, or -1 if it is not cached or expired.
func (c *containsCache) Get(kind cache.EntryKind, hash string) (int64, bool) {
	key := cache.LookupKey(kind, hash)

	c.mu.Lock()
	defer c.mu.Unlock()

	if element, found := c.entries[key]; found {
		entry := element.Value.(*containsEntry)
		if c.now().Before(entry.expires) {
			c.counter.WithLabelValues(hitStatus).Inc()
			return entry.size, true
		}
		c.remove(element)
	}

	c.counter.WithLabelValues(missStatus).Inc()
	return -1, false
}

// Caches that the backend has a blob, replacing an earlier entry.
func (c *containsCache) Add(kind cache.EntryKind, hash string, size int64) {
	key := cache.LookupKey(kind, hash)

	c.mu.Lock()
	defer c.mu.Unlock()

	if element, found := c.entries[key]; found {
		c.remove(element)
	}

	entry := &containsEntry{key: key, size: size, expires: c.now().Add(c.ttl)}
	c.entries[key] = c.order.PushFront(entry)

	// Drop expired and over-limit entries from the oldest end.
	for element := c.order.Back(); element != nil; element = c.order.Back() {
		if c.order.Len() <= c.maxEntries && c.now().Before(element.Value.(*containsEntry).expires) {
			break
		}
		c.remove(element)
	}
}

// Caller must hold mu.
func (c *containsCache) remove(element *list.Element) {
	c.order.Remove(element)
	delete(c.entries, element.Value.(*containsEntry).key)
}
