package s3proxy

import (
	"errors"
	"fmt"
	"io"
	"log"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/config"
	"github.com/minio/minio-go/v6"
	"github.com/minio/minio-go/v6/pkg/credentials"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const numUploaders = 100
const maxQueuedUploads = 1000000

type uploadReq struct {
	hash string
	size int64
	kind cache.EntryKind
	rdr  io.Reader
}

type s3Cache struct {
	mcore        *minio.Core
	prefix       string
	bucket       string
	uploadQueue  chan<- uploadReq
	accessLogger cache.Logger
	errorLogger  cache.Logger
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
func New(s3Config *config.S3CloudStorageConfig, accessLogger cache.Logger,
	errorLogger cache.Logger) cache.Proxy {

	fmt.Println("Using S3 backend.")

	var minioCore *minio.Core
	var err error

	if s3Config.IAMRoleEndpoint != "" {
		// Initialize minio client object with IAM credentials
		creds := credentials.NewIAM(s3Config.IAMRoleEndpoint)
		minioClient, err := minio.NewWithCredentials(
			s3Config.Endpoint,
			creds,
			!s3Config.DisableSSL,
			s3Config.Region,
		)

		if err != nil {
			log.Fatalln(err)
		}

		minioCore = &minio.Core{
			Client: minioClient,
		}
	} else {
		// Initialize minio client object.
		minioCore, err = minio.NewCore(
			s3Config.Endpoint,
			s3Config.AccessKeyID,
			s3Config.SecretAccessKey,
			!s3Config.DisableSSL,
		)

		if err != nil {
			log.Fatalln(err)
		}
	}

	uploadQueue := make(chan uploadReq, maxQueuedUploads)
	c := &s3Cache{
		mcore:        minioCore,
		prefix:       s3Config.Prefix,
		bucket:       s3Config.Bucket,
		uploadQueue:  uploadQueue,
		accessLogger: accessLogger,
		errorLogger:  errorLogger,
	}

	for uploader := 0; uploader < numUploaders; uploader++ {
		go func() {
			for item := range uploadQueue {
				c.uploadFile(item)
			}
		}()
	}

	return c
}

func (c *s3Cache) objectKey(hash string, kind cache.EntryKind) string {
	if c.prefix == "" {
		return fmt.Sprintf("%s/%s", kind, hash)
	}
	return fmt.Sprintf("%s/%s/%s", c.prefix, kind, hash)
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
	uploadDigest := ""
	if item.kind == cache.CAS {
		uploadDigest = item.hash
	}

	_, err := c.mcore.PutObject(
		c.bucket,                          // bucketName
		c.objectKey(item.hash, item.kind), // objectName
		item.rdr,                          // reader
		item.size,                         // objectSize
		"",                                // md5base64
		uploadDigest,                      // sha256
		minio.PutObjectOptions{
			UserMetadata: map[string]string{
				"Content-Type": "application/octet-stream",
			},
		}, // metadata
	)

	logResponse(c.accessLogger, "UPLOAD", c.bucket, c.objectKey(item.hash, item.kind), err)
}

func (c *s3Cache) Put(kind cache.EntryKind, hash string, size int64, rdr io.Reader) {
	select {
	case c.uploadQueue <- uploadReq{
		hash: hash,
		size: size,
		kind: kind,
		rdr:  rdr,
	}:
	default:
		c.errorLogger.Printf("too many uploads queued\n")
	}
}

func (c *s3Cache) Get(kind cache.EntryKind, hash string) (io.ReadCloser, int64, error) {

	object, info, _, err := c.mcore.GetObject(
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

	logResponse(c.accessLogger, "DOWNLOAD", c.bucket, c.objectKey(hash, kind), nil)

	return object, info.Size, nil
}

func (c *s3Cache) Contains(kind cache.EntryKind, hash string) (bool, int64) {
	size := int64(-1)

	s, err := c.mcore.StatObject(
		c.bucket,                  // bucketName
		c.objectKey(hash, kind),   // objectName
		minio.StatObjectOptions{}, // opts
	)

	exists := (err == nil)
	if err != nil {
		err = errNotFound
	} else {
		size = s.Size
	}

	logResponse(c.accessLogger, "CONTAINS", c.bucket, c.objectKey(hash, kind), err)

	return exists, size
}
