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

	testutils "github.com/buchgr/bazel-remote/utils"

	"github.com/buchgr/bazel-remote/cache"
)

func tempDir(t *testing.T) string {
	dir, err := ioutil.TempDir("", "bazel-remote")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func checkItems(cache *diskCache, expSize int64, expNum int) error {
	if cache.lru.Len() != expNum {
		return fmt.Errorf("expected %d files in the cache, found %d", expNum, cache.lru.Len())
	}
	if cache.lru.CurrentSize() != expSize {
		return fmt.Errorf("expected %d bytes in the cache, found %d", expSize, cache.lru.CurrentSize())
	}

	// Dig into the internals of the cache to make sure that all items are committed.
	for _, it := range cache.lru.(*sizedLRU).cache {
		if it.Value.(*entry).value.(*lruItem).committed != true {
			return fmt.Errorf("expected committed = true")
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
		return fmt.Errorf("expected %d files on disk, found %d", expNum, numFiles)
	}

	return nil
}

const NO_INSTANCE_NAME = ""
const KEY = "a-key"
const CONTENTS = "hello"
const CONTENTS_HASH = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

func TestCacheBasics(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache := New(cacheDir, 100)

	err := checkItems(testCache.(*diskCache), 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Non-existing item
	rdr, sizeBytes, err := testCache.Get(cache.CAS, NO_INSTANCE_NAME, CONTENTS_HASH)
	if err != nil {
		t.Fatal(err)
	}
	if rdr != nil {
		t.Fatal("expected the item not to exist")
	}

	// Add an item
	err = testCache.Put(cache.CAS, NO_INSTANCE_NAME, CONTENTS_HASH, int64(len(CONTENTS)), strings.NewReader(CONTENTS))
	if err != nil {
		t.Fatal(err)
	}

	// Dig into the internals to make sure that the cache state has been
	// updated correctly
	err = checkItems(testCache.(*diskCache), int64(len(CONTENTS)), 1)
	if err != nil {
		t.Fatal(err)
	}

	// Get the item back
	rdr, sizeBytes, err = testCache.Get(cache.CAS, NO_INSTANCE_NAME, CONTENTS_HASH)
	if err != nil {
		t.Fatal(err)
	}

	err = expectContentEquals(rdr, sizeBytes, []byte(CONTENTS))
	if err != nil {
		t.Fatal(err)
	}
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
		strReader := strings.NewReader(strings.Repeat("a", i))
		err := testCache.Put(cache.AC, NO_INSTANCE_NAME, fmt.Sprintf("aa-%d", i), int64(i), strReader)
		if err != nil {
			t.Fatal(err)
		}

		err = checkItems(testCache.(*diskCache), thisExp.expSize, thisExp.expNum)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCachePutWrongSize(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache := New(cacheDir, 100)

	err := testCache.Put(cache.AC, NO_INSTANCE_NAME, "aa-aa", int64(10), strings.NewReader("hello"))
	if err == nil {
		t.Fatal("Expected error due to size being different")
	}
}

func expectContentEquals(rdr io.ReadCloser, sizeBytes int64, expectedContent []byte) error {
	if rdr == nil {
		return fmt.Errorf("expected the item to exist")
	}
	data, err := ioutil.ReadAll(rdr)
	if err != nil {
		return err
	}
	if bytes.Compare(data, expectedContent) != 0 {
		return fmt.Errorf("expected response '%s', but received '%s'",
			expectedContent, data)
	}
	if int64(len(data)) != sizeBytes {
		return fmt.Errorf("Expected sizeBytes to be '%d' but was '%d'",
			sizeBytes, len(data))
	}

	return nil
}

func putGetCompare(kind cache.EntryKind, hash string, content string, testCache cache.Cache) error {
	return putGetCompareBytes(kind, hash, []byte(content), testCache)
}

func putGetCompareBytes(kind cache.EntryKind, hash string, data []byte, testCache cache.Cache) error {
	err := putBytes(kind, NO_INSTANCE_NAME, hash, data, testCache)
	if err != nil {
		return err
	}

	return getCompareBytes(kind, NO_INSTANCE_NAME, hash, data, testCache)
}

func put(kind cache.EntryKind, instanceName string, hash string, content string, testCache cache.Cache) error {
	return putBytes(kind, instanceName, hash, []byte(content), testCache)
}

func putBytes(kind cache.EntryKind, instanceName string, hash string, data []byte, testCache cache.Cache) error {
	r := bytes.NewReader(data)

	return testCache.Put(kind, instanceName, hash, int64(len(data)), r)
}

func getCompare(kind cache.EntryKind, instanceName string, hash string, content string, testCache cache.Cache) error {
	return getCompareBytes(kind, instanceName, hash, []byte(content), testCache)
}

func getCompareBytes(kind cache.EntryKind, instanceName string, hash string, data []byte, testCache cache.Cache) error {
	rdr, sizeBytes, err := testCache.Get(kind, instanceName, hash)
	if err != nil {
		return err
	}
	// Get the item back
	return expectContentEquals(rdr, sizeBytes, data)
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

	var err error
	err = putGetCompare(cache.CAS, hashStr("hello"), "hello", testCache)
	if err != nil {
		t.Fatal(err)
	}
	err = putGetCompare(cache.CAS, hashStr("hello"), "hello", testCache)
	if err != nil {
		t.Fatal(err)
	}

	err = putGetCompare(cache.AC, hashStr("world"), "world1", testCache)
	if err != nil {
		t.Fatal(err)
	}
	err = putGetCompare(cache.AC, hashStr("world"), "world2", testCache)
	if err != nil {
		t.Fatal(err)
	}

	err = putGetCompare(cache.RAW, hashStr("world"), "world3", testCache)
	if err != nil {
		t.Fatal(err)
	}
	err = putGetCompare(cache.RAW, hashStr("world"), "world4", testCache)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRespectInstanceName(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache := New(cacheDir, 30)

	hash := hashStr("just some common hash")

	var err error
	err = put(cache.AC, NO_INSTANCE_NAME, hash, "content", testCache)
	if err != nil {
		t.Fatal(err)
	}
	err = put(cache.AC, "a", hash, "contentA", testCache)
	if err != nil {
		t.Fatal(err)
	}
	err = put(cache.AC, "b", hash, "contentB", testCache)
	if err != nil {
		t.Fatal(err)
	}

	// expect different content for different instance names

	err = getCompare(cache.AC, NO_INSTANCE_NAME, hash, "content", testCache)
	if err != nil {
		t.Fatal(err)
	}
	err = getCompare(cache.AC, "a", hash, "contentA", testCache)
	if err != nil {
		t.Fatal(err)
	}
	err = getCompare(cache.AC, "b", hash, "contentB", testCache)
	if err != nil {
		t.Fatal(err)
	}

	anotherTestCache := New(cacheDir, 30)

	err = getCompare(cache.AC, NO_INSTANCE_NAME, hash, "content", anotherTestCache)
	if err != nil {
		t.Fatal(err)
	}
	err = getCompare(cache.AC, "a", hash, "contentA", anotherTestCache)
	if err != nil {
		t.Fatal(err)
	}
	err = getCompare(cache.AC, "b", hash, "contentB", anotherTestCache)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCacheExistingFiles(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)

	ensureDirExists(filepath.Join(cacheDir, "cas", "f5"))
	ensureDirExists(filepath.Join(cacheDir, "cas", "fd"))
	ensureDirExists(filepath.Join(cacheDir, "ac", "73"))
	ensureDirExists(filepath.Join(cacheDir, "raw", "73"))

	items := []string{
		"cas/f5/f53b46209596d170f7659a414c9ff9f6b545cf77ffd6e1cbe9bcc57e1afacfbd",
		"cas/fd/fdce205a735f407ae2910426611893d99ed985e3d9a341820283ea0b7d046ee3",
		"ac/73/733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
		"raw/73/733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
	}

	for _, it := range items {
		err := ioutil.WriteFile(filepath.Join(cacheDir, it), []byte(CONTENTS), os.ModePerm)
		if err != nil {
			t.Fatal(err)
		}
		// Wait a bit to account for atime granularity
		time.Sleep(10 * time.Millisecond)
	}

	const expectedSize = 4 * int64(len(CONTENTS))
	testCache := New(cacheDir, expectedSize)

	err := checkItems(testCache.(*diskCache), expectedSize, 4)
	if err != nil {
		t.Fatal(err)
	}

	// Adding a new file should evict items[0] (the oldest)
	err = testCache.Put(cache.CAS, NO_INSTANCE_NAME, CONTENTS_HASH, int64(len(CONTENTS)), strings.NewReader(CONTENTS))
	if err != nil {
		t.Fatal(err)
	}

	err = checkItems(testCache.(*diskCache), expectedSize, 4)
	if err != nil {
		t.Fatal(err)
	}
	found := testCache.Contains(cache.CAS, NO_INSTANCE_NAME, "f53b46209596d170f7659a414c9ff9f6b545cf77ffd6e1cbe9bcc57e1afacfbd")
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

	for k := range []cache.EntryKind{cache.AC, cache.RAW} {
		kind := cache.EntryKind(k)
		err := testCache.Put(kind, NO_INSTANCE_NAME, hashStr("foo"), 10000, strings.NewReader(CONTENTS))
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
}

// Make sure that Cache rejects an upload whose hashsum doesn't match
func TestCacheCorruptedCASBlob(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache := New(cacheDir, 1000)

	err := testCache.Put(cache.CAS, NO_INSTANCE_NAME, hashStr("foo"), int64(len(CONTENTS)),
		strings.NewReader(CONTENTS))
	if err == nil {
		t.Fatal("expected hash mismatch error")
	}

	// We expect the upload to succeed without validation:
	err = testCache.Put(cache.RAW, NO_INSTANCE_NAME, hashStr("foo"), int64(len(CONTENTS)),
		strings.NewReader(CONTENTS))
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigrateFromOldDirectoryStructure(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	acHash, err := testutils.CreateRandomFile(cacheDir+"/ac/", 512)
	if err != nil {
		t.Fatal(err)
	}
	casHash1, err := testutils.CreateRandomFile(cacheDir+"/cas/", 1024)
	if err != nil {
		t.Fatal(err)
	}
	casHash2, err := testutils.CreateRandomFile(cacheDir+"/cas/", 1024)
	if err != nil {
		t.Fatal(err)
	}
	testCache := New(cacheDir, 2560)
	_, numItems := testCache.Stats()
	if numItems != 3 {
		t.Fatalf("Expected test cache size 3 but was %d", numItems)
	}
	if !testCache.Contains(cache.AC, NO_INSTANCE_NAME, acHash) {
		t.Fatalf("Expected cache to contain AC entry '%s'", acHash)
	}
	if !testCache.Contains(cache.CAS, NO_INSTANCE_NAME, casHash1) {
		t.Fatalf("Expected cache to contain CAS entry '%s'", casHash1)
	}
	if !testCache.Contains(cache.CAS, NO_INSTANCE_NAME, casHash2) {
		t.Fatalf("Expected cache to contain CAS entry '%s'", casHash2)
	}
}

func TestLoadExistingEntries(t *testing.T) {
	// Test that loading existing items works
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	numBlobs := int64(3)
	blobSize := int64(1024)

	acHash, err := testutils.CreateCacheFile(cacheDir+"/ac/", blobSize)
	if err != nil {
		t.Fatal(err)
	}
	casHash, err := testutils.CreateCacheFile(cacheDir+"/cas/", blobSize)
	if err != nil {
		t.Fatal(err)
	}
	rawHash, err := testutils.CreateCacheFile(cacheDir+"/raw/", blobSize)
	if err != nil {
		t.Fatal(err)
	}

	testCache := New(cacheDir, blobSize*numBlobs)
	_, numItems := testCache.Stats()
	if int64(numItems) != numBlobs {
		t.Fatalf("Expected test cache size %d but was %d",
			numBlobs, numItems)
	}
	if !testCache.Contains(cache.AC, NO_INSTANCE_NAME, acHash) {
		t.Fatalf("Expected cache to contain AC entry '%s'", acHash)
	}
	if !testCache.Contains(cache.CAS, NO_INSTANCE_NAME, casHash) {
		t.Fatalf("Expected cache to contain CAS entry '%s'", casHash)
	}
	if !testCache.Contains(cache.RAW, NO_INSTANCE_NAME, rawHash) {
		t.Fatalf("Expected cache to contain RAW entry '%s'", rawHash)
	}
}

func TestDistinctKeyspaces(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	blobSize := 1024
	cacheSize := int64(blobSize * 3)

	testCache := New(cacheDir, cacheSize)

	blob, casHash := testutils.RandomDataAndHash(1024)

	// Add the same blob with the same key, to each of the three
	// keyspaces, and verify that we have exactly three items in
	// the cache.

	var err error

	err = putGetCompareBytes(cache.CAS, casHash, blob, testCache)
	if err != nil {
		t.Fatal(err)
	}

	err = putGetCompareBytes(cache.AC, casHash, blob, testCache)
	if err != nil {
		t.Fatal(err)
	}

	err = putGetCompareBytes(cache.RAW, casHash, blob, testCache)
	if err != nil {
		t.Fatal(err)
	}

	_, numItems := testCache.Stats()
	if numItems != 3 {
		t.Fatalf("Expected test cache size 3 but was %d",
			numItems)
	}
}
