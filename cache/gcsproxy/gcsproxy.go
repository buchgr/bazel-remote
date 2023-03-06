// Package gcsproxy provides cache implementation that proxies requests
// to/from Google Cloud Storage (GCS).
package gcsproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/httpproxy"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// New creates a cache that proxies requests to Google Cloud Storage.
func New(bucket string, useDefaultCredentials bool, jsonCredentialsFile string, storageMode string,
	accessLogger cache.Logger, errorLogger cache.Logger, numUploaders, maxQueuedUploads int) (cache.Proxy, error) {
	var remoteClient *http.Client
	var err error

	if useDefaultCredentials {
		remoteClient, err = google.DefaultClient(context.Background(),
			"https://www.googleapis.com/auth/devstorage.read_write")
		if err != nil {
			return nil, err
		}
	} else if jsonCredentialsFile != "" {
		jsonConfig, err := os.ReadFile(jsonCredentialsFile)
		if err != nil {
			err = fmt.Errorf("Failed to read Google Credentials file '%s': %v", jsonCredentialsFile, err)
			return nil, err
		}
		config, err := google.CredentialsFromJSON(context.Background(), jsonConfig,
			"https://www.googleapis.com/auth/devstorage.read_write")
		if err != nil {
			err = fmt.Errorf("The provided Google Credentials file '%s' couldn't be parsed: %v",
				jsonCredentialsFile, err)
			return nil, err
		}
		remoteClient = oauth2.NewClient(context.Background(), config.TokenSource)
	} else {
		return nil, fmt.Errorf("For Google authentication one needs to specify one of default "+
			"credentials or a json credentials file %v", useDefaultCredentials)
	}

	errorLogger.Printf("Proxying artifacts to GCS bucket '%s'.\n", bucket)

	baseURL := url.URL{
		Scheme: "https",
		Host:   "storage.googleapis.com",
		Path:   bucket,
	}

	return httpproxy.New(&baseURL, storageMode, remoteClient, accessLogger, errorLogger, numUploaders, maxQueuedUploads)
}
