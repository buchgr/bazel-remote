// Package gcs provides cache implementation that proxies requests to/from
// Google Cloud Storage (GCS).
package gcs

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"

	"github.com/buchgr/bazel-remote/cache"
	cachehttp "github.com/buchgr/bazel-remote/cache/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// New creates a cache that proxies requests to Google Cloud Storage.
func New(bucket string, useDefaultCredentials bool, jsonCredentialsFile string,
	diskCache cache.Cache, accessLogger cache.Logger, errorLogger cache.Logger) (cache.Cache, error) {
	var remoteClient *http.Client
	var err error

	if useDefaultCredentials {
		remoteClient, err = google.DefaultClient(oauth2.NoContext,
			"https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			return nil, err
		}
	} else if jsonCredentialsFile != "" {
		jsonConfig, err := ioutil.ReadFile(jsonCredentialsFile)
		if err != nil {
			err = fmt.Errorf("Failed to read Google Credentials file '%s': %v", jsonCredentialsFile, err)
			return nil, err
		}
		config, err := google.CredentialsFromJSON(oauth2.NoContext, jsonConfig)
		if err != nil {
			err = fmt.Errorf("The provided Google Credentials file '%s' couldn't be parsed: %v",
				jsonCredentialsFile, err)
			return nil, err
		}
		remoteClient = oauth2.NewClient(oauth2.NoContext, config.TokenSource)
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

	return cachehttp.New(&baseURL, diskCache, remoteClient, accessLogger, errorLogger), nil
}
