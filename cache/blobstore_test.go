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
)

// Test cases are split into a private (i.e. lowercase) and a public (i.e. uppercase)
// function, to make it easier to test multiple implementations of a BlobStore.
// Please add implementation-specific tests in their respective files, e.g.
// `blobstore_fs_test.go`.

const KEY = "a-key"
const CONTENTS = "hello"
const CONTENTS_HASH = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

func tempDir(t *testing.T) string {
	dir, err := ioutil.TempDir("", "bazel-remote")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func checkItems(t *testing.T, cache BlobStore, expSize int64, expNum int) {
	// This function liberally peeks into the internals of the blob store, and should be
	// updated with care when adding new implementations.

	switch storeInternals := cache.(type) {
	case *fsBlobStore:
		if storeInternals.lru.Len() != expNum {
			t.Fatalf("expected %d files in the BlobStore, found %d", expNum, storeInternals.lru.Len())
		}
		if storeInternals.lru.CurrentSize() != expSize {
			t.Fatalf("expected %d bytes in the BlobStore, found %d", expSize, storeInternals.lru.CurrentSize())
		}

		// Dig into the internals of the blobStore to make sure that all items are committed.
		for _, it := range storeInternals.lru.(*sizedLRU).cache {
			if it.Value.(*entry).value.(*lruItem).committed != true {
				t.Fatalf("expected committed = true")
			}
		}

		numFiles := 0
		filepath.Walk(storeInternals.dir, func(name string, info os.FileInfo, err error) error {
			if !info.IsDir() {
				numFiles += 1
			}
			return nil
		})

		if numFiles != expNum {
			t.Fatalf("expected %d files on disk, found %d", expNum, numFiles)
		}
	}
}

func expectContentEquals(t *testing.T, c BlobStore, key string, content []byte) {
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

//

func testCacheBasics(t *testing.T, store BlobStore) {
	checkItems(t, store, 0, 0)

	// Non-existing item
	rr := httptest.NewRecorder()
	found, err := store.Get(KEY, rr)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected the item not to exist")
	}

	// Add an item
	err = store.Put(KEY, int64(len(CONTENTS)), CONTENTS_HASH, strings.NewReader(CONTENTS))
	if err != nil {
		t.Fatal(err)
	}

	// Dig into the internals to make sure that the blobStore state has been
	// updated correctly
	checkItems(t, store, int64(len(CONTENTS)), 1)

	// Get the item back
	expectContentEquals(t, store, KEY, []byte(CONTENTS))
}

func TestCacheBasics(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	cache := NewFsBlobStore(cacheDir, 100)
	testCacheBasics(t, cache)
}

//

func testCacheEviction(t *testing.T, store BlobStore) {
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
		err := store.Put(fmt.Sprintf("key-%d", i), int64(i), "", strings.NewReader("hello"))
		if err != nil {
			t.Fatal(err)
		}

		checkItems(t, store, thisExp.expSize, thisExp.expNum)
	}
}

func TestCacheEviction(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	store := NewFsBlobStore(cacheDir, 10)
	testCacheEviction(t, store)
}

//

// Make sure that we can overwrite items if we upload the same key again.
func testOverwrite(t *testing.T, store BlobStore) {
	oldContent := "Hello"
	newContent := "World"

	err := store.Put(KEY, 1, "", strings.NewReader(oldContent));
	if err != nil {
		t.Fatal(err)
	}

	// Get the item back
	expectContentEquals(t, store, KEY, []byte(oldContent))

	// Overwrite
	err = store.Put(KEY, 1, "", strings.NewReader(newContent));
	if err != nil {
		t.Fatal(err)
	}

	// Get the item back again
	expectContentEquals(t, store, KEY, []byte(newContent))
}

func TestOverwrite(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	store := NewFsBlobStore(cacheDir, 10)
	testOverwrite(t, store)
}

//

// Make sure that BlobStore returns ErrTooBig when trying to upload an item that's bigger
// than the maximum size.
func testCacheTooBig(t *testing.T, store BlobStore) {
	err := store.Put("a-key", 10000, "", strings.NewReader(CONTENTS))
	if err == nil {
		t.Fatal("expected ErrTooBig")
	}
	switch err.(type) {
	case *ErrTooBig:
	default:
		t.Fatal("expected ErrTooBig")
	}
}

func TestCacheTooBig(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	store := NewFsBlobStore(cacheDir, 100)
	testCacheTooBig(t, store)
}

//

// Make sure that BlobStore rejects an upload whose hashsum doesn't match
func testCacheCorruptedFile(t *testing.T, store BlobStore) {
	err := store.Put(KEY, int64(len(CONTENTS)), strings.Repeat("x", 64), strings.NewReader(CONTENTS))
	if err == nil {
		t.Fatal("expected hash mismatch error")
	}
}

func TestCacheCorruptedFile(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	store := NewFsBlobStore(cacheDir, 1000)
	testCacheCorruptedFile(t, store)
}
