package disk

import (
	"context"
	"errors"
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

var errMissingBlob = errors.New("a blob could not be found")

// Optimised implementation of FindMissingBlobs, which batches local index
// lookups and performs concurrent proxy lookups for local cache misses.
// Returns a slice with the blobs that are missing from the cache.
//
// Note that this modifies the input slice and returns a subset of it.
func (c *diskCache) FindMissingCasBlobs(ctx context.Context, blobs []*pb.Digest) ([]*pb.Digest, error) {
	err := c.findMissingCasBlobsInternal(ctx, blobs, false)
	if err != nil {
		return nil, err
	}
	return filterNonNil(blobs), nil
}

// Identifies local and proxy cache misses for blobs. Modifies the input `blobs` slice such that found
// blobs are replaced with nil, while the missing digests remain unchanged.
//
// When failFast is true and a blob could not be found in the local cache nor in the
// proxy back end, the search will immediately terminate and errMissingBlob will be returned. Given that the
// search is terminated early, the contents of blobs will only have partially been updated.
func (c *diskCache) findMissingCasBlobsInternal(ctx context.Context, blobs []*pb.Digest, failFast bool) error {
	// batchSize moderates how long the cache lock is held by findMissingLocalCAS.
	const batchSize = 20

	var cancelContextForFailFast context.CancelFunc = nil
	cancelledDueToFailFast := false

	if failFast && c.proxy != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()

		cancelContextForFailFast = func() {
			// Indicate that we were canceled so that we can fail fast.
			cancelledDueToFailFast = true
			cancel()
		}
	}

	var wg sync.WaitGroup

	var chunk []*pb.Digest
	remaining := blobs

	for len(remaining) > 0 {
		select {
		case <-ctx.Done():
			if cancelledDueToFailFast {
				return errMissingBlob
			}
			return status.Error(codes.Canceled, "Request was cancelled")
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

		if c.proxy == nil && failFast {
			// There's no proxy, there are missing blobs from the local cache, and we are failing fast.
			return errMissingBlob
		}

		if c.proxy != nil {
			for i := range chunk {
				if chunk[i] == nil {
					continue
				}

				if chunk[i].SizeBytes > c.maxProxyBlobSize {
					// The blob would exceed the limit, so skip it.
					if failFast {
						return errMissingBlob
					}
					continue
				}

				// Adding to the containsQueue channel may have blocked on a previous iteration,
				// so check to see if the context has cancelled.
				select {
				case <-ctx.Done():
					if cancelledDueToFailFast {
						return errMissingBlob
					}
					return status.Error(codes.Canceled, "Request was cancelled")
				default:
				}

				wg.Add(1)
				c.containsQueue <- proxyCheck{
					wg:     &wg,
					digest: &chunk[i],
					ctx:    ctx,
					// When failFast is true, onProxyMiss will have been set to a function that
					// will cancel the context, causing the remaining proxyChecks to short-circuit.
					onProxyMiss: cancelContextForFailFast,
				}
			}
		}
	}

	if c.proxy != nil {
		// Adapt the waitgroup for select
		waitCh := make(chan struct{})
		go func() {
			wg.Wait()
			close(waitCh)
		}()

		// Wait for all proxyChecks to finish or a context cancellation.
		select {
		case <-ctx.Done():
			if cancelledDueToFailFast {
				return errMissingBlob
			}
			return status.Error(codes.Canceled, "Request was cancelled")
		case <-waitCh: // Everything in the waitgroup has finished.
		}
	}

	return nil
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
	var item lruItem
	var key string
	missing := 0

	c.mu.Lock()

	for i := range blobs {
		if blobs[i].SizeBytes == 0 && blobs[i].Hash == emptySha256 {
			c.accessLogger.Printf("GRPC CAS HEAD %s OK", blobs[i].Hash)
			blobs[i] = nil
			continue
		}

		foundSize := int64(-1)
		key = cache.LookupKey(cache.CAS, blobs[i].Hash)
		item, exists = c.lru.Get(key)
		if exists {
			foundSize = item.size
		}

		if exists && !isSizeMismatch(blobs[i].SizeBytes, foundSize) {
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
