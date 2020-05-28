package testutils

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io/ioutil"
	"log"
	"os"
	"testing"

	"github.com/buchgr/bazel-remote/utils/metrics"
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

// CreateCacheFile creates a random data file of the given size in the
// provided directory (corresponding to one of bazel-remote's keyspaces)
// and returns its sha256 hash and any error that occurred.
func CreateCacheFile(dir string, size int64) (string, error) {
	data, hash := RandomDataAndHash(size)
	subdir := dir + "/" + hash[0:2]
	os.MkdirAll(subdir, os.ModePerm)
	filepath := subdir + "/" + hash

	return hash, ioutil.WriteFile(filepath, data, os.ModePerm)
}

// RandomDataAndHash creates a random blob of the specified size, and
// returns that blob along with its sha256 hash.
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

type metricsStub struct{}

func NewMetricsStub() *metricsStub {
	return new(metricsStub)
}

func (m metricsStub) IncomingRequestCompleted(kind metrics.Kind, method metrics.Method, status metrics.Status, headers map[string][]string, protocol metrics.Protocol) {
	// Do nothing
}
