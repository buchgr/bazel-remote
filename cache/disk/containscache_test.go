package disk

import (
	"testing"
	"time"

	"github.com/buchgr/bazel-remote/v2/cache"
)

const (
	containsCacheBlob      = "9205adc12a2c8b65e7cd77918ff8e6e20f39bdd0b7fc4b984abfd690c79d80c1"
	containsCacheOtherBlob = "423789fae66b9539c5622134c580700a154a15e355af4e3311a4e12ee0c9d243"
)

// newTestContainsCache returns a cache whose clock the caller advances by hand.
func newTestContainsCache(maxEntries int, ttl time.Duration) (*containsCache, *time.Time) {
	now := time.Unix(0, 0)
	c := newContainsCache(maxEntries, ttl)
	c.now = func() time.Time { return now }
	return c, &now
}

func TestContainsCacheRemembers(t *testing.T) {
	t.Parallel()

	c, _ := newTestContainsCache(10, time.Minute)

	if _, found := c.Get(cache.CAS, containsCacheBlob); found {
		t.Error("Expected an unknown blob not to be found")
	}

	c.Add(cache.CAS, containsCacheBlob, 42)

	size, found := c.Get(cache.CAS, containsCacheBlob)
	if !found {
		t.Fatal("Expected a recorded blob to be found")
	}
	if size != 42 {
		t.Errorf("Expected the recorded size 42, found %d", size)
	}
}

func TestContainsCacheSeparatesKinds(t *testing.T) {
	t.Parallel()

	c, _ := newTestContainsCache(10, time.Minute)
	c.Add(cache.CAS, containsCacheBlob, 42)

	if _, found := c.Get(cache.AC, containsCacheBlob); found {
		t.Error("Expected the same hash under another kind not to be found")
	}
}

// A read must not renew the entry: a hot blob still gets re-checked.
func TestContainsCacheExpires(t *testing.T) {
	t.Parallel()

	c, now := newTestContainsCache(10, time.Minute)
	c.Add(cache.CAS, containsCacheBlob, 42)

	*now = now.Add(30 * time.Second)
	if _, found := c.Get(cache.CAS, containsCacheBlob); !found {
		t.Error("Expected the blob to still be recorded after half its lifetime")
	}

	*now = now.Add(31 * time.Second)
	if _, found := c.Get(cache.CAS, containsCacheBlob); found {
		t.Error("Expected the blob to be forgotten once its lifetime passed")
	}

	if len(c.entries) != 0 {
		t.Errorf("Expected reading an expired entry to drop it, found %d", len(c.entries))
	}
}

func TestContainsCacheDropsExpiredNobodyAsksAbout(t *testing.T) {
	t.Parallel()

	c, now := newTestContainsCache(10, time.Minute)
	c.Add(cache.CAS, containsCacheBlob, 42)

	*now = now.Add(time.Hour)
	c.Add(cache.CAS, containsCacheOtherBlob, 43)

	if len(c.entries) != 1 {
		t.Errorf("Expected the expired entry to be dropped, found %d entries", len(c.entries))
	}
	if _, found := c.Get(cache.CAS, containsCacheOtherBlob); !found {
		t.Error("Expected the entry that was just added to survive")
	}
}

func TestContainsCacheBound(t *testing.T) {
	t.Parallel()

	c, now := newTestContainsCache(2, time.Hour)

	hashes := []string{containsCacheBlob, containsCacheOtherBlob,
		"7f715e87ab77cfa3084ce8f7bb8f51e4059d02147b2139635673b7751004a170"}
	for _, hash := range hashes {
		c.Add(cache.CAS, hash, 42)
		*now = now.Add(time.Second)
	}

	if len(c.entries) != 2 {
		t.Fatalf("Expected the bound of 2 to hold, found %d entries", len(c.entries))
	}
	if _, found := c.Get(cache.CAS, hashes[0]); found {
		t.Error("Expected the entry closest to expiring to be the one dropped")
	}
	for _, hash := range hashes[1:] {
		if _, found := c.Get(cache.CAS, hash); !found {
			t.Errorf("Expected %s to be kept", hash)
		}
	}
}

func TestContainsCacheReplaces(t *testing.T) {
	t.Parallel()

	c, _ := newTestContainsCache(10, time.Minute)
	c.Add(cache.CAS, containsCacheBlob, 42)
	c.Add(cache.CAS, containsCacheBlob, 43)

	if len(c.entries) != 1 {
		t.Errorf("Expected one entry for one blob, found %d", len(c.entries))
	}

	size, found := c.Get(cache.CAS, containsCacheBlob)
	if !found {
		t.Fatal("Expected the blob to be found")
	}
	if size != 43 {
		t.Errorf("Expected the size from the later record, 43, found %d", size)
	}
}
