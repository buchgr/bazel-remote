//+build cgo

package zstdimpl

import (
	"github.com/valyala/gozstd"
	"io"
	"io/ioutil"
)

const compressionLevel = 1

type cgoZstd struct{}

func init() {
	register("cgo", cgoZstd{})
}

func (cgoZstd) GetDecoder(in io.ReadCloser) (io.ReadCloser, error) {
	return ioutil.NopCloser(gozstd.NewReader(in)), nil
}

func (cgoZstd) GetEncoder(out io.WriteCloser) (zstdEncoder, error) {
	return gozstd.NewWriterLevel(out, compressionLevel), nil
}

func (cgoZstd) DecodeAll(in []byte) ([]byte, error) {
	return gozstd.Decompress(nil, in)
}

func (cgoZstd) EncodeAll(in []byte) []byte {
	return gozstd.CompressLevel(nil, in, compressionLevel)
}
