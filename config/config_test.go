package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestValidServerConfig(t *testing.T) {
	yaml := `host: localhost
port: 8080
dir: /opt/cache-dir
max_size: 100
htpasswd_file: /opt/.htpasswd
tls_cert_file: /opt/tls.cert
tls_key_file:  /opt/tls.key
`

	config, err := newFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		Host:         "localhost",
		Port:         8080,
		Dir:          "/opt/cache-dir",
		MaxSize:      100,
		HtpasswdFile: "/opt/.htpasswd",
		TLSCertFile:  "/opt/tls.cert",
		TLSKeyFile:   "/opt/tls.key",
	}

	if !reflect.DeepEqual(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}

func TestValidGCSProxyConfig(t *testing.T) {
	yaml := `host: localhost
port: 8080
dir: /opt/cache-dir
max_size: 100
gcs_proxy:
  bucket: gcs-bucket
  use_default_credentials: false
  json_credentials_file: /opt/creds.json
  disable_writes: true
`
	config, err := newFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		Host:    "localhost",
		Port:    8080,
		Dir:     "/opt/cache-dir",
		MaxSize: 100,
		GoogleCloudStorage: &GoogleCloudStorageConfig{
			Bucket:                "gcs-bucket",
			UseDefaultCredentials: false,
			JSONCredentialsFile:   "/opt/creds.json",
			DisableWrites:         true,
		},
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}

func TestValidHttpProxyConfig(t *testing.T) {
	yaml := `host: localhost
port: 8080
dir: /opt/cache-dir
max_size: 100
http_proxy:
  url: https://remote-cache.com:8080/cache
  disable_reads: true
`
	config, err := newFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		Host:    "localhost",
		Port:    8080,
		Dir:     "/opt/cache-dir",
		MaxSize: 100,
		HTTPBackend: &HTTPBackendConfig{
			BaseURL:      "https://remote-cache.com:8080/cache",
			DisableReads: true,
		},
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}

func TestDirRequired(t *testing.T) {
	testConfig := &Config{
		Host:    "localhost",
		Port:    8080,
		MaxSize: 100,
	}
	err := validateConfig(testConfig)
	if err == nil {
		t.Fatal("Expected an error because no 'dir' was specified")
	}
	if !strings.Contains(err.Error(), "'dir'") {
		t.Fatal("Expected the error message to mention the missing 'dir' key/flag")
	}
}

func TestMaxSizeRequired(t *testing.T) {
	testConfig := &Config{
		Host: "localhost",
		Port: 8080,
		Dir:  "/opt/cache-dir",
	}
	err := validateConfig(testConfig)
	if err == nil {
		t.Fatal("Expected an error because no 'max_size' was specified")
	}
	if !strings.Contains(err.Error(), "'max_size'") {
		t.Fatal("Expected the error message to mention the missing 'max_size' key/flag")
	}
}

func TestCannotDisableBothReadAndWriteInGCSProxyConfig(t *testing.T) {
	testConfig := &Config{
		Host:    "localhost",
		Port:    8080,
		Dir:     "/opt/cache-dir",
		MaxSize: 100,
		GoogleCloudStorage: &GoogleCloudStorageConfig{
			Bucket:                "gcs-bucket",
			UseDefaultCredentials: false,
			JSONCredentialsFile:   "/opt/creds.json",
			DisableReads:          true,
			DisableWrites:         true,
		},
	}
	err := validateConfig(testConfig)
	if err == nil {
		t.Fatal("Expected an error because both 'disable_reads' and 'disable_writes' were set")
	}
	if !strings.Contains(err.Error(), "'disable_reads'") || !strings.Contains(err.Error(), "'disable_writes'") {
		t.Fatal("Expected the error message to mention the conflicting 'disable_reads' and 'disable_writes' key/flag")
	}
}

func TestCannotDisableBothReadAndWriteInHttpProxyConfig(t *testing.T) {
	testConfig := &Config{
		Host:    "localhost",
		Port:    8080,
		Dir:     "/opt/cache-dir",
		MaxSize: 100,
		HTTPBackend: &HTTPBackendConfig{
			BaseURL:       "https://remote-cache.com:8080/cache",
			DisableReads:  true,
			DisableWrites: true,
		},
	}
	err := validateConfig(testConfig)
	if err == nil {
		t.Fatal("Expected an error because both 'disable_reads' and 'disable_writes' were set")
	}
	if !strings.Contains(err.Error(), "'disable_reads'") || !strings.Contains(err.Error(), "'disable_writes'") {
		t.Fatal("Expected the error message to mention the conflicting 'disable_reads' and 'disable_writes' key/flag")
	}
}
