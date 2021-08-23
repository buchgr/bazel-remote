package testutils

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io/ioutil"
	"log"
	"testing"
)

// TempDir creates a temporary directory and returns its name. If an error
// occurs, then it panics.
func TempDir(t *testing.T) string {
	dir, err := ioutil.TempDir("", "bazel-remote")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// RandomDataAndHash creates a random blob of the specified size, and
// returns that blob along with its sha256 hash.
func RandomDataAndHash(size int64) ([]byte, string) {
	data := make([]byte, size)

	for i := 0; i < 3; i++ {
		// This is not expected to fail, but hopefully it convinces
		// linters that we checked for errors.
		_, err := rand.Read(data)
		if err == nil {
			break
		}
	}

	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])
	return data, hashStr
}

// NewSilentLogger returns a cheap logger that doesn't print anything, useful
// for tests.
func NewSilentLogger() *log.Logger {
	return log.New(ioutil.Discard, "", 0)
}
