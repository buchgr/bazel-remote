package s3proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/disk/casblob"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type uploadReq struct {
	hash string
	size int64
	kind cache.EntryKind
	rc   io.ReadCloser
}

type s3Cache struct {
	mcore            *minio.Core
	prefix           string
	bucket           string
	uploadQueue      chan<- uploadReq
	accessLogger     cache.Logger
	errorLogger      cache.Logger
	v2mode           bool
	updateTimestamps bool
	objectKey        func(hash string, kind cache.EntryKind) string
}

var (
	cacheHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bazel_remote_s3_cache_hits",
		Help: "The total number of s3 backend cache hits",
	})
	cacheMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bazel_remote_s3_cache_misses",
		Help: "The total number of s3 backend cache misses",
	})
)

// Used in place of minio's verbose "NoSuchKey" error.
var errNotFound = errors.New("NOT FOUND")

// New returns a new instance of the S3-API based cache
func New(
	// S3CloudStorageConfig struct fields:
	Endpoint string,
	Bucket string,
	Prefix string,
	Credentials *credentials.Credentials,
	DisableSSL bool,
	UpdateTimestamps bool,
	Region string,

	storageMode string, accessLogger cache.Logger,
	errorLogger cache.Logger, numUploaders, maxQueuedUploads int) cache.Proxy {

	fmt.Println("Using S3 backend.")

	var minioCore *minio.Core
	var err error

	if Credentials == nil {
		log.Fatalf("Failed to determine s3proxy credentials")
	}

	// Initialize minio client with credentials
	opts := &minio.Options{
		Creds: Credentials,

		Region: Region,
		Secure: !DisableSSL,
	}
	minioCore, err = minio.NewCore(Endpoint, opts)
	if err != nil {
		log.Fatalln(err)
	}

	if storageMode != "zstd" && storageMode != "uncompressed" {
		log.Fatalf("Unsupported storage mode for the s3proxy backend: %q, must be one of \"zstd\" or \"uncompressed\"",
			storageMode)
	}

	c := &s3Cache{
		mcore:            minioCore,
		prefix:           Prefix,
		bucket:           Bucket,
		accessLogger:     accessLogger,
		errorLogger:      errorLogger,
		v2mode:           storageMode == "zstd",
		updateTimestamps: UpdateTimestamps,
	}

	if c.v2mode {
		c.objectKey = func(hash string, kind cache.EntryKind) string {
			return objectKeyV2(c.prefix, hash, kind)
		}
	} else {
		c.objectKey = func(hash string, kind cache.EntryKind) string {
			return objectKeyV1(c.prefix, hash, kind)
		}
	}

	if maxQueuedUploads > 0 && numUploaders > 0 {
		uploadQueue := make(chan uploadReq, maxQueuedUploads)
		for uploader := 0; uploader < numUploaders; uploader++ {
			go func() {
				for item := range uploadQueue {
					c.uploadFile(item)
				}
			}()
		}

		c.uploadQueue = uploadQueue
	}

	return c
}

func objectKeyV2(prefix string, hash string, kind cache.EntryKind) string {
	var baseKey string
	if kind == cache.CAS {
		// Use "cas.v2" to distinguish new from old format blobs.
		baseKey = path.Join("cas.v2", hash[:2], hash)
	} else {
		baseKey = path.Join(kind.String(), hash[:2], hash)
	}

	if prefix == "" {
		return baseKey
	}

	return path.Join(prefix, baseKey)
}

func objectKeyV1(prefix string, hash string, kind cache.EntryKind) string {
	if prefix == "" {
		return path.Join(kind.String(), hash[:2], hash)
	}

	return path.Join(prefix, kind.String(), hash[:2], hash)
}

// Helper function for logging responses
func logResponse(log cache.Logger, method, bucket, key string, err error) {
	status := "OK"
	if err != nil {
		status = err.Error()
	}

	log.Printf("S3 %s %s %s %s", method, bucket, key, status)
}

func (c *s3Cache) uploadFile(item uploadReq) {
	_, err := c.mcore.PutObject(
		context.Background(),
		c.bucket,                          // bucketName
		c.objectKey(item.hash, item.kind), // objectName
		item.rc,                           // reader
		item.size,                         // objectSize
		"",                                // md5base64
		"",                                // sha256
		minio.PutObjectOptions{
			UserMetadata: map[string]string{
				"Content-Type": "application/octet-stream",
			},
		}, // metadata
	)

	logResponse(c.accessLogger, "UPLOAD", c.bucket, c.objectKey(item.hash, item.kind), err)

	item.rc.Close()
}

func (c *s3Cache) Put(ctx context.Context, kind cache.EntryKind, hash string, size int64, rc io.ReadCloser) {
	if c.uploadQueue == nil {
		rc.Close()
		return
	}

	select {
	case c.uploadQueue <- uploadReq{
		hash: hash,
		size: size,
		kind: kind,
		rc:   rc,
	}:
	default:
		c.errorLogger.Printf("too many uploads queued\n")
		rc.Close()
	}
}

func (c *s3Cache) UpdateModificationTimestamp(ctx context.Context, bucket string, object string) {
	src := minio.CopySrcOptions{
		Bucket: bucket,
		Object: object,
	}

	dst := minio.CopyDestOptions{
		Bucket: bucket,
		Object: object,
	}

	_, err := c.mcore.ComposeObject(context.Background(), dst, src)

	logResponse(c.accessLogger, "COMPOSE", bucket, object, err)
}

func (c *s3Cache) Get(ctx context.Context, kind cache.EntryKind, hash string) (io.ReadCloser, int64, error) {

	rc, info, _, err := c.mcore.GetObject(
		ctx,
		c.bucket,                 // bucketName
		c.objectKey(hash, kind),  // objectName
		minio.GetObjectOptions{}, // opts
	)
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			cacheMisses.Inc()
			logResponse(c.accessLogger, "DOWNLOAD", c.bucket, c.objectKey(hash, kind), errNotFound)
			return nil, -1, nil
		}
		cacheMisses.Inc()
		logResponse(c.accessLogger, "DOWNLOAD", c.bucket, c.objectKey(hash, kind), err)
		return nil, -1, err
	}
	cacheHits.Inc()

	if c.updateTimestamps {
		c.UpdateModificationTimestamp(ctx, c.bucket, c.objectKey(hash, kind))
	}

	logResponse(c.accessLogger, "DOWNLOAD", c.bucket, c.objectKey(hash, kind), nil)

	if kind == cache.CAS && c.v2mode {
		return casblob.ExtractLogicalSize(rc)
	}

	return rc, info.Size, nil
}

func (c *s3Cache) Contains(ctx context.Context, kind cache.EntryKind, hash string) (bool, int64) {
	size := int64(-1)
	exists := false

	s, err := c.mcore.StatObject(
		ctx,
		c.bucket,                  // bucketName
		c.objectKey(hash, kind),   // objectName
		minio.StatObjectOptions{}, // opts
	)

	exists = (err == nil)
	if err != nil {
		err = errNotFound
	} else if kind != cache.CAS || !c.v2mode {
		size = s.Size
	}

	logResponse(c.accessLogger, "CONTAINS", c.bucket, c.objectKey(hash, kind), err)

	return exists, size
}
