package s3

import (
	"bytes"
	"errors"
	"crypto"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"log"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/config"
	"github.com/minio/minio-go/v6"
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
	errorLogger cache.Logger) cache.CacheProxy {

	fmt.Println("Using S3 backend.")

	// Initialize minio client object.
	minioCore, err := minio.NewCore(
		s3Config.Endpoint,
		s3Config.AccessKeyID,
		s3Config.SecretAccessKey,
		!s3Config.DisableSSL,
	)
	if err != nil {
		log.Fatalln(err)
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
	uploadDigestSha256 := ""
	uploadDigestMd5Base64 := ""
	if item.kind == cache.CAS {
		hashType := cache.GetHashType(item.hash)
		if hashType == crypto.SHA256 {
			// Use SHA256 hash as-is
			uploadDigestSha256 = item.hash
		} else if hashType == crypto.MD5 {
			// Convert MD5 hex string to base64 encoding
			md5Bytes, err := hex.DecodeString(item.hash)
			if err != nil {
				return
			}
			uploadDigestMd5Base64 = base64.StdEncoding.EncodeToString(md5Bytes)
		} else {
			// Hash the data using SHA256
			data, err := ioutil.ReadAll(item.rdr)
			if err != nil {
				return
			}
			hasher := crypto.SHA256.New()
			if _, err := io.Copy(io.Writer(hasher), bytes.NewBuffer(data)); err != nil {
				return
			}
			uploadDigestSha256 = hex.EncodeToString(hasher.Sum(nil))

			// Create a new reader as we read item.rdr for hashing
			item.rdr = bytes.NewReader(data)
		}
	}

	_, err := c.mcore.PutObject(
		c.bucket,                          // bucketName
		c.objectKey(item.hash, item.kind), // objectName
		item.rdr,                          // reader
		item.size,                         // objectSize
		uploadDigestMd5Base64,             // md5base64
		uploadDigestSha256,                // sha256
		map[string]string{
			"Content-Type": "application/octet-stream",
		}, // metadata
		nil, // sse
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

func (c *s3Cache) Contains(kind cache.EntryKind, hash string) bool {

	_, err := c.mcore.StatObject(
		c.bucket,                  // bucketName
		c.objectKey(hash, kind),   // objectName
		minio.StatObjectOptions{}, // opts
	)

	exists := (err == nil)
	if err != nil {
		err = errNotFound
	}
	logResponse(c.accessLogger, "CONTAINS", c.bucket, c.objectKey(hash, kind), err)

	return exists
}
