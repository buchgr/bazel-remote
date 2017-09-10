package cache

import (
	"testing"
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
