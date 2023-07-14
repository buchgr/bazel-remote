package config

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"

	"github.com/buchgr/bazel-remote/v2/cache/azblobproxy"
	"github.com/buchgr/bazel-remote/v2/cache/gcsproxy"
	"github.com/buchgr/bazel-remote/v2/cache/httpproxy"
	"github.com/buchgr/bazel-remote/v2/cache/s3proxy"
	"github.com/minio/minio-go/v7"
)

func (c *Config) setProxy() error {
	if c.GoogleCloudStorage != nil {
		proxyCache, err := gcsproxy.New(c.GoogleCloudStorage.Bucket,
			c.GoogleCloudStorage.UseDefaultCredentials, c.GoogleCloudStorage.JSONCredentialsFile,
			c.StorageMode, c.AccessLogger, c.ErrorLogger, c.NumUploaders, c.MaxQueuedUploads)
		if err != nil {
			return err
		}

		c.ProxyBackend = proxyCache
		return nil
	}

	if c.HTTPBackend != nil {
		httpClient := &http.Client{}
		var baseURL *url.URL
		baseURL, err := url.Parse(c.HTTPBackend.BaseURL)
		if err != nil {
			return err
		}

		if c.HTTPBackend.CertFile != "" && c.HTTPBackend.KeyFile != "" {
			readCert, err := tls.LoadX509KeyPair(
				c.HTTPBackend.CertFile,
				c.HTTPBackend.KeyFile,
			)
			if err != nil {
				return err
			}

			config := &tls.Config{
				Certificates: []tls.Certificate{readCert},
			}

			tr := &http.Transport{TLSClientConfig: config}
			httpClient.Transport = tr
		}

		proxyCache, err := httpproxy.New(baseURL, c.StorageMode,
			httpClient, c.AccessLogger, c.ErrorLogger, c.NumUploaders, c.MaxQueuedUploads)
		if err != nil {
			return err
		}

		c.ProxyBackend = proxyCache
		return nil
	}

	if c.S3CloudStorage != nil {
		creds, err := c.S3CloudStorage.GetCredentials()
		if err != nil {
			return err
		}

		bucketLookupType, err := parseBucketLookupType(c.S3CloudStorage.BucketLookupType)
		if err != nil {
			return err
		}
		c.ProxyBackend = s3proxy.New(
			c.S3CloudStorage.Endpoint,
			c.S3CloudStorage.Bucket,
			bucketLookupType,
			c.S3CloudStorage.Prefix,
			creds,
			c.S3CloudStorage.DisableSSL,
			c.S3CloudStorage.UpdateTimestamps,
			c.S3CloudStorage.Region,
			c.StorageMode, c.AccessLogger, c.ErrorLogger, c.NumUploaders, c.MaxQueuedUploads)
		return nil
	}

	if c.AzBlobConfig != nil {
		creds, err := c.AzBlobConfig.GetCredentials()
		if err != nil {
			return err
		}

		c.ProxyBackend = azblobproxy.New(
			c.AzBlobConfig.StorageAccount,
			c.AzBlobConfig.ContainerName,
			c.AzBlobConfig.Prefix,
			creds,
			c.AzBlobConfig.SharedKey,
			c.AzBlobConfig.UpdateTimestamps,
			c.StorageMode, c.AccessLogger, c.ErrorLogger, c.NumUploaders, c.MaxQueuedUploads,
		)
		return nil
	}

	return nil
}

func parseBucketLookupType(typeStr string) (minio.BucketLookupType, error) {
	valMap := map[string]minio.BucketLookupType{
		"auto": minio.BucketLookupAuto,
		"dns":  minio.BucketLookupDNS,
		"path": minio.BucketLookupPath,
	}

	val, ok := valMap[typeStr]
	if !ok {
		return 0, fmt.Errorf("Unsupported value: %s", typeStr)
	}

	return val, nil
}
