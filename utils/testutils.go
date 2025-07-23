package testutils

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"testing"

	"github.com/buchgr/bazel-remote/v2/cache"
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

// AssertEquals fails the test if expected and actual values are not equal.
// It works with any comparable type.
func AssertEquals[T comparable](t *testing.T, expected T, actual T) {
	t.Helper()
	if expected != actual {
		t.Fatalf("Expected %v, but got %v.", expected, actual)
	}
}

// AssertSuccess asserts that the provided result represents a successful outcome.
//
// The success criteria are:
// - nil value (e.g., no error)
// - true boolean
//
// The failure criteria are:
// - non-nil error
// - false boolean
func AssertSuccess(t *testing.T, result interface{}) {
	t.Helper()
	switch v := result.(type) {
	case nil:
		return // Success as expected
	case error:
		if v != nil {
			t.Fatalf("Expected success, but got error: %v", v)
		}
	case bool:
		if !v {
			t.Fatalf("Expected success, but got false value")
		}
	default:
		t.Fatalf("Unsupported type: %T", v)
	}
}

// AssertFailureWithCode asserts that the provided error is a *cache.Error with the expected code.
func AssertFailureWithCode(t *testing.T, err error, expectedCode int) {
	t.Helper()
	if err == nil {
		t.Fatalf("Expected failure, but got no error.")
	}
	var cerr *cache.Error
	if errors.As(err, &cerr) {
		if cerr.Code != expectedCode {
			t.Fatalf("Error code mismatch: expected %d, got %d", expectedCode, cerr.Code)
		}
	} else {
		t.Fatalf("Expected error of type *Error, got %T", err)
	}
}
