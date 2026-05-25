package s3proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"strings"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk/casblob"
	"github.com/buchgr/bazel-remote/v2/utils/backendproxy"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type s3Cache struct {
	mcore                *minio.Core
	prefix               string
	bucket               string
	uploadQueue          chan<- backendproxy.UploadReq
	accessLogger         cache.Logger
	errorLogger          cache.Logger
	v2mode               bool
	updateTimestamps     bool
	objectKey            func(hash string, kind cache.EntryKind) string
	sharedFilesystemMode bool // When true, S3 bucket is mapped to same filesystem as local disk
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
	BucketLookupType minio.BucketLookupType,
	Prefix string,
	Credentials *credentials.Credentials,
	DisableSSL bool,
	UpdateTimestamps bool,
	Region string,
	MaxIdleConns int,

	storageMode string, accessLogger cache.Logger,
	errorLogger cache.Logger, numUploaders, maxQueuedUploads int,
	sharedFilesystemMode bool) cache.Proxy {

	fmt.Println("Using S3 backend.")

	var minioCore *minio.Core
	var err error

	if Credentials == nil {
		log.Fatalf("Failed to determine s3proxy credentials")
	}

	secure := !DisableSSL
	tr, err := minio.DefaultTransport(secure)
	if err != nil {
		log.Fatalf("Failed to create default minio transport: %v", err)
	}

	tr.MaxIdleConns = MaxIdleConns
	tr.MaxIdleConnsPerHost = MaxIdleConns

	// Initialize minio client with credentials
	opts := &minio.Options{
		Creds:        Credentials,
		BucketLookup: BucketLookupType,

		Region:    Region,
		Secure:    secure,
		Transport: tr,
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
		mcore:                minioCore,
		prefix:               Prefix,
		bucket:               Bucket,
		accessLogger:         accessLogger,
		errorLogger:          errorLogger,
		v2mode:               storageMode == "zstd",
		updateTimestamps:     UpdateTimestamps,
		sharedFilesystemMode: sharedFilesystemMode,
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

	c.uploadQueue = backendproxy.StartUploaders(c, numUploaders, maxQueuedUploads)

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

// objectKeyPrefixSharedFS returns the prefix to search for objects in shared filesystem mode.
// In this mode, files are stored with local disk naming convention: <hash>-<size>-<random>
// so we need to list objects with this prefix to find them.
func objectKeyPrefixSharedFS(prefix string, hash string, kind cache.EntryKind) string {
	var kindDir string
	if kind == cache.CAS {
		kindDir = "cas.v2"
	} else if kind == cache.AC {
		kindDir = "ac.v2"
	} else {
		kindDir = "raw.v2"
	}

	// Prefix for listing: <kindDir>/<hash[:2]>/<hash>-
	baseKey := path.Join(kindDir, hash[:2], hash) + "-"

	if prefix == "" {
		return baseKey
	}

	return path.Join(prefix, baseKey)
}

// findObjectByPrefix lists objects matching the prefix and returns the first match.
// Used in shared filesystem mode where objects have random suffixes.
func (c *s3Cache) findObjectByPrefix(ctx context.Context, prefix string) (string, int64, error) {
	opts := minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: false,
		MaxKeys:   1,
	}

	objectCh := c.mcore.Client.ListObjects(ctx, c.bucket, opts)

	for object := range objectCh {
		if object.Err != nil {
			return "", -1, object.Err
		}
		// Skip .tmp files (in-progress uploads)
		if strings.HasSuffix(object.Key, ".tmp") {
			continue
		}
		return object.Key, object.Size, nil
	}

	return "", -1, errNotFound
}

func (c *s3Cache) UploadFile(item backendproxy.UploadReq) {
	_, err := c.mcore.PutObject(
		context.Background(),
		c.bucket,                          // bucketName
		c.objectKey(item.Hash, item.Kind), // objectName
		item.Rc,                           // reader
		item.SizeOnDisk,                   // objectSize
		"",                                // md5base64
		"",                                // sha256
		minio.PutObjectOptions{
			UserMetadata: map[string]string{
				"Content-Type": "application/octet-stream",
			},
		}, // metadata
	)

	logResponse(c.accessLogger, "UPLOAD", c.bucket, c.objectKey(item.Hash, item.Kind), err)

	_ = item.Rc.Close()
}

func (c *s3Cache) Put(ctx context.Context, kind cache.EntryKind, hash string, logicalSize int64, sizeOnDisk int64, rc io.ReadCloser) {
	// In shared filesystem mode, don't upload to S3 - the local disk write
	// will be visible via the S3 interface since they share the same storage
	if c.sharedFilesystemMode {
		_ = rc.Close()
		return
	}

	if c.uploadQueue == nil {
		_ = rc.Close()
		return
	}

	select {
	case c.uploadQueue <- backendproxy.UploadReq{
		Hash:        hash,
		LogicalSize: logicalSize,
		SizeOnDisk:  sizeOnDisk,
		Kind:        kind,
		Rc:          rc,
	}:
	default:
		c.errorLogger.Printf("too many uploads queued\n")
		_ = rc.Close()
	}
}

func (c *s3Cache) UpdateModificationTimestamp(ctx context.Context, bucket string, object string) {
	src := minio.CopySrcOptions{
		Bucket: bucket,
		Object: object,
	}

	dst := minio.CopyDestOptions{
		Bucket:          bucket,
		Object:          object,
		ReplaceMetadata: true,
	}

	_, err := c.mcore.ComposeObject(context.Background(), dst, src)

	logResponse(c.accessLogger, "COMPOSE", bucket, object, err)
}

func (c *s3Cache) Get(ctx context.Context, kind cache.EntryKind, hash string, _ int64) (io.ReadCloser, int64, error) {
	var objectKey string
	var objectSize int64

	if c.sharedFilesystemMode {
		// In shared filesystem mode, files use local disk naming: <hash>-<size>-<random>
		// We need to list objects to find the right one
		prefix := objectKeyPrefixSharedFS(c.prefix, hash, kind)
		var err error
		objectKey, objectSize, err = c.findObjectByPrefix(ctx, prefix)
		if err != nil {
			cacheMisses.Inc()
			logResponse(c.accessLogger, "DOWNLOAD", c.bucket, prefix+"*", errNotFound)
			return nil, -1, nil
		}
	} else {
		objectKey = c.objectKey(hash, kind)
	}

	rc, info, _, err := c.mcore.GetObject(
		ctx,
		c.bucket,                 // bucketName
		objectKey,                // objectName
		minio.GetObjectOptions{}, // opts
	)
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			cacheMisses.Inc()
			logResponse(c.accessLogger, "DOWNLOAD", c.bucket, objectKey, errNotFound)
			return nil, -1, nil
		}
		cacheMisses.Inc()
		logResponse(c.accessLogger, "DOWNLOAD", c.bucket, objectKey, err)
		return nil, -1, err
	}
	cacheHits.Inc()

	if c.updateTimestamps {
		c.UpdateModificationTimestamp(ctx, c.bucket, objectKey)
	}

	logResponse(c.accessLogger, "DOWNLOAD", c.bucket, objectKey, nil)

	if kind == cache.CAS && c.v2mode {
		return casblob.ExtractLogicalSize(rc)
	}

	if c.sharedFilesystemMode {
		return rc, objectSize, nil
	}
	return rc, info.Size, nil
}

func (c *s3Cache) Contains(ctx context.Context, kind cache.EntryKind, hash string, _ int64) (bool, int64) {
	size := int64(-1)
	exists := false

	if c.sharedFilesystemMode {
		// In shared filesystem mode, files use local disk naming: <hash>-<size>-<random>
		// We need to list objects to find the right one
		prefix := objectKeyPrefixSharedFS(c.prefix, hash, kind)
		objectKey, objectSize, err := c.findObjectByPrefix(ctx, prefix)
		if err == nil {
			exists = true
			if kind != cache.CAS || !c.v2mode {
				size = objectSize
			}
			logResponse(c.accessLogger, "CONTAINS", c.bucket, objectKey, nil)
		} else {
			logResponse(c.accessLogger, "CONTAINS", c.bucket, prefix+"*", errNotFound)
		}
		return exists, size
	}

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
