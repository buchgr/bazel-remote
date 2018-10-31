package config

import (
	"reflect"
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
			BaseURL: "https://remote-cache.com:8080/cache",
		},
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}

func TestValidS3CloudStorageConfig(t *testing.T) {
	yaml := `host: localhost
port: 8080
dir: /opt/cache-dir
max_size: 100
s3_proxy:
  endpoint: minio.example.com:9000
  bucket: test-bucket
  location: test-location
  access_key_id: EXAMPLE_ACCESS_KEY
  secret_access_key: EXAMPLE_SECRET_KEY
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
		S3CloudStorage: &S3CloudStorageConfig{
			Endpoint:        "minio.example.com:9000",
			Bucket:          "test-bucket",
			Location:        "test-location",
			AccessKeyID:     "EXAMPLE_ACCESS_KEY",
			SecretAccessKey: "EXAMPLE_SECRET_KEY",
		},
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}
