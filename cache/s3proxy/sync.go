package s3proxy

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"time"

	"github.com/buchgr/bazel-remote/v2/cache"

	"github.com/minio/minio-go/v7"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	defaultSyncUploadMaxAttempts = 5
	defaultSyncUploadBaseBackoff = 100 * time.Millisecond
	defaultSyncUploadMaxBackoff  = 5 * time.Second
)

var (
	syncUploadDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "bazel_remote_s3_sync_upload_duration_seconds",
		Help:    "The time taken by synchronous s3 uploads, retries included",
		Buckets: prometheus.ExponentialBuckets(0.01, 2, 12),
	})
	syncUploadRetries = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bazel_remote_s3_sync_upload_retries",
		Help: "The total number of retried synchronous s3 upload attempts",
	})
	syncUploadFailures = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bazel_remote_s3_sync_upload_failures",
		Help: "The total number of synchronous s3 uploads that failed after all attempts",
	})
)

// syncS3Cache is the s3 proxy with s3.sync_upload enabled. It implements
// cache.SyncProxy, so the disk cache acknowledges a write only after
// PutSync has stored it in the bucket.
type syncS3Cache struct {
	*s3Cache

	syncCore    *minio.Core // Makes a single attempt per PutObject call.
	maxAttempts int
	baseBackoff time.Duration
	maxBackoff  time.Duration
}

var _ cache.SyncProxy = (*syncS3Cache)(nil)

// PutSync uploads the item, retrying failed attempts with exponential
// backoff and jitter until maxAttempts is reached or ctx is done.
func (c *syncS3Cache) PutSync(ctx context.Context, kind cache.EntryKind, hash string, logicalSize int64, sizeOnDisk int64, rc io.ReadCloser) error {
	defer func() { _ = rc.Close() }()

	start := time.Now()
	defer func() { syncUploadDuration.Observe(time.Since(start).Seconds()) }()

	key := c.objectKey(hash, kind)
	rs, seekable := rc.(io.ReadSeeker)
	backoff := c.baseBackoff

	var err error
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			if _, serr := rs.Seek(0, io.SeekStart); serr != nil {
				err = fmt.Errorf("rewinding for retry: %w", serr)
				break
			}
		}

		_, err = c.syncCore.PutObject(ctx, c.bucket, key, rc, sizeOnDisk, "", "",
			minio.PutObjectOptions{
				UserMetadata: map[string]string{
					"Content-Type": "application/octet-stream",
				},
			})
		logResponse(c.accessLogger, "UPLOAD", c.bucket, key, err)
		if err == nil {
			return nil
		}

		if attempt >= c.maxAttempts || !seekable || ctx.Err() != nil {
			break
		}

		// Equal jitter: sleep between backoff/2 and backoff.
		sleep := backoff/2 + rand.N(backoff/2+1)
		t := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			t.Stop()
			syncUploadFailures.Inc()
			return fmt.Errorf("%w (last attempt: %v)", ctx.Err(), err)
		case <-t.C:
		}
		syncUploadRetries.Inc()
		backoff = min(2*backoff, c.maxBackoff)
	}

	syncUploadFailures.Inc()
	c.errorLogger.Printf("S3 UPLOAD %s %s failed: %v", c.bucket, key, err)
	return err
}
