package config

import (
	"net/http"
	"net/url"

	"github.com/buchgr/bazel-remote/cache/gcsproxy"
	"github.com/buchgr/bazel-remote/cache/httpproxy"
	"github.com/buchgr/bazel-remote/cache/s3proxy"
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

		c.ProxyBackend = s3proxy.New(
			c.S3CloudStorage.Endpoint,
			c.S3CloudStorage.Bucket,
			c.S3CloudStorage.Prefix,
			creds,
			c.S3CloudStorage.DisableSSL,
			c.S3CloudStorage.UpdateTimestamps,
			c.S3CloudStorage.Region,
			c.StorageMode, c.AccessLogger, c.ErrorLogger, c.NumUploaders, c.MaxQueuedUploads)
		return nil
	}

	return nil
}
