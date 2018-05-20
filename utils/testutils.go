package testutils

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func TempDir(t *testing.T) string {
	dir, err := ioutil.TempDir("", "bazel-remote")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func CreateRandomFile(dir string, size int64) (string, error) {
	data, filename := RandomDataAndHash(size)
	filepath := dir + "/" + filename

	return filename, ioutil.WriteFile(filepath, data, 0744)
}

func RandomDataAndHash(size int64) ([]byte, string) {
	data := make([]byte, size)
	rand.Read(data)
	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])
	return data, hashStr
}

func CreateTmpCacheDirs(t *testing.T) string {
	path, err := ioutil.TempDir("", "bazel-remote-test")
	if err != nil {
		t.Error("Couldn't create tmp dir", err)
	}
	EnsureDirExists(filepath.Join(path, "ac"))
	EnsureDirExists(filepath.Join(path, "cas"))

	return path
}

// NewSilentLogger returns a cheap logger that doesn't print anything, useful
// for tests.
func NewSilentLogger() *log.Logger {
	return log.New(ioutil.Discard, "", 0)
}

func EnsureDirExists(path string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		err = os.MkdirAll(path, os.FileMode(0744))
		if err != nil {
			log.Fatal(err)
		}
	}
}
