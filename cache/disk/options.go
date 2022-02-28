package disk

import (
	"fmt"
	"log"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/disk/casblob"

	"github.com/prometheus/client_golang/prometheus"
)

type Option func(*CacheConfig) error

type CacheConfig struct {
	diskCache *diskCache        // Assumed to be non-nil.
	metrics   *metricsDecorator // May be nil.
}

func WithStorageMode(mode string) Option {
	return func(c *CacheConfig) error {
		if mode == "zstd" {
			c.diskCache.storageMode = casblob.Zstandard
			return nil
		} else if mode == "uncompressed" {
			c.diskCache.storageMode = casblob.Identity
			return nil
		} else {
			return fmt.Errorf("Unsupported storage mode: " + mode)
		}
	}
}

func WithMaxBlobSize(size int64) Option {
	return func(c *CacheConfig) error {
		if size <= 0 {
			return fmt.Errorf("Invalid MaxBlobSize: %d", size)
		}

		c.diskCache.maxBlobSize = size
		return nil
	}
}

func WithProxyBackend(proxy cache.Proxy) Option {
	return func(c *CacheConfig) error {
		if c.diskCache.proxy != nil && proxy != nil {
			return fmt.Errorf("Proxy backends may be set only once")
		}

		if proxy != nil {
			c.diskCache.proxy = proxy
			c.diskCache.spawnContainsQueueWorkers()
		}

		return nil
	}
}

func WithProxyMaxBlobSize(maxProxyBlobSize int64) Option {
	return func(c *CacheConfig) error {
		if maxProxyBlobSize <= 0 {
			return fmt.Errorf("Invalid MaxProxyBlobSize: %d", maxProxyBlobSize)
		}

		c.diskCache.maxProxyBlobSize = maxProxyBlobSize
		return nil
	}
}

func WithAccessLogger(logger *log.Logger) Option {
	return func(c *CacheConfig) error {
		c.diskCache.accessLogger = logger
		return nil
	}
}

func WithEndpointMetrics() Option {
	return func(c *CacheConfig) error {
		if c.metrics != nil {
			return fmt.Errorf("WithEndpointMetrics specified multiple times")
		}

		c.metrics = &metricsDecorator{
			counter: prometheus.NewCounterVec(prometheus.CounterOpts{
				Name: "bazel_remote_incoming_requests_total",
				Help: "The number of incoming cache requests",
			},
				[]string{"method", "kind", "status"}),
		}

		return nil
	}
}
