package cache

import (
	"testing"
	"os"
	"path/filepath"
)

func TestCache(t *testing.T) {
	cacheDir := "/tmp/foobar"
	cache := NewCache(cacheDir, 100)
	actualSize := cache.CurrSize()
	if actualSize != 0 {
		t.Error(
			"For initial cache size",
			"expected", 0,
			"got", actualSize,
		)
	}

	actualDir := cache.Dir()
	if cacheDir != actualDir {
		t.Error(
			"For cache directory",
			"expected", cacheDir,
			"got", actualDir,
		)
	}

	actualNumFiles := cache.NumFiles()
	if actualNumFiles != 0 {
		t.Error(
			"For number of files",
			"expected", 0,
			"got", actualNumFiles,
		)
	}

	cache.RemoveFile("does-not-exist-hash")
	actualSize = cache.CurrSize()
	if actualSize != 0 {
		t.Error(
			"For cache size after removing none existing file",
			"expected", 0,
			"got", actualSize,
		)
	}

	actualContainsFile := cache.ContainsFile("does-not-exist-hash")
	if actualContainsFile {
		t.Error(
			"For checking existance of not existing file",
			"expected", false,
			"got", true,
		)
	}

	cache.AddFile("does-exist-hash", 50)
	actualSize = cache.CurrSize()
	if actualSize != 50 {
		t.Error(
			"For cache size after adding a new file",
			"expected", 50,
			"got", actualSize,
		)
	}

	if !cache.ContainsFile("does-exist-hash") {
		t.Error(
			"For checking if the cache contains an existing file",
			"expected", true,
			"got", false,
		)
	}

	actualNumFiles = cache.NumFiles()
	if actualNumFiles != 1 {
		t.Error(
			"For number of files",
			"expected", 1,
			"got", actualNumFiles,
		)
	}

	cache.RemoveFile("does-exist-hash")
	actualSize = cache.CurrSize()
	if actualSize != 0 {
		t.Error(
			"For cache size after removing a file",
			"expected", 0,
			"got", actualSize,
		)
	}
}

func TestLoadExistingFiles(t *testing.T) {
	cacheDir := createTmpCacheDirs(t)
	defer os.RemoveAll(cacheDir)

	cache := NewCache(cacheDir, 100)

	// Create a file in the cache directory
	_, err := createRandomFile(filepath.Join(cacheDir, "cas"), 10)
	if err != nil {
		t.Fatal(err)
	}

	// The cache should still be empty
	actualNumFiles := cache.NumFiles()
	if actualNumFiles != 0 {
		t.Error(
			"For number of files",
			"expected", 0,
			"got", actualNumFiles,
		)
	}

	// Now re-index disk contents
	cache.LoadExistingFiles()

	// The cache should have 1 item now
	actualNumFiles = cache.NumFiles()
	if actualNumFiles != 1 {
		t.Error(
			"For number of files",
			"expected", 1,
			"got", actualNumFiles,
		)
	}
}
