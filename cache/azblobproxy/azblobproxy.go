package azblobproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path"
	"time"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk/casblob"
	"github.com/buchgr/bazel-remote/v2/utils/backendproxy"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	cacheHits = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bazel_remote_azblob_cache_hits",
		Help: "The total number of azblob backend cache hits",
	})
	cacheMisses = promauto.NewCounter(prometheus.CounterOpts{
		Name: "bazel_remote_azblob_cache_misses",
		Help: "The total number of azblob backend cache misses",
	})
)

type azBlobCache struct {
	containerClient  *azblob.ContainerClient
	storageAccount   string
	container        string
	prefix           string
	v2mode           bool
	uploadQueue      chan<- backendproxy.UploadReq
	accessLogger     cache.Logger
	errorLogger      cache.Logger
	objectKey        func(hash string, kind cache.EntryKind) string
	updateTimestamps bool
}

func (c *azBlobCache) Put(ctx context.Context, kind cache.EntryKind, hash string, logicalSize int64, sizeOnDisk int64, rc io.ReadCloser) {
	if c.uploadQueue == nil {
		rc.Close()
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
		rc.Close()
	}
}

func (c *azBlobCache) Get(ctx context.Context, kind cache.EntryKind, hash string, _ int64) (rc io.ReadCloser, size int64, err error) {
	key := c.objectKey(hash, kind)
	if c.prefix != "" {
		key = c.prefix + "/" + key
	}
	client, err := c.containerClient.NewBlockBlobClient(key)

	if err != nil {
		cacheMisses.Inc()
		logResponse(c.accessLogger, "DOWNLOAD", c.storageAccount, c.container, key, err)
		return nil, -1, err
	}

	resp, err := client.Download(context.Background(), nil)
	if err != nil {
		var stgErr *azblob.StorageError
		cacheMisses.Inc()
		if errors.As(err, &stgErr) && stgErr.ErrorCode == azblob.StorageErrorCodeBlobNotFound {
			logResponse(c.accessLogger, "DOWNLOAD", c.storageAccount, c.container, key, errNotFound)
			return nil, -1, nil
		}
		logResponse(c.accessLogger, "DOWNLOAD", c.storageAccount, c.container, key, err)
		return nil, -1, err
	}
	cacheHits.Inc()

	if c.updateTimestamps {
		c.UpdateModificationTimestamp(ctx, key)
	}

	logResponse(c.accessLogger, "DOWNLOAD", c.storageAccount, c.container, key, err)

	rc = resp.Body(&azblob.RetryReaderOptions{MaxRetryRequests: 2})

	if kind == cache.CAS && c.v2mode {
		return casblob.ExtractLogicalSize(rc)
	}

	if resp.ContentLength != nil {
		size = *resp.ContentLength
	}

	return rc, size, nil
}

var errNotFound = errors.New("NOT FOUND")

func (c *azBlobCache) Contains(ctx context.Context, kind cache.EntryKind, hash string, _ int64) (bool, int64) {
	key := c.objectKey(hash, kind)
	if c.prefix != "" {
		key = c.prefix + "/" + key
	}

	size := int64(-1)
	exists := false

	client, err := c.containerClient.NewBlobClient(key)
	if err != nil {
		logResponse(c.accessLogger, "CONTAINS", c.storageAccount, c.container, key, err)
		return exists, size
	}

	props, err := client.GetProperties(context.Background(), nil)

	exists = (err == nil)
	if err != nil {
		err = errNotFound
	} else if kind != cache.CAS || !c.v2mode {
		if props.ContentLength != nil {
			size = *props.ContentLength
		}
	}

	logResponse(c.accessLogger, "CONTAINS", c.storageAccount, c.container, key, err)

	return exists, size
}

func New(
	storageAccount string,
	containerName string,
	prefix string,
	creds azcore.TokenCredential,
	sharedKey string,
	UpdateTimestamps bool,
	storageMode string, accessLogger cache.Logger,
	errorLogger cache.Logger, numUploaders, maxQueuedUploads int,
) cache.Proxy {
	url := fmt.Sprintf("https://%s.blob.core.windows.net/", storageAccount)

	var err error
	var serviceClient *azblob.ServiceClient
	if creds == nil && len(sharedKey) > 0 {
		cred, e := azblob.NewSharedKeyCredential(storageAccount, sharedKey)
		if e != nil {
			log.Fatalln(e)
		}
		serviceClient, err = azblob.NewServiceClientWithSharedKey(url, cred, nil)

	} else {
		serviceClient, err = azblob.NewServiceClient(url, creds, nil)
	}

	if err != nil {
		log.Fatalln(err)
	}

	containerClient, err := serviceClient.NewContainerClient(containerName)
	if err != nil {
		log.Fatalln(err)
	}

	if storageMode != "zstd" && storageMode != "uncompressed" {
		log.Fatalf("Unsupported storage mode for the azblobproxy backend: %q, must be one of \"zstd\" or \"uncompressed\"",
			storageMode)
	}

	c := &azBlobCache{
		containerClient:  containerClient,
		prefix:           prefix,
		storageAccount:   storageAccount,
		container:        containerName,
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

	c.uploadQueue = backendproxy.StartUploaders(c, numUploaders, maxQueuedUploads)

	return c
}

func (c *azBlobCache) UploadFile(item backendproxy.UploadReq) {
	defer item.Rc.Close()

	key := c.objectKey(item.Hash, item.Kind)
	if c.prefix != "" {
		key = c.prefix + "/" + key
	}
	client, err := c.containerClient.NewBlockBlobClient(key)
	if err != nil {
		logResponse(c.accessLogger, "UPLOAD", c.storageAccount, c.container, key, err)
		return
	}

	_, err = client.Upload(context.Background(), item.Rc.(io.ReadSeekCloser), nil)

	logResponse(c.accessLogger, "UPLOAD", c.storageAccount, c.container, key, err)
}

func (c *azBlobCache) UpdateModificationTimestamp(ctx context.Context, key string) {
	client, err := c.containerClient.NewBlockBlobClient(key)
	if err != nil {
		logResponse(c.accessLogger, "UPDATE_TIMESTAMPS", c.storageAccount, c.container, key, err)
	}
	metadata := map[string]string{
		"LastModified": time.Now().Format("1/2/2006, 03:05 PM"),
	}
	_, err = client.SetMetadata(ctx, metadata, nil)
	logResponse(c.accessLogger, "UPDATE_TIMESTAMPS", c.storageAccount, c.container, key, err)
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
func logResponse(log cache.Logger, method, storageAccount, container, key string, err error) {
	status := "OK"
	if err != nil {
		status = err.Error()
	}

	log.Printf("AZBLOB %s %s %s %s", method, storageAccount, container, key, status)
}
