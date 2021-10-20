//+build cgo

package zstdimpl

import (
	"github.com/valyala/gozstd"
	"io"
	"runtime"
	"sync"
)

const compressionLevel = 1

type cgoZstd struct{}

func init() {
	register("cgo", cgoZstd{})
}

func (cgoZstd) GetDecoder(in io.ReadCloser) (io.ReadCloser, error) {
	r := readerPool.Get().(*readerWrapper)
	r.Reset(in, nil)
	return &putReaderToPoolOnClose{r}, nil
}

func (cgoZstd) GetEncoder(out io.WriteCloser) (zstdEncoder, error) {
	w := writerPool.Get().(*writerWrapper)
	w.Reset(out, nil, compressionLevel)
	return &putWriterToPoolOnClose{w}, nil
}

func (cgoZstd) DecodeAll(in []byte) ([]byte, error) {
	return gozstd.Decompress(nil, in)
}

func (cgoZstd) EncodeAll(in []byte) []byte {
	return gozstd.CompressLevel(nil, in, compressionLevel)
}

// -- Reader pool
var readerPool = &sync.Pool{
	New: newReader,
}

type readerWrapper struct {
	*gozstd.Reader
}

func newReader() interface{} {
	r := &readerWrapper{gozstd.NewReader(nil)}
	runtime.SetFinalizer(r, releaseReader)
	return r
}

func releaseReader(r *readerWrapper) {
	r.Release()
}

type putReaderToPoolOnClose struct {
	*readerWrapper
}

func (r *putReaderToPoolOnClose) Close() error {
	readerPool.Put(r.readerWrapper)
	return nil
}

// -- Writer pool
var writerPool = &sync.Pool{
	New: newWriter,
}

type writerWrapper struct {
	*gozstd.Writer
}

func newWriter() interface{} {
	w := &writerWrapper{gozstd.NewWriterLevel(nil, compressionLevel)}
	runtime.SetFinalizer(w, releaseWriter)
	return w
}

func releaseWriter(w *writerWrapper) {
	w.Release()
}

type putWriterToPoolOnClose struct {
	*writerWrapper
}

func (w putWriterToPoolOnClose) Close() error {
	err := w.Close()
	writerPool.Put(w.writerWrapper)
	return err
}
