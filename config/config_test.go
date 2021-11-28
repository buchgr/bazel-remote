package config

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

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
enable_ac_key_instance_mangling: true
enable_endpoint_metrics: true
experimental_remote_asset_api: true
http_read_timeout: 5s
http_write_timeout: 10s
access_log_level: none
`

	config, err := newFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		Host:                        "localhost",
		Port:                        8080,
		GRPCPort:                    9092,
		Dir:                         "/opt/cache-dir",
		MaxSize:                     100,
		StorageMode:                 "zstd",
		HtpasswdFile:                "/opt/.htpasswd",
		TLSCertFile:                 "/opt/tls.cert",
		TLSKeyFile:                  "/opt/tls.key",
		DisableHTTPACValidation:     true,
		EnableACKeyInstanceMangling: true,
		EnableEndpointMetrics:       true,
		ExperimentalRemoteAssetAPI:  true,
		HTTPReadTimeout:             5 * time.Second,
		HTTPWriteTimeout:            10 * time.Second,
		NumUploaders:                100,
		MaxQueuedUploads:            1000000,
		MaxBlobSize:                 math.MaxInt64,
		MetricsDurationBuckets:      []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320},
		AccessLogLevel:              "none",
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
		Host:        "localhost",
		Port:        8080,
		GRPCPort:    9092,
		Dir:         "/opt/cache-dir",
		MaxSize:     100,
		StorageMode: "zstd",
		GoogleCloudStorage: &GoogleCloudStorageConfig{
			Bucket:                "gcs-bucket",
			UseDefaultCredentials: false,
			JSONCredentialsFile:   "/opt/creds.json",
		},
		NumUploaders:           100,
		MaxQueuedUploads:       1000000,
		MaxBlobSize:            math.MaxInt64,
		MetricsDurationBuckets: []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320},
		AccessLogLevel:         "all",
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
  mode: zstd
`
	config, err := newFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		Host:        "localhost",
		Port:        8080,
		GRPCPort:    9092,
		Dir:         "/opt/cache-dir",
		MaxSize:     100,
		StorageMode: "zstd",
		HTTPBackend: &HTTPBackendConfig{
			BaseURL: "https://remote-cache.com:8080/cache",
		},
		NumUploaders:           100,
		MaxQueuedUploads:       1000000,
		MaxBlobSize:            math.MaxInt64,
		MetricsDurationBuckets: []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320},
		AccessLogLevel:         "all",
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
  auth_method: access_key
  access_key_id: EXAMPLE_ACCESS_KEY
  secret_access_key: EXAMPLE_SECRET_KEY
`
	config, err := newFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		Host:        "localhost",
		Port:        8080,
		Dir:         "/opt/cache-dir",
		MaxSize:     100,
		StorageMode: "zstd",
		S3CloudStorage: &S3CloudStorageConfig{
			Endpoint:        "minio.example.com:9000",
			Bucket:          "test-bucket",
			Prefix:          "test-prefix",
			AuthMethod:      "access_key",
			AccessKeyID:     "EXAMPLE_ACCESS_KEY",
			SecretAccessKey: "EXAMPLE_SECRET_KEY",
		},
		NumUploaders:           100,
		MaxQueuedUploads:       1000000,
		MaxBlobSize:            math.MaxInt64,
		MetricsDurationBuckets: []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320},
		AccessLogLevel:         "all",
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
		Host:                   "localhost",
		Port:                   1234,
		Dir:                    "/opt/cache-dir",
		MaxSize:                42,
		StorageMode:            "zstd",
		ProfilePort:            7070,
		ProfileHost:            "",
		NumUploaders:           100,
		MaxQueuedUploads:       1000000,
		MaxBlobSize:            math.MaxInt64,
		MetricsDurationBuckets: []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320},
		AccessLogLevel:         "all",
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

func TestValidMetricsDurationBuckets(t *testing.T) {
	yaml := `host: localhost
port: 1234
dir: /opt/cache-dir
max_size: 42
storage_mode: zstd
endpoint_metrics_duration_buckets: [.005, .1, 5]
`
	config, err := newFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		Host:                   "localhost",
		Port:                   1234,
		Dir:                    "/opt/cache-dir",
		MaxSize:                42,
		StorageMode:            "zstd",
		NumUploaders:           100,
		MaxQueuedUploads:       1000000,
		MaxBlobSize:            math.MaxInt64,
		MetricsDurationBuckets: []float64{0.005, 0.1, 5},
		AccessLogLevel:         "all",
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}

func TestMetricsDurationBucketsNoDuplicates(t *testing.T) {
	testConfig := &Config{
		Host:                   "localhost",
		Port:                   8080,
		MaxSize:                42,
		MaxBlobSize:            200,
		Dir:                    "/opt/cache-dir",
		StorageMode:            "uncompressed",
		MetricsDurationBuckets: []float64{1, 2, 3, 3},
	}
	err := validateConfig(testConfig)
	if err == nil {
		t.Fatal("Expected an error because 'endpoint_metrics_duration_buckets' contained a duplicate")
	}
	if !strings.Contains(err.Error(), "'endpoint_metrics_duration_buckets'") {
		t.Fatalf("Expected the error message to mention the invalid 'endpoint_metrics_duration_buckets' key. Got '%s'", err.Error())
	}
}

func TestStorageModes(t *testing.T) {
	tests := []struct {
		yaml     string
		expected string
		invalid  bool
	}{{
		yaml: `host: localhost
port: 1234
dir: /foo/bar 
max_size: 20
`,
		expected: "zstd",
	},
		{
			yaml: `host: localhost
port: 1234
dir: /foo/bar 
max_size: 20
storage_mode: zstd
`,
			expected: "zstd",
		},
		{
			yaml: `host: localhost
port: 1234
dir: /foo/bar 
max_size: 20
storage_mode: uncompressed
`,
			expected: "uncompressed",
		},
		{
			yaml: `host: localhost
port: 1234
dir: /foo/bar 
max_size: 20
storage_mode: gzip
`,
			invalid: true,
		}}

	for _, tc := range tests {
		cfg, err := newFromYaml([]byte(tc.yaml))
		if !tc.invalid && err != nil {
			t.Error("Expected to succeed, got", err)
		}

		if tc.invalid {
			if err == nil {
				t.Error("Expected an error, got nil")
			}
			continue
		}

		if cfg.StorageMode != tc.expected {
			t.Errorf("Expected %q, got %q", tc.expected, cfg.StorageMode)
		}
	}
}

func TestValidSocketOverride(t *testing.T) {
	yaml := `socket: /tmp/http.sock
grpc_socket: /tmp/grpc.sock
dir: /opt/cache-dir
max_size: 42
storage_mode: zstd
`
	config, err := newFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		Socket:                 "/tmp/http.sock",
		GRPCSocket:             "/tmp/grpc.sock",
		Dir:                    "/opt/cache-dir",
		MaxSize:                42,
		StorageMode:            "zstd",
		NumUploaders:           100,
		MaxQueuedUploads:       1000000,
		MaxBlobSize:            math.MaxInt64,
		MetricsDurationBuckets: []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320},
		AccessLogLevel:         "all",
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}
