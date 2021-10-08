package zstdimpl

import (
	"errors"
	"github.com/buchgr/bazel-remote/utils/zstdpool"
	"github.com/klauspost/compress/zstd"
	syncpool "github.com/mostynb/zstdpool-syncpool"
	"io"
)

var zstdFastestLevel = zstd.WithEncoderLevel(zstd.SpeedFastest)

var encoder, _ = zstd.NewWriter(nil, zstdFastestLevel) // TODO: raise WithEncoderConcurrency ?
var decoder, _ = zstd.NewReader(nil)                   // TODO: raise WithDecoderConcurrency ?

var encoderPool = zstdpool.GetEncoderPool()
var decoderPool = zstdpool.GetDecoderPool()

var errDecoderPoolFail = errors.New("failed to get decoder from pool")
var errEncoderPoolFail = errors.New("failed to get encoder from pool")

type goZstd struct{}

func init() {
	register("go", goZstd{})
}

// zstdEncoderWrapper is a zstdEncoder that embeds an encoder,
// and on Close returns it to the pool
// TODO(mostynb): Why encoderpool does not provide such functionality. decoderpool has it (IOReadCloser).
type zstdEncoderWrapper struct {
	*syncpool.EncoderWrapper
}

func (w *zstdEncoderWrapper) Close() error {
	err := w.EncoderWrapper.Close()
	encoderPool.Put(w.EncoderWrapper)
	return err
}

func (goZstd) GetDecoder(in io.ReadCloser) (io.ReadCloser, error) {
	dec, ok := decoderPool.Get().(*syncpool.DecoderWrapper)
	if !ok {
		return nil, errDecoderPoolFail
	}
	err := dec.Reset(in)
	if err != nil {
		decoderPool.Put(dec)
		return nil, err
	}
	return dec.IOReadCloser(), nil
}

func (goZstd) GetEncoder(out io.WriteCloser) (zstdEncoder, error) {
	enc, ok := encoderPool.Get().(*syncpool.EncoderWrapper)
	if !ok {
		return nil, errEncoderPoolFail
	}
	enc.Reset(out)
	return &zstdEncoderWrapper{enc}, nil
}

func (goZstd) DecodeAll(in []byte) ([]byte, error) {
	return decoder.DecodeAll(in, nil)
}

func (goZstd) EncodeAll(in []byte) []byte {
	return encoder.EncodeAll(in, nil)
}
