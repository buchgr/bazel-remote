package s3proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/config"
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
	mcore        *minio.Core
	prefix       string
	bucket       string
	keyVersion   int
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
	errorLogger cache.Logger, numUploaders, maxQueuedUploads int) cache.Proxy {

	fmt.Println("Using S3 backend.")

	var minioCore *minio.Core
	var err error

	if s3Config.AccessKeyID != "" && s3Config.SecretAccessKey != "" {
		// Initialize minio client object.
		opts := &minio.Options{
			Creds:  credentials.NewStaticV4(s3Config.AccessKeyID, s3Config.SecretAccessKey, ""),
			Secure: !s3Config.DisableSSL,
			Region: s3Config.Region,
		}
		minioCore, err = minio.NewCore(s3Config.Endpoint, opts)
		if err != nil {
			log.Fatalln(err)
		}
	} else {
		// Initialize minio client object with IAM credentials
		opts := &minio.Options{
			// This config value may be empty.
			Creds: credentials.NewIAM(s3Config.IAMRoleEndpoint),

			Region: s3Config.Region,
			Secure: !s3Config.DisableSSL,
		}

		minioClient, err := minio.New(
			s3Config.Endpoint,
			opts,
		)
		if err != nil {
			log.Fatalln(err)
		}

		minioCore = &minio.Core{
			Client: minioClient,
		}
	}

	c := &s3Cache{
		mcore:        minioCore,
		prefix:       s3Config.Prefix,
		bucket:       s3Config.Bucket,
		keyVersion:   s3Config.KeyVersion,
		accessLogger: accessLogger,
		errorLogger:  errorLogger,
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

func (c *s3Cache) objectKey(hash string, kind cache.EntryKind) string {
	var baseKey string
	if kind == cache.CAS {
		// Use "cas.v2" to distinguish new from old format blobs.
		baseKey = path.Join("cas.v2", hash[:2], hash)
	} else {
		baseKey = path.Join(kind.String(), hash[:2], hash)
	}

	if c.prefix == "" {
		return baseKey
	}

	return path.Join(c.prefix, baseKey)
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

func (c *s3Cache) Put(kind cache.EntryKind, hash string, size int64, rc io.ReadCloser) {
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

func (c *s3Cache) Get(kind cache.EntryKind, hash string) (io.ReadCloser, int64, error) {

	object, info, _, err := c.mcore.GetObject(
		context.Background(),
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
	exists := false

	if kind != cache.CAS {
		s, err := c.mcore.StatObject(
			context.Background(),
			c.bucket,                  // bucketName
			c.objectKey(hash, kind),   // objectName
			minio.StatObjectOptions{}, // opts
		)

		exists = (err == nil)
		if err != nil {
			err = errNotFound
		} else {
			size = s.Size
		}

		logResponse(c.accessLogger, "CONTAINS", c.bucket, c.objectKey(hash, kind), err)

		return exists, size
	}

	// Handle the more complicated, compressed CAS blob case.

	// https://github.com/minio/minio-go/issues/1106

	var err error
	var uncompressedSize int64
	var n int
	var req *http.Request
	var rsp *http.Response
	var blobHeader []byte
	var u *url.URL

	u, err = c.mcore.PresignedGetObject(context.Background(), c.bucket,
		c.objectKey(hash, kind), time.Second*20, make(url.Values))
	if err != nil {
		goto end
	}

	// TODO: The following code is essentially duplicated from the
	// httpproxy code. Refactor and reuse it (and drop the goto's)?

	req, err = http.NewRequest("GET", u.String(), nil)
	if err != nil {
		goto end
	}
	req.Header.Add("Range", "bytes=0-7")
	rsp, err = http.DefaultClient.Do(req) // FIXME: use a single http.Client
	if err != nil {
		goto end
	}
	if rsp.StatusCode != http.StatusOK && rsp.StatusCode != http.StatusPartialContent {
		goto end
	}

	blobHeader = make([]byte, 8)
	n, err = io.ReadFull(rsp.Body, blobHeader)
	if err != nil {
		goto end
	}
	defer rsp.Body.Close()
	if n != 8 {
		goto end
	}

	err = binary.Read(bytes.NewReader(blobHeader), binary.LittleEndian,
		&uncompressedSize)
	if err != nil {
		goto end
	}
	exists = true
	size = uncompressedSize

end:
	logResponse(c.accessLogger, "CONTAINS", c.bucket, c.objectKey(hash, kind), err)

	return exists, size
}
