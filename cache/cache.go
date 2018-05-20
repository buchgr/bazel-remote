package cache

import (
	"io"
)

// Logger is designed to be satisfied by log.Logger.
type Logger interface {
	Printf(format string, v ...interface{})
}

// Error is used by Cache implementations to return a structured error.
type Error struct {
	// Corresponds to a http.Status* code
	Code int
	// A human-readable string describing the error
	Text string
}

func (e *Error) Error() string {
	return e.Text
}

// Cache is the interface for a generic blob storage backend. Implementers should handle
// locking internally.
type Cache interface {

	// Put stores a stream of `size` bytes from `r` into the cache. If `expectedSha256` is
	// not the empty string, and the contents don't match it, an error is returned
	Put(key string, size int64, expectedSha256 string, r io.Reader) error

	// Get writes the content of the cache item stored under `key` to `w`. If the item is
	// not found, it returns ok = false.
	Get(key string, actionCache bool) (data io.ReadCloser, sizeBytes int64, err error)

	// Contains returns true if the `key` exists.
	Contains(key string, actionCache bool) (ok bool)

	// MaxSize returns the maximum cache size in bytes.
	MaxSize() int64

	// CurrentSize returns the current cache size in bytes.
	CurrentSize() int64

	// NumItems returns the number of items stored in the cache.
	NumItems() int
}
