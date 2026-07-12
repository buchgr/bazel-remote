package zstdimpl

import (
	"fmt"
	"io"
	"slices"
)

var registry map[string]ZstdImpl

func register(implName string, impl ZstdImpl) {
	if registry == nil {
		registry = make(map[string]ZstdImpl)
	}
	registry[implName] = impl
}

func Get(implName string) (ZstdImpl, error) {
	impl, ok := registry[implName]
	if !ok {
		return nil, fmt.Errorf("unrecognized ZSTD implementation: %s, supported: %s", implName, GetImplementations())
	}
	return impl, nil
}

func GetImplementations() []string {
	result := make([]string, 0, len(registry))

	for name := range registry {
		result = append(result, name)
	}

	slices.Sort(result)

	return result
}

type ZstdImpl interface {
	GetDecoder(in io.ReadCloser) (io.ReadCloser, error)
	GetEncoder(out io.WriteCloser) (zstdEncoder, error)
	DecodeAll(in []byte) ([]byte, error)

	// EncodeAll compresses in and appends the result to dst, returning the
	// updated slice (like the underlying zstd EncodeAll). Passing a dst with
	// enough spare capacity lets callers reuse a single output buffer across
	// many chunks instead of allocating a fresh one per call. Pass a nil dst
	// to let the implementation allocate.
	EncodeAll(dst, in []byte) []byte
}

type zstdEncoder interface {
	io.WriteCloser
	io.ReaderFrom
}
