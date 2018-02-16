package cache

import (
	"os"
	"testing"
)

func TestEnsureSpaceBasics(t *testing.T) {
	cacheDir := createTmpCacheDirs(t)
	defer os.RemoveAll(cacheDir)

	e := NewEnsureSpacer(0.9, 0.5)
	c := NewCache(cacheDir, 100)

	enoughSpace := e.EnsureSpace(c, 10)
	if !enoughSpace {
		t.Error("Expected the cache to have enough space.")
	}

	enoughSpace = e.EnsureSpace(c, 100)
	if !enoughSpace {
		t.Error("Expected the cache to have enough space.")
	}

	enoughSpace = e.EnsureSpace(c, 101)
	if enoughSpace {
		t.Error("Expected the cache to not have enough space.")
	}
}

func TestEnsureSpacePurging(t *testing.T) {
	cacheDir := createTmpCacheDirs(t)
	defer os.RemoveAll(cacheDir)

	c := NewCache(cacheDir, 100)
	for i := 0; i < 9; i++ {
		filename, err := createRandomFile(cacheDir, 10)
		if err != nil {
			t.Fatal(err)
		}
		c.AddFile(filename, 10)
	}

	if c.CurrSize() != 90 {
		t.Error(
			"For cache directory size",
			"expected", 90,
			"got", c.CurrSize(),
		)
	}

	e := NewEnsureSpacer(0.9, 0.5)
	enoughSpace := e.EnsureSpace(c, 10)
	if !enoughSpace {
		t.Error("Expected the cache to have enough space.")
	}

	fd, err := os.Open(cacheDir)
	if err != nil {
		t.Error(err)
	}
	dirEntries, err := fd.Readdir(-1)
	if err != nil {
		t.Error(err)
	}

	files := []os.FileInfo{}
	for _, entry := range dirEntries {
		if !entry.IsDir() {
			files = append(files, entry)
		}
	}

	actualNumFiles := len(files)
	if actualNumFiles != 5 {
		t.Error("For the number of files in the cache directory",
			"expected", 5,
			"got", actualNumFiles,
		)
	}
	actualCacheSize := c.CurrSize()
	if actualCacheSize != 50 {
		t.Error("For the current cache size",
			"expected", 50,
			"got", actualCacheSize)
	}
}
