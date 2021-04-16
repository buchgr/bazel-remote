package config

import (
	"log"
	"net/http"
	"net/url"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/gcsproxy"
	"github.com/buchgr/bazel-remote/cache/httpproxy"
	"github.com/buchgr/bazel-remote/cache/s3proxy"
)

func (c *Config) GetProxy(accessLogger *log.Logger, errorLogger *log.Logger) (cache.Proxy, error) {
	if c.GoogleCloudStorage != nil {
		proxyCache, err := gcsproxy.New(c.GoogleCloudStorage.Bucket,
			c.GoogleCloudStorage.UseDefaultCredentials, c.GoogleCloudStorage.JSONCredentialsFile,
			c.StorageMode, accessLogger, errorLogger, c.NumUploaders, c.MaxQueuedUploads)
		if err != nil {
			return nil, err
		}

		return proxyCache, nil
	}

	if c.HTTPBackend != nil {
		httpClient := &http.Client{}
		var baseURL *url.URL
		baseURL, err := url.Parse(c.HTTPBackend.BaseURL)
		if err != nil {
			return nil, err
		}
		proxyCache, err := httpproxy.New(baseURL, c.StorageMode,
			httpClient, accessLogger, errorLogger, c.NumUploaders, c.MaxQueuedUploads)
		if err != nil {
			return nil, err
		}

		return proxyCache, nil
	}

	if c.S3CloudStorage != nil {
		proxyCache := s3proxy.New(
			c.S3CloudStorage.Endpoint,
			c.S3CloudStorage.Bucket,
			c.S3CloudStorage.Prefix,
			c.S3CloudStorage.AccessKeyID,
			c.S3CloudStorage.SecretAccessKey,
			c.S3CloudStorage.DisableSSL,
			c.S3CloudStorage.IAMRoleEndpoint,
			c.S3CloudStorage.Region,
			c.StorageMode, accessLogger, errorLogger, c.NumUploaders, c.MaxQueuedUploads)
		return proxyCache, nil
	}

	return nil, nil
}
