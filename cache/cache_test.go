package cache

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"net/http/httptest"
	"bytes"
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

func TestCacheBasics(t *testing.T) {
	const KEY = "a-key"
	const CONTENTS = "hello"

	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	cache := NewFsCache(cacheDir, 100)

	checkItems(t, cache, 0, 0)

	// Non-existing item
	rr := httptest.NewRecorder()
	found, err := cache.Get(KEY, rr)
	if err != nil {
		t.Fatal()
	}
	if found != false {
		t.Fatal()
	}

	// Add an item
	err = cache.Put(KEY, int64(len(CONTENTS)), "", strings.NewReader(CONTENTS))
	if err != nil {
		t.Fatal()
	}

	// Dig into the internals to make sure that the cache state has been
	// updated correctly
	checkItems(t, cache, int64(len(CONTENTS)), 1)

	// Get the item back
	rr = httptest.NewRecorder()
	found, err = cache.Get(KEY, rr)
	if err != nil {
		t.Fatal()
	}
	if found != true {
		t.Fatal()
	}
	if bytes.Compare(rr.Body.Bytes(), []byte(CONTENTS)) != 0 {
		t.Fatal()
	}
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

func TestCacheExistingFiles(t *testing.T) {
	cacheDir := tempDir(t)
	//defer os.RemoveAll(cacheDir)

	ensureDirExists(filepath.Join(cacheDir, "cas"))
	ensureDirExists(filepath.Join(cacheDir, "ac"))

	items := []string{
		"cas/f53b46209596d170f7659a414c9ff9f6b545cf77ffd6e1cbe9bcc57e1afacfbd",
		"cas/fdce205a735f407ae2910426611893d99ed985e3d9a341820283ea0b7d046ee3",
		"ac/733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
	}

	actualCacheSize := int64(0)
	for _, it := range items {
		err := ioutil.WriteFile(filepath.Join(cacheDir, it), []byte(it), os.ModePerm)
		if err != nil {
			t.Fatal(err)
		}
		actualCacheSize += int64(len(it))
	}

	cache := NewFsCache(cacheDir, 10000)

	checkItems(t, cache, actualCacheSize, 3)
}

// Make sure that Cache returns ErrTooBig when trying to upload an item that's bigger
// than the maximum size.
func TestCacheTooBig(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	cache := NewFsCache(cacheDir, 100)

	err := cache.Put("a-key", 10000, "", strings.NewReader("hello"))
	if err == nil {
		t.Fatal()
	}
	switch err.(type) {
	case *ErrTooBig:
	default:
		t.Fatal()
	}
}
