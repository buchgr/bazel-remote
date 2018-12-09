package disk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchgr/bazel-remote/utils"

	"github.com/buchgr/bazel-remote/cache"
)

func tempDir(t *testing.T) string {
	dir, err := ioutil.TempDir("", "bazel-remote")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func checkItems(t *testing.T, cache *diskCache, expSize int64, expNum int) {
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
			numFiles++
		}
		return nil
	})

	if numFiles != expNum {
		t.Fatalf("expected %d files on disk, found %d", expNum, numFiles)
	}
}

const KEY = "a-key"
const CONTENTS = "hello"
const CONTENTS_HASH = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

func TestCacheBasics(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache := New(cacheDir, 100)

	checkItems(t, testCache.(*diskCache), 0, 0)

	// Non-existing item
	data, sizeBytes, err := testCache.Get(cache.CAS, CONTENTS_HASH)
	if err != nil {
		t.Fatal(err)
	}
	if data != nil {
		t.Fatal("expected the item not to exist")
	}

	// Add an item
	err = testCache.Put(cache.CAS, CONTENTS_HASH, int64(len(CONTENTS)), strings.NewReader(CONTENTS))
	if err != nil {
		t.Fatal(err)
	}

	// Dig into the internals to make sure that the cache state has been
	// updated correctly
	checkItems(t, testCache.(*diskCache), int64(len(CONTENTS)), 1)

	// Get the item back
	data, sizeBytes, err = testCache.Get(cache.CAS, CONTENTS_HASH)
	if err != nil {
		t.Fatal(err)
	}
	expectContentEquals(t, data, sizeBytes, []byte(CONTENTS))
}

func TestCacheEviction(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache := New(cacheDir, 10)

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
		err := testCache.Put(cache.AC, fmt.Sprintf("aa-%d", i), int64(i), strings.NewReader("hello"))
		if err != nil {
			t.Fatal(err)
		}

		checkItems(t, testCache.(*diskCache), thisExp.expSize, thisExp.expNum)
	}
}

func expectContentEquals(t *testing.T, data io.ReadCloser, sizeBytes int64, expectedContent []byte) {
	if data == nil {
		t.Fatal("expected the item to exist")
	}
	dataBytes, err := ioutil.ReadAll(data)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Compare(dataBytes, expectedContent) != 0 {
		t.Fatalf("expected response '%s', but received '%s'",
			dataBytes, CONTENTS)
	}
	if int64(len(dataBytes)) != sizeBytes {
		t.Fatalf("Expected sizeBytes to be '%d' but was '%d'", len(dataBytes), sizeBytes)
	}
}

func putGetCompare(kind cache.EntryKind, hash string, content string, testCache cache.Cache,
	t *testing.T) {
	err := testCache.Put(kind, hash, int64(len(content)), strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}

	data, sizeBytes, err := testCache.Get(kind, hash)
	if err != nil {
		t.Fatal(err)
	}
	// Get the item back
	expectContentEquals(t, data, sizeBytes, []byte(content))
}

func hashStr(content string) string {
	hashBytes := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hashBytes[:])
}

// Make sure that we can overwrite items if we upload the same key again.
func TestOverwrite(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache := New(cacheDir, 10)

	putGetCompare(cache.CAS, hashStr("hello"), "hello", testCache, t)
	putGetCompare(cache.CAS, hashStr("hello"), "hello", testCache, t)

	putGetCompare(cache.AC, hashStr("world"), "world1", testCache, t)
	putGetCompare(cache.AC, hashStr("world"), "world2", testCache, t)
}

func TestCacheExistingFiles(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)

	ensureDirExists(filepath.Join(cacheDir, "cas", "f5"))
	ensureDirExists(filepath.Join(cacheDir, "cas", "fd"))
	ensureDirExists(filepath.Join(cacheDir, "ac", "73"))

	items := []string{
		"cas/f5/f53b46209596d170f7659a414c9ff9f6b545cf77ffd6e1cbe9bcc57e1afacfbd",
		"cas/fd/fdce205a735f407ae2910426611893d99ed985e3d9a341820283ea0b7d046ee3",
		"ac/73/733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
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
	testCache := New(cacheDir, expectedSize)

	checkItems(t, testCache.(*diskCache), expectedSize, 3)

	// Adding a new file should evict items[0] (the oldest)
	err := testCache.Put(cache.CAS, CONTENTS_HASH, int64(len(CONTENTS)), strings.NewReader(CONTENTS))
	if err != nil {
		t.Fatal(err)
	}
	checkItems(t, testCache.(*diskCache), expectedSize, 3)
	found := testCache.Contains(cache.CAS, "f53b46209596d170f7659a414c9ff9f6b545cf77ffd6e1cbe9bcc57e1afacfbd")
	if found {
		t.Fatalf("%s should have been evicted", items[0])
	}
}

// Make sure that the cache returns http.StatusInsufficientStorage when trying to upload an item
// that's bigger than the maximum size.
func TestCacheBlobTooLarge(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache := New(cacheDir, 100)

	err := testCache.Put(cache.AC, hashStr("foo"), 10000, strings.NewReader(CONTENTS))
	if err == nil {
		t.Fatal("Expected an error")
	}

	if cerr, ok := err.(*cache.Error); ok {
		if cerr.Code != http.StatusInsufficientStorage {
			t.Fatalf("Expected error code %d but received %d", http.StatusInsufficientStorage, cerr.Code)
		}
	} else {
		t.Fatal("Expected error to be of type Error")
	}
}

// Make sure that Cache rejects an upload whose hashsum doesn't match
func TestCacheCorruptedFile(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache := New(cacheDir, 1000)

	err := testCache.Put(cache.CAS, hashStr("foo"), int64(len(CONTENTS)), strings.NewReader(CONTENTS))
	if err == nil {
		t.Fatal("expected hash mismatch error")
	}
}

func TestLoadExistingEntries(t *testing.T) {
	// Test that loading existing items works
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)
	acHash, err := testutils.CreateRandomFile(cacheDir+"/ac/", 1024)
	if err != nil {
		t.Fatal(err)
	}
	casHash, err := testutils.CreateRandomFile(cacheDir+"/cas/", 1024)
	if err != nil {
		t.Fatal(err)
	}

	testCache := New(cacheDir, 2048)
	if testCache.NumItems() != 2 {
		t.Fatalf("Expected test cache size 2 but was %d", testCache.NumItems())
	}
	if !testCache.Contains(cache.AC, acHash) {
		t.Fatalf("Expected cache to contain AC entry '%s'", acHash)
	}
	if !testCache.Contains(cache.CAS, casHash) {
		t.Fatalf("Expected cache to contain CAS entry '%s'", acHash)
	}

}
