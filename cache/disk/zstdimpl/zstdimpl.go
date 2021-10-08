package zstdimpl

import (
	"io"
	"log"
)

var registry map[string]ZstdImpl

func register(implName string, impl ZstdImpl) {
	if registry == nil {
		registry = make(map[string]ZstdImpl)
	}
	registry[implName] = impl
}

func Get(implName string) ZstdImpl {
	impl, ok := registry[implName]
	if !ok {
		log.Fatalf("Unrecognized ZSTD implementation: %s, supported: %s", implName, registry)
	}
	return impl
}

type ZstdImpl interface {
	GetDecoder(in io.ReadCloser) (io.ReadCloser, error)
	GetEncoder(out io.WriteCloser) (zstdEncoder, error)
	DecodeAll(in []byte) ([]byte, error)
	EncodeAll(in []byte) []byte
}

type zstdEncoder interface {
	io.WriteCloser
	io.ReaderFrom
}
