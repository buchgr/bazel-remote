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
	// Like Action Cache, but without ActionResult validation.
	// Not exposed externally, only used for HTTP when running with
	// the --disable_http_ac_validation commandline flag.
	RAW
)

func (e EntryKind) String() string {
	if e == AC {
		return "ac"
	}
	if e == CAS {
		return "cas"
	}
	return "raw"
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

// Cache backends implement this interface, and are optionally used
// by DiskCache. CacheProxy implementations are expected to be safe
// for concurrent use.
type CacheProxy interface {
	// Put should make a reasonable effort to proxy this data to the backend.
	// This is allowed to fail silently (eg when under heavy load).
	Put(kind EntryKind, hash string, size int64, rdr io.Reader)

	// Get should return the cache item identified by `hash`, or an error
	// if something went wrong. If the item was not found, the io.ReadCloser
	// will be nil.
	Get(kind EntryKind, hash string) (io.ReadCloser, int64, error)

	// Contains returns whether or not the cache item exists on the
	// remote end, and the size if it exists (and -1 if the size is
	// unknown).
	Contains(kind EntryKind, hash string) (bool, int64)
}
