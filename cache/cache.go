package cache

import (
	"io"
)

// EntryKind describes the kind of cache entry
type EntryKind int

const (
	// AC stands for Action Cache
	AC EntryKind = iota
	// CAS stands for Content Addressable Storage
	CAS
)

func (e EntryKind) String() string {
	if e == AC {
		return "ac"
	}
	return "cas"
}

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

	// Put stores a stream of `size` bytes from `r` into the cache. If `hash` is
	// not the empty string, and the contents don't match it, a non-nil error is
	// returned.
	Put(kind EntryKind, hash string, size int64, r io.Reader) error

	// Get returns an io.ReadCloser with the content of the cache item stored under `hash`
	// and the number of bytes that can be read from it. If the item is not found, `r` is
	// nil. If some error occurred when processing the request, then it is returned.
	Get(kind EntryKind, hash string) (r io.ReadCloser, sizeBytes int64, err error)

	// Contains returns true if the `hash` key exists in the cache.
	Contains(kind EntryKind, hash string) (ok bool)

	// MaxSize returns the maximum cache size in bytes.
	MaxSize() int64

	// CurrentSize returns the current cache size in bytes.
	CurrentSize() int64

	// NumItems returns the number of items stored in the cache.
	NumItems() int
}
