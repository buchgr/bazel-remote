package zstdpool

import (
	"sync"

	"github.com/klauspost/compress/zstd"
	syncpool "github.com/mostynb/zstdpool-syncpool"
)

var onceEncPool sync.Once
var encoderPool *sync.Pool

var onceDecPool sync.Once
var decoderPool *sync.Pool

func GetEncoderPool() *sync.Pool {
	onceEncPool.Do(func() {
		encoderPool = syncpool.NewEncoderPool(
			zstd.WithEncoderConcurrency(1),
			zstd.WithEncoderLevel(zstd.SpeedFastest),
			zstd.WithLowerEncoderMem(true))
	})

	return encoderPool
}

func GetDecoderPool() *sync.Pool {
	onceDecPool.Do(func() {
		decoderPool = syncpool.NewDecoderPool(
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderLowmem(true))
	})

	return decoderPool
}
