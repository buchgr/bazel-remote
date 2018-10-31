package s3

import (
	"io"
	"log"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/minio/minio-go"
)

type s3Cache struct {
	mclient  *minio.Client
	location string
	bucket   string
}

// New erturns a new instance of the S3-API based cached
func New(endpoint string, bucket string, location string,
	accessKeyId string, secretAccessKey string) cache.Cache {
	// For now, do not use SSL in the test POC stage.
	useSSL := false

	// Initialize minio client object.
	minioClient, err := minio.New(endpoint, accessKeyId, secretAccessKey, useSSL)
	if err != nil {
		log.Fatalln(err)
	}

	log.Printf("%#v\n", minioClient) // minioClient is now setup

	cache := &s3Cache{
		mclient:  minioClient,
		location: location,
		bucket:   bucket,
	}
	return cache
}

// Put stores a stream of `size` bytes from `r` into the cache. If `expectedSha256` is
// not the empty string, and the contents don't match it, an error is returned
func (c *s3Cache) Put(key string, size int64, expectedSha256 string, r io.Reader) error {

	// Upload the zip file with PutObject
	// PutObject(bucketName, objectName string, reader io.Reader, objectSize int64,opts PutObjectOptions) (n int, err error)
	n, err := c.mclient.PutObject(
		c.bucket, // bucketName
		key,      // objectName
		r,        // reader
		size,     // objectSize
		minio.PutObjectOptions{
			ContentType: "application/octet-stream",
		}, // opts
	)
	if err != nil {
		log.Fatalln(err)
		return err
	}
	log.Printf("Successfully uploaded %s of size %d (%d)\n", key, n, size)
	return nil
}

// Get writes the content of the cache item stored under `key` to `w`. If the item is
// not found, it returns ok = false.
func (c *s3Cache) Get(key string, actionCache bool) (data io.ReadCloser, sizeBytes int64, err error) {
	objInfo, err := c.mclient.StatObject(
		c.bucket, // bucketName
		key,      // objectName
		minio.StatObjectOptions{}, // opts
	)
	if err != nil {
		return nil, 0, err
	}

	object, err := c.mclient.GetObject(
		c.bucket, // bucketName
		key,      // objectName
		minio.GetObjectOptions{}, // opts
	)

	return object, objInfo.Size, err
}

// Contains returns true if the `key` exists.
func (c *s3Cache) Contains(key string, actionCache bool) (ok bool) {
	objInfo, err := c.mclient.StatObject(
		c.bucket, // bucketName
		key,      // objectName
		minio.StatObjectOptions{}, // opts
	)
	if err == nil {
		return true
	}
	return false
}

// MaxSize returns the maximum cache size in bytes.
func (c *s3Cache) MaxSize() int64 {
	// In order to return the max size, we will need to iterate over all the
	// objects in a bucket via s3. Each object will be an API call.
	// Since this can take a long time, and there are other ways (see AWS/Minio
	// dashboards) to get the same information, these are not implemented.
	return -1
}

// CurrentSize returns the current cache size in bytes.
func (c *s3Cache) CurrentSize() int64 {
	return -1
}

// NumItems returns the number of items stored in the cache.
func (c *s3Cache) NumItems() int {
	return -1
}
