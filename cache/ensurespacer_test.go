package cache

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io/ioutil"
	"os"
	"testing"
)

func TestEnsureSpaceBasics(t *testing.T) {
	cacheDir := createTmpDir(t)
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
	cacheDir := createTmpDir(t)
	defer os.RemoveAll(cacheDir)

	c := NewCache(cacheDir, 100)
	for i := 0; i < 9; i++ {
		data := make([]byte, 10)
		rand.Read(data)
		filename := hex.EncodeToString(sha256.New().Sum(data))
		filepath := cacheDir + "/" + filename

		ioutil.WriteFile(filepath, data, 0744)
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
	files, err := fd.Readdir(-1)
	if err != nil {
		t.Error(err)
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

func createTmpDir(t *testing.T) string {
	path, err := ioutil.TempDir("", "ensurespacer")
	if err != nil {
		t.Error("Couldn't create tmp dir", err)
	}
	return path
}
