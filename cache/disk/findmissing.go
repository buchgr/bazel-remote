package disk

import (
	"context"
	"sync"

	"github.com/buchgr/bazel-remote/cache"

	pb "github.com/buchgr/bazel-remote/genproto/build/bazel/remote/execution/v2"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type proxyCheck struct {
	wg          *sync.WaitGroup
	digest      **pb.Digest
	ctx         context.Context
	onProxyMiss func()
}

// Optimised implementation of FindMissingBlobs, which batches local index
// lookups and performs concurrent proxy lookups for local cache misses.
// Returns a slice with the blobs that are missing from the cache.
//
// Note that this modifies the input slice and returns a subset of it.
func (c *diskCache) FindMissingCasBlobs(ctx context.Context, blobs []*pb.Digest) ([]*pb.Digest, error) {
	return c.findMissingCasBlobsInternal(ctx, blobs, false)
}

func (c *diskCache) findMissingCasBlobsInternal(ctx context.Context, blobs []*pb.Digest, failFast bool) ([]*pb.Digest, error) {
	// batchSize moderates how long the cache lock is held by findMissingLocalCAS.
	const batchSize = 20

	var contextCancel context.CancelFunc = nil

	if failFast && c.proxy != nil {
		// We want the contains worker to cancel the context on a cache miss, allowing us to fail fast.
		ctx, contextCancel = context.WithCancel(ctx)

		defer contextCancel()
	}

	var wg sync.WaitGroup

	var chunk []*pb.Digest
	remaining := blobs

	for len(remaining) > 0 {
		select {
		case <-ctx.Done():
			return nil, status.Error(codes.Canceled, "Request was cancelled")
		default:
		}

		if len(remaining) <= batchSize {
			chunk = remaining
			remaining = nil
		} else {
			chunk = remaining[:batchSize]
			remaining = remaining[batchSize:]
		}

		numMissing := c.findMissingLocalCAS(chunk)
		if numMissing == 0 {
			continue
		}

		if c.proxy != nil {
			wg.Add(numMissing)
			for i := range chunk {
				if chunk[i] != nil {
					c.containsQueue <- proxyCheck{
						wg:          &wg,
						digest:      &chunk[i],
						ctx:         ctx,
						onProxyMiss: contextCancel,
					}
				}
			}
		} else if failFast {
			break
		}
	}

	if c.proxy != nil {
		wg.Wait()
	}

	return filterNonNil(blobs), nil
}

// Move all the non-nil items in the input slice to the
// start, and return the non-nil sub-slice.
func filterNonNil(blobs []*pb.Digest) []*pb.Digest {
	count := 0
	for i := 0; i < len(blobs); i++ {
		if blobs[i] != nil {
			blobs[count] = blobs[i]
			count++
		}
	}

	return blobs[:count]
}

// Set blobs that exist in the disk cache to nil, and return the number
// of missing blobs.
func (c *diskCache) findMissingLocalCAS(blobs []*pb.Digest) int {
	var exists bool
	var key string
	missing := 0

	c.mu.Lock()

	for i := range blobs {
		if blobs[i].SizeBytes == 0 {
			c.accessLogger.Printf("GRPC CAS HEAD %s OK", blobs[i].Hash)
			blobs[i] = nil
			continue
		}

		key = cache.LookupKey(cache.CAS, blobs[i].Hash)
		_, exists = c.lru.Get(key)
		if exists {
			c.accessLogger.Printf("GRPC CAS HEAD %s OK", blobs[i].Hash)
			blobs[i] = nil
		} else {
			missing++
		}
	}

	c.mu.Unlock()

	return missing
}

func (c *diskCache) containsWorker() {
	var ok bool
	for req := range c.containsQueue {
		if req.ctx != nil {
			select {
			case <-req.ctx.Done():
				// Fast-fail if the context has already been cancelled.
				c.accessLogger.Printf("GRPC CAS HEAD %s CANCELLED", (*req.digest).Hash)
				req.wg.Done()
				continue
			default:
			}
		}

		ok, _ = c.proxy.Contains(req.ctx, cache.CAS, (*req.digest).Hash)
		if ok {
			c.accessLogger.Printf("GRPC CAS HEAD %s OK", (*req.digest).Hash)
			// The blob exists on the proxy, remove it from the
			// list of missing blobs.
			*(req.digest) = nil
		} else {
			c.accessLogger.Printf("GRPC CAS HEAD %s NOT FOUND", (*req.digest).Hash)
			if req.onProxyMiss != nil {
				req.onProxyMiss()
			}
		}
		req.wg.Done()
	}
}

func (c *diskCache) spawnContainsQueueWorkers() {
	// TODO: make these configurable?
	const queueSize = 2048
	const numWorkers = 512

	c.containsQueue = make(chan proxyCheck, queueSize)
	for i := 0; i < numWorkers; i++ {
		go c.containsWorker()
	}
}
