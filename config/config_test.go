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
grpc_port: 9092
dir: /opt/cache-dir
max_size: 100
htpasswd_file: /opt/.htpasswd
tls_cert_file: /opt/tls.cert
tls_key_file:  /opt/tls.key
disable_http_ac_validation: true
enable_endpoint_metrics: true
experimental_remote_asset_api: true
`

	config, err := newFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		Host:                       "localhost",
		Port:                       8080,
		GRPCPort:                   9092,
		Dir:                        "/opt/cache-dir",
		MaxSize:                    100,
		HtpasswdFile:               "/opt/.htpasswd",
		TLSCertFile:                "/opt/tls.cert",
		TLSKeyFile:                 "/opt/tls.key",
		DisableHTTPACValidation:    true,
		EnableEndpointMetrics:      true,
		ExperimentalRemoteAssetAPI: true,
	}

	if !reflect.DeepEqual(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}

func TestValidGCSProxyConfig(t *testing.T) {
	yaml := `host: localhost
port: 8080
grpc_port: 9092
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
		Host:     "localhost",
		Port:     8080,
		GRPCPort: 9092,
		Dir:      "/opt/cache-dir",
		MaxSize:  100,
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
grpc_port: 9092
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
		Host:     "localhost",
		Port:     8080,
		GRPCPort: 9092,
		Dir:      "/opt/cache-dir",
		MaxSize:  100,
		HTTPBackend: &HTTPBackendConfig{
			BaseURL: "https://remote-cache.com:8080/cache",
		},
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}

func TestDirRequired(t *testing.T) {
	testConfig := &Config{
		Host:     "localhost",
		Port:     8080,
		GRPCPort: 9092,
		MaxSize:  100,
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
		Host:     "localhost",
		Port:     8080,
		GRPCPort: 9092,
		Dir:      "/opt/cache-dir",
	}
	err := validateConfig(testConfig)
	if err == nil {
		t.Fatal("Expected an error because no 'max_size' was specified")
	}
	if !strings.Contains(err.Error(), "'max_size'") {
		t.Fatal("Expected the error message to mention the missing 'max_size' key/flag")
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
  prefix: test-prefix
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
			Prefix:          "test-prefix",
			AccessKeyID:     "EXAMPLE_ACCESS_KEY",
			SecretAccessKey: "EXAMPLE_SECRET_KEY",
		},
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}

func TestValidProfiling(t *testing.T) {
	yaml := `host: localhost
port: 1234
dir: /opt/cache-dir
max_size: 42
profile_port: 7070
`
	config, err := newFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		Host:        "localhost",
		Port:        1234,
		Dir:         "/opt/cache-dir",
		MaxSize:     42,
		ProfilePort: 7070,
		ProfileHost: "",
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}

	yaml += `
profile_host: 192.168.1.1`

	expectedConfig.ProfileHost = "192.168.1.1"

	config, err = newFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}
