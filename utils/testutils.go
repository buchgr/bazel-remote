package testutils

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io/ioutil"
	"log"
	"os"
	"testing"
)

func TempDir(t *testing.T) string {
	dir, err := ioutil.TempDir("", "bazel-remote")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func CreateCacheFile(dir string, size int64) (string, error) {
	data, hash := RandomDataAndHash(size)
	subdir := dir + "/" + hash[0:2]
	os.MkdirAll(subdir, os.ModePerm)
	filepath := subdir + "/" + hash

	return hash, ioutil.WriteFile(filepath, data, os.ModePerm)
}

func RandomDataAndHash(size int64) ([]byte, string) {
	data := make([]byte, size)
	rand.Read(data)
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])
	return data, hashStr
}

// NewSilentLogger returns a cheap logger that doesn't print anything, useful
// for tests.
func NewSilentLogger() *log.Logger {
	return log.New(ioutil.Discard, "", 0)
}
