package s3

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/config"
	"github.com/minio/minio-go/v6"
)

const numUploaders = 100
const maxQueuedUploads = 1000000

type uploadReq struct {
	hash string
	kind cache.EntryKind
}

type s3Cache struct {
	mcore        *minio.Core
	local        cache.Cache
	prefix       string
	bucket       string
	uploadQueue  chan<- (*uploadReq)
	accessLogger cache.Logger
	errorLogger  cache.Logger
}

// New erturns a new instance of the S3-API based cached
func New(s3Config *config.S3CloudStorageConfig, local cache.Cache, accessLogger cache.Logger,
	errorLogger cache.Logger) cache.Cache {

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

	uploadQueue := make(chan *uploadReq, maxQueuedUploads)
	c := &s3Cache{
		mcore:        minioCore,
		local:        local,
		prefix:       s3Config.Prefix,
		bucket:       s3Config.Bucket,
		uploadQueue:  uploadQueue,
		accessLogger: accessLogger,
		errorLogger:  errorLogger,
	}

	for uploader := 0; uploader < numUploaders; uploader++ {
		go func() {
			for item := range uploadQueue {
				c.uploadFile(item.hash, item.kind)
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
	log.Printf("%4s %3s %15s %s err=%v", method, "", bucket, key, err)
}

func (c *s3Cache) uploadFile(hash string, kind cache.EntryKind) {
	data, size, err := c.local.Get(kind, hash)
	if err != nil {
		return
	}

	uploadDigest := ""
	if kind == cache.CAS {
		uploadDigest = hash
	}

	_, err = c.mcore.PutObject(
		c.bucket,                // bucketName
		c.objectKey(hash, kind), // objectName
		data,                    // reader
		size,                    // objectSize
		"",                      // md5base64
		uploadDigest,            // sha256
		map[string]string{
			"Content-Type": "application/octet-stream",
		}, // metadata
		nil, // sse
	)
	if data != nil {
		data.Close()
	}
	logResponse(c.accessLogger, "PUT", c.bucket, c.objectKey(hash, kind), err)
}

func (c *s3Cache) Put(kind cache.EntryKind, hash string, size int64, data io.Reader) (err error) {
	if c.local.Contains(kind, hash) {
		io.Copy(ioutil.Discard, data)
		return nil
	}
	c.local.Put(kind, hash, size, data)

	select {
	case c.uploadQueue <- &uploadReq{
		hash: hash,
		kind: kind,
	}:
	default:
		c.errorLogger.Printf("too many uploads queued\n")
	}
	return nil
}

func (c *s3Cache) Get(kind cache.EntryKind, hash string) (io.ReadCloser, int64, error) {
	if c.local.Contains(kind, hash) {
		return c.local.Get(kind, hash)
	}

	object, info, _, err := c.mcore.GetObject(
		c.bucket,                 // bucketName
		c.objectKey(hash, kind),  // objectName
		minio.GetObjectOptions{}, // opts
	)
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, 0, nil // not found

		}
		return nil, 0, err
	}
	defer object.Close()

	logResponse(c.accessLogger, "GET", c.bucket, c.objectKey(hash, kind), err)

	err = c.local.Put(kind, hash, info.Size, object)
	if err != nil {
		return nil, -1, err
	}

	return c.local.Get(kind, hash)
}

func (c *s3Cache) Contains(kind cache.EntryKind, hash string) bool {
	return c.local.Contains(kind, hash)
}

func (c *s3Cache) MaxSize() int64 {
	return c.local.MaxSize()
}

func (c *s3Cache) Stats() (currentSize int64, numItems int) {
	return c.local.Stats()
}
