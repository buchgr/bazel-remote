package cache

import (
	"testing"
	"os"
	"path/filepath"
	"io/ioutil"
	"time"
	"strings"
)

// Tests specific to `fsBlobStore`.

func TestCacheExistingFiles(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)

	ensureDirExists(filepath.Join(cacheDir, "cas"))
	ensureDirExists(filepath.Join(cacheDir, "ac"))

	items := []string{
		"cas/f53b46209596d170f7659a414c9ff9f6b545cf77ffd6e1cbe9bcc57e1afacfbd",
		"cas/fdce205a735f407ae2910426611893d99ed985e3d9a341820283ea0b7d046ee3",
		"ac/733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
	}

	for _, it := range items {
		err := ioutil.WriteFile(filepath.Join(cacheDir, it), []byte(CONTENTS), os.ModePerm)
		if err != nil {
			t.Fatal(err)
		}
		// Wait a bit to account for atime granularity
		time.Sleep(10 * time.Millisecond)
	}

	const expectedSize = 3 * int64(len(CONTENTS))
	cache := NewFsBlobStore(cacheDir, expectedSize)

	checkItems(t, cache, expectedSize, 3)

	// Adding a new file should evict items[0] (the oldest)
	err := cache.Put("a-key", int64(len(CONTENTS)), CONTENTS_HASH, strings.NewReader(CONTENTS))
	if err != nil {
		t.Fatal(err)
	}
	checkItems(t, cache, expectedSize, 3)
	found, err := cache.Contains(items[0])
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("%s should have been evicted", items[0])
	}
}
