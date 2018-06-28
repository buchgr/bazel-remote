package cache

import (
	"io"
	"net/http"
)

// ErrTooBig is returned by BlobStore::Put when when the item size is bigger than the
// blob store size limit.
type ErrTooBig struct{}

func (e *ErrTooBig) Error() string {
	return "item bigger than the blob store size limit"
}

// BlobStore is the interface for a generic blob storage blobStore. Implementers should handle
// locking internally.
type BlobStore interface {
	// Put stores a stream of `size` bytes from `r` into the store. If `expectedSha256` is
	// not the empty string, and the contents don't match it, an error is returned.
	Put(key string, size int64, expectedSha256 string, r io.Reader) error
	// Get writes the content of the blob stored under `key` to `w`. If the blob is
	// not found, it returns ok = false.
	Get(key string, w http.ResponseWriter) (ok bool, err error)
	Contains(key string) (ok bool, err error)

	// Stats
	MaxSize() int64
	CurrentSize() int64
	NumItems() int
}
