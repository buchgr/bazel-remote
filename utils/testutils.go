package testutils

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"os"
	"testing"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
)

// TempDir creates a temporary directory and returns its name. If an error
// occurs, then it panics.
func TempDir(t *testing.T) string {
	dir, err := os.MkdirTemp("", "bazel-remote")
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

func RandomDataAndDigest(size int64) ([]byte, pb.Digest) {
	data, hash := RandomDataAndHash(size)
	return data, pb.Digest{
		Hash:      hash,
		SizeBytes: size,
	}
}

// NewSilentLogger returns a cheap logger that doesn't print anything, useful
// for tests.
func NewSilentLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}
