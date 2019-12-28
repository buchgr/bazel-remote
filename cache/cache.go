package cache

import (
	"io"
	"io/ioutil"

	pb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/golang/protobuf/proto"
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

// Cache is the interface for a generic blob storage backend. Implementers should handle
// locking internally.
type Cache interface {

	// Put stores a stream of `size` bytes from `rdr` into the cache. If `hash` is
	// not the empty string, and the contents don't match it, a non-nil error is
	// returned.
	Put(kind EntryKind, instanceName string, hash string, size int64, rdr io.Reader) error

	// Get returns an io.ReadCloser with the content of the cache item stored under `hash`
	// and the number of bytes that can be read from it. If the item is not found, `rdr` is
	// nil. If some error occurred when processing the request, then it is returned.
	Get(kind EntryKind, instanceName string, hash string) (rdr io.ReadCloser, sizeBytes int64, err error)

	// Contains returns true if the `hash` key exists in the cache.
	Contains(kind EntryKind, instanceName string, hash string) (ok bool)

	// MaxSize returns the maximum cache size in bytes.
	MaxSize() int64

	// Return the current size of the cache in bytes, and the number of
	// items stored in the cache.
	Stats() (int64, int)
}

// If `hash` refers to a valid ActionResult with all the dependencies
// available in the CAS, return it and its serialized value.
// If not, return nil values.
// If something unexpected went wrong, return an error.
func GetValidatedActionResult(c Cache, instanceName string, hash string) (*pb.ActionResult, []byte, error) {
	rdr, sizeBytes, err := c.Get(AC, instanceName, hash)
	if err != nil {
		return nil, nil, err
	}

	if rdr == nil || sizeBytes <= 0 {
		return nil, nil, nil // aka "not found"
	}

	data, err := ioutil.ReadAll(rdr)
	if err != nil {
		return nil, nil, err
	}

	result := &pb.ActionResult{}
	err = proto.Unmarshal(data, result)
	if err != nil {
		return nil, nil, err
	}

	for _, f := range result.OutputFiles {
		if len(f.Contents) == 0 && f.Digest.SizeBytes > 0 {
			if !c.Contains(CAS, instanceName, f.Digest.Hash) {
				return nil, nil, nil // aka "not found"
			}
		}
	}

	for _, d := range result.OutputDirectories {
		if !c.Contains(CAS, instanceName, d.TreeDigest.Hash) {
			return nil, nil, nil // aka "not found"
		}
	}

	if result.StdoutDigest != nil && result.StdoutDigest.SizeBytes > 0 {
		if !c.Contains(CAS, instanceName, result.StdoutDigest.Hash) {
			return nil, nil, nil // aka "not found"
		}
	}

	if result.StderrDigest != nil && result.StderrDigest.SizeBytes > 0 {
		if !c.Contains(CAS, instanceName, result.StderrDigest.Hash) {
			return nil, nil, nil // aka "not found"
		}
	}

	return result, data, nil
}
