package cache

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tempDir(t *testing.T) string {
	dir, err := ioutil.TempDir("", "bazel-remote")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func checkItems(t *testing.T, cache *fsCache, expSize int64, expNum int) {
	if cache.lru.Len() != expNum {
		t.Fatalf("expected %d files in the cache, found %d", expNum, cache.lru.Len())
	}
	if cache.lru.CurrentSize() != expSize {
		t.Fatalf("expected %d bytes in the cache, found %d", expSize, cache.lru.CurrentSize())
	}

	// Dig into the internals of the cache to make sure that all items are committed.
	for _, it := range cache.lru.(*sizedLRU).cache {
		if it.Value.(*entry).value.(*lruItem).committed != true {
			t.Fatalf("expected committed = true")
		}
	}

	numFiles := 0
	filepath.Walk(cache.dir, func(name string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			numFiles += 1
		}
		return nil
	})

	if numFiles != expNum {
		t.Fatalf("expected %d files on disk, found %d", expNum, numFiles)
	}
}

func expectContentEquals(t *testing.T, c Cache, key string, content []byte) {
	rr := httptest.NewRecorder()
	found, err := c.Get(key, rr)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("expected item at key %s to exist", key)
	}
	if bytes.Compare(rr.Body.Bytes(), []byte(content)) != 0 {
		t.Fatalf("expected item at key %s to contain '%s', but got '%s'",
			rr.Body.Bytes(), content)
	}
}

const KEY = "a-key"
const CONTENTS = "hello"
const CONTENTS_HASH = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

func TestCacheBasics(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	cache := NewFsCache(cacheDir, 100)

	checkItems(t, cache, 0, 0)

	// Non-existing item
	rr := httptest.NewRecorder()
	found, err := cache.Get(KEY, rr)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected the item not to exist")
	}

	// Add an item
	err = cache.Put(KEY, int64(len(CONTENTS)), CONTENTS_HASH, strings.NewReader(CONTENTS))
	if err != nil {
		t.Fatal(err)
	}

	// Dig into the internals to make sure that the cache state has been
	// updated correctly
	checkItems(t, cache, int64(len(CONTENTS)), 1)

	// Get the item back
	expectContentEquals(t, cache, KEY, []byte(CONTENTS))
}

func TestCacheEviction(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	cache := NewFsCache(cacheDir, 10)

	expectedSizesNumItems := []struct {
		expSize int64
		expNum  int
	}{
		{0, 1},  // 0
		{1, 2},  // 0, 1
		{3, 3},  // 0, 1, 2
		{6, 4},  // 0, 1, 2, 3
		{10, 5}, // 0, 1, 2, 3, 4
		{9, 2},  // 4, 5
		{6, 1},  // 6
		{7, 1},  // 7
	}

	for i, thisExp := range expectedSizesNumItems {
		err := cache.Put(fmt.Sprintf("key-%d", i), int64(i), "", strings.NewReader("hello"))
		if err != nil {
			t.Fatal(err)
		}

		checkItems(t, cache, thisExp.expSize, thisExp.expNum)
	}
}

// Make sure that we can overwrite items if we upload the same key again.
func TestOverwrite(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	cache := NewFsCache(cacheDir, 10)

	oldContent := "Hello"
	newContent := "World"

	err := cache.Put(KEY, 1, "", strings.NewReader(oldContent));
	if err != nil {
		t.Fatal(err)
	}

	// Get the item back
	expectContentEquals(t, cache, KEY, []byte(oldContent))

	// Overwrite
	err = cache.Put(KEY, 1, "", strings.NewReader(newContent));
	if err != nil {
		t.Fatal(err)
	}

	// Get the item back again
	expectContentEquals(t, cache, KEY, []byte(newContent))
}

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
	cache := NewFsCache(cacheDir, expectedSize)

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

// Make sure that Cache returns ErrTooBig when trying to upload an item that's bigger
// than the maximum size.
func TestCacheTooBig(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	cache := NewFsCache(cacheDir, 100)

	err := cache.Put("a-key", 10000, "", strings.NewReader(CONTENTS))
	if err == nil {
		t.Fatal("expected ErrTooBig")
	}
	switch err.(type) {
	case *ErrTooBig:
	default:
		t.Fatal("expected ErrTooBig")
	}
}

// Make sure that Cache rejects an upload whose hashsum doesn't match
func TestCacheCorruptedFile(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	cache := NewFsCache(cacheDir, 1000)

	err := cache.Put(KEY, int64(len(CONTENTS)), strings.Repeat("x", 64), strings.NewReader(CONTENTS))
	if err == nil {
		t.Fatal("expected hash mismatch error")
	}
}
