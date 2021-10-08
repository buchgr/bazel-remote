package disk

import (
	"fmt"
	"github.com/buchgr/bazel-remote/cache/disk/zstdimpl"
	"log"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/disk/casblob"
)

type Option func(*Cache) error

func WithStorageMode(mode string) Option {
	return func(c *Cache) error {
		if mode == "zstd" {
			c.storageMode = casblob.Zstandard
			return nil
		} else if mode == "uncompressed" {
			c.storageMode = casblob.Identity
			return nil
		} else {
			return fmt.Errorf("Unsupported storage mode: " + mode)
		}
	}
}

func WithZstdImplementation(impl string) Option {
	return func(c *Cache) error {
		c.zstd = zstdimpl.Get(impl)
		return nil
	}
}

func WithMaxBlobSize(size int64) Option {
	return func(c *Cache) error {
		if size <= 0 {
			return fmt.Errorf("Invalid MaxBlobSize: %d", size)
		}

		c.maxBlobSize = size
		return nil
	}
}

func WithProxyBackend(proxy cache.Proxy) Option {
	return func(c *Cache) error {
		if c.proxy != nil && proxy != nil {
			return fmt.Errorf("Proxy backends may be set only once")
		}

		if proxy != nil {
			c.proxy = proxy
			c.spawnContainsQueueWorkers()
		}

		return nil
	}
}

func WithAccessLogger(logger *log.Logger) Option {
	return func(c *Cache) error {
		c.accessLogger = logger
		return nil
	}
}
