package config

import (
	"math"
	"net/url"
	"os"
	"reflect"
	"regexp"
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
log_timezone: local
`

	config, err := NewFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		HTTPAddress:                 "localhost:8080",
		GRPCAddress:                 "localhost:9092",
		Dir:                         "/opt/cache-dir",
		MaxSize:                     100,
		StorageMode:                 "zstd",
		ZstdImplementation:          "go",
		HtpasswdFile:                "/opt/.htpasswd",
		MinTLSVersion:               "1.0",
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
		MaxProxyBlobSize:            math.MaxInt64,
		MetricsDurationBuckets:      []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320},
		AccessLogLevel:              "none",
		LogTimezone:                 "local",
		Otel:                        nil,
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
	config, err := NewFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		HTTPAddress:        "localhost:8080",
		GRPCAddress:        "localhost:9092",
		Dir:                "/opt/cache-dir",
		MaxSize:            100,
		StorageMode:        "zstd",
		ZstdImplementation: "go",
		GoogleCloudStorage: &GoogleCloudStorageConfig{
			Bucket:                "gcs-bucket",
			UseDefaultCredentials: false,
			JSONCredentialsFile:   "/opt/creds.json",
		},
		NumUploaders:           100,
		MinTLSVersion:          "1.0",
		MaxQueuedUploads:       1000000,
		MaxBlobSize:            math.MaxInt64,
		MaxProxyBlobSize:       math.MaxInt64,
		MetricsDurationBuckets: []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320},
		AccessLogLevel:         "all",
		LogTimezone:            "UTC",
		Otel:                   nil,
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
	config, err := NewFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	url, err := url.Parse("https://remote-cache.com:8080/cache")
	if err != nil {
		t.Fatal(err)
	}
	expectedConfig := &Config{
		HTTPAddress:        "localhost:8080",
		GRPCAddress:        "localhost:9092",
		Dir:                "/opt/cache-dir",
		MaxSize:            100,
		StorageMode:        "zstd",
		ZstdImplementation: "go",
		HTTPBackend: &URLBackendConfig{
			BaseURL: url,
		},
		NumUploaders:           100,
		MinTLSVersion:          "1.0",
		MaxQueuedUploads:       1000000,
		MaxBlobSize:            math.MaxInt64,
		MaxProxyBlobSize:       math.MaxInt64,
		MetricsDurationBuckets: []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320},
		AccessLogLevel:         "all",
		LogTimezone:            "UTC",
		Otel:                   nil,
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}

func TestDirRequired(t *testing.T) {
	testConfig := &Config{
		HTTPAddress: "localhost:8080",
		GRPCAddress: "localhost:9092",
		MaxSize:     100,
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
		HTTPAddress: "localhost:8080",
		GRPCAddress: "localhost:9092",
		Dir:         "/opt/cache-dir",
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
	config, err := NewFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		HTTPAddress:        "localhost:8080",
		Dir:                "/opt/cache-dir",
		MaxSize:            100,
		StorageMode:        "zstd",
		ZstdImplementation: "go",
		S3CloudStorage: &S3CloudStorageConfig{
			Endpoint:        "minio.example.com:9000",
			Bucket:          "test-bucket",
			Prefix:          "test-prefix",
			AuthMethod:      "access_key",
			AccessKeyID:     "EXAMPLE_ACCESS_KEY",
			SecretAccessKey: "EXAMPLE_SECRET_KEY",
		},
		NumUploaders:           100,
		MinTLSVersion:          "1.0",
		MaxQueuedUploads:       1000000,
		MaxBlobSize:            math.MaxInt64,
		MaxProxyBlobSize:       math.MaxInt64,
		MetricsDurationBuckets: []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320},
		AccessLogLevel:         "all",
		LogTimezone:            "UTC",
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}

func TestValidLDAPConfig(t *testing.T) {
	yaml := `host: localhost
port: 8080
dir: /opt/cache-dir
max_size: 100
ldap:
  url: ldap://ldap.example.com
  base_dn: OU=My Users,DC=example,DC=com
  username_attribute: sAMAccountName
  bind_user: ldapuser
  bind_password: ldappassword
  cache_time: 3600s
  groups_query: (|(memberOf=CN=bazel-users,OU=Groups,OU=My Users,DC=example,DC=com)(memberOf=CN=other-users,OU=Groups2,OU=Alien Users,DC=foo,DC=org))
`
	config, err := NewFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		HTTPAddress:        "localhost:8080",
		Dir:                "/opt/cache-dir",
		MaxSize:            100,
		StorageMode:        "zstd",
		ZstdImplementation: "go",
		LDAP: &LDAPConfig{
			URL:               "ldap://ldap.example.com",
			BaseDN:            "OU=My Users,DC=example,DC=com",
			BindUser:          "ldapuser",
			BindPassword:      "ldappassword",
			UsernameAttribute: "sAMAccountName",
			GroupsQuery:       "(|(memberOf=CN=bazel-users,OU=Groups,OU=My Users,DC=example,DC=com)(memberOf=CN=other-users,OU=Groups2,OU=Alien Users,DC=foo,DC=org))",
			CacheTime:         3600 * time.Second,
		},
		NumUploaders:           100,
		MinTLSVersion:          "1.0",
		MaxQueuedUploads:       1000000,
		MaxBlobSize:            math.MaxInt64,
		MaxProxyBlobSize:       math.MaxInt64,
		MetricsDurationBuckets: []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320},
		AccessLogLevel:         "all",
		LogTimezone:            "UTC",
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
profile_address: :7070
`
	config, err := NewFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		HTTPAddress:            "localhost:1234",
		Dir:                    "/opt/cache-dir",
		MaxSize:                42,
		StorageMode:            "zstd",
		ZstdImplementation:     "go",
		ProfileAddress:         ":7070",
		NumUploaders:           100,
		MinTLSVersion:          "1.0",
		MaxQueuedUploads:       1000000,
		MaxBlobSize:            math.MaxInt64,
		MaxProxyBlobSize:       math.MaxInt64,
		MetricsDurationBuckets: []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320},
		AccessLogLevel:         "all",
		LogTimezone:            "UTC",
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}

	re := regexp.MustCompile("profile_address: .*")
	yaml = re.ReplaceAllString(yaml, "profile_address: \"192.168.1.1:7070\"")

	expectedConfig.ProfileAddress = "192.168.1.1:7070"

	config, err = NewFromYaml([]byte(yaml))
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
	config, err := NewFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		HTTPAddress:            "localhost:1234",
		Dir:                    "/opt/cache-dir",
		MaxSize:                42,
		StorageMode:            "zstd",
		ZstdImplementation:     "go",
		MinTLSVersion:          "1.0",
		NumUploaders:           100,
		MaxQueuedUploads:       1000000,
		MaxBlobSize:            math.MaxInt64,
		MaxProxyBlobSize:       math.MaxInt64,
		MetricsDurationBuckets: []float64{0.005, 0.1, 5},
		AccessLogLevel:         "all",
		LogTimezone:            "UTC",
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}

func TestMetricsDurationBucketsNoDuplicates(t *testing.T) {
	testConfig := &Config{
		HTTPAddress:            "localhost:8080",
		MaxSize:                42,
		MaxBlobSize:            200,
		MaxProxyBlobSize:       math.MaxInt64,
		Dir:                    "/opt/cache-dir",
		StorageMode:            "uncompressed",
		ZstdImplementation:     "go",
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
		cfg, err := NewFromYaml([]byte(tc.yaml))
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

func TestHttpGrpcServerPortConflict(t *testing.T) {
	testConfig := &Config{
		HTTPAddress:        ":5000",
		GRPCAddress:        ":5000",
		Dir:                "/opt/cache-dir",
		MaxSize:            100,
		StorageMode:        "zstd",
		ZstdImplementation: "go",
	}
	err := validateConfig(testConfig)
	if err == nil {
		t.Fatal("Expected an error because 'http_address' and 'grpc_address' have conflicting TCP ports")
	}
	if !strings.Contains(err.Error(), "5000") {
		t.Fatal("Expected the error message to mention the conflicting port '5000'")
	}
}

func TestValidListenerAddress(t *testing.T) {
	yaml := `http_address: localhost:1234
grpc_address: localhost:5678
dir: /opt/cache-dir
max_size: 42
storage_mode: zstd
`
	config, err := NewFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		HTTPAddress:            "localhost:1234",
		GRPCAddress:            "localhost:5678",
		Dir:                    "/opt/cache-dir",
		MaxSize:                42,
		StorageMode:            "zstd",
		ZstdImplementation:     "go",
		NumUploaders:           100,
		MinTLSVersion:          "1.0",
		MaxQueuedUploads:       1000000,
		MaxBlobSize:            math.MaxInt64,
		MaxProxyBlobSize:       math.MaxInt64,
		MetricsDurationBuckets: []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320},
		AccessLogLevel:         "all",
		LogTimezone:            "UTC",
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}

func TestValidSocketOverride(t *testing.T) {
	yaml := `http_address: unix:///tmp/http.sock
grpc_address: unix:///tmp/grpc.sock
dir: /opt/cache-dir
max_size: 42
storage_mode: zstd
`
	config, err := NewFromYaml([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	expectedConfig := &Config{
		HTTPAddress:            "unix:///tmp/http.sock",
		GRPCAddress:            "unix:///tmp/grpc.sock",
		Dir:                    "/opt/cache-dir",
		MaxSize:                42,
		StorageMode:            "zstd",
		ZstdImplementation:     "go",
		NumUploaders:           100,
		MinTLSVersion:          "1.0",
		MaxQueuedUploads:       1000000,
		MaxBlobSize:            math.MaxInt64,
		MaxProxyBlobSize:       math.MaxInt64,
		MetricsDurationBuckets: []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320},
		AccessLogLevel:         "all",
		LogTimezone:            "UTC",
	}

	if !cmp.Equal(config, expectedConfig) {
		t.Fatalf("Expected '%+v' but got '%+v'", expectedConfig, config)
	}
}

func TestSocketPathMissing(t *testing.T) {
	testConfig := &Config{
		HTTPAddress:        "unix://",
		Dir:                "/opt/cache-dir",
		MaxSize:            100,
		StorageMode:        "zstd",
		ZstdImplementation: "go",
	}
	err := validateConfig(testConfig)
	if err == nil {
		t.Fatal("Expected an error because 'http_address' specifies an invalid Unix socket")
	}
	if !strings.Contains(err.Error(), "'http_address'") {
		t.Fatal("Expected the error message to mention the missing 'http_address' key/flag")
	}
}

func TestOtelConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		yaml        string
		envVars     map[string]string
		wantErr     bool
		errContains string
		checkFunc   func(*testing.T, *Config)
	}{
		{
			name: "valid config with all fields",
			yaml: `dir: /tmp/test
max_size: 100
otel:
  tracing:
    enabled: true
    exporter_endpoint: localhost:4317
    service_name: test-service
    sample_rate: 0.5
`,
			wantErr: false,
			checkFunc: func(t *testing.T, c *Config) {
				if c.Otel == nil || !c.Otel.Tracing.Enabled {
					t.Error("Expected OTEL to be enabled")
				}
				if c.Otel.Tracing.ExporterEndpoint != "localhost:4317" {
					t.Errorf("Expected endpoint localhost:4317, got %s", c.Otel.Tracing.ExporterEndpoint)
				}
				if c.Otel.Tracing.ServiceName != "test-service" {
					t.Errorf("Expected service name test-service, got %s", c.Otel.Tracing.ServiceName)
				}
				if c.Otel.Tracing.SampleRate != 0.5 {
					t.Errorf("Expected sample rate 0.5, got %f", c.Otel.Tracing.SampleRate)
				}
			},
		},
		{
			name: "valid config with defaults",
			yaml: `dir: /tmp/test
max_size: 100
otel:
  tracing:
    enabled: true
    exporter_endpoint: localhost:4317
`,
			wantErr: false,
			checkFunc: func(t *testing.T, c *Config) {
				if c.Otel == nil || !c.Otel.Tracing.Enabled {
					t.Error("Expected OTEL to be enabled")
				}
				if c.Otel.Tracing.ServiceName != "bazel-remote" {
					t.Errorf("Expected default service name bazel-remote, got %s", c.Otel.Tracing.ServiceName)
				}
				if c.Otel.Tracing.SampleRate != 1.0 {
					t.Errorf("Expected default sample rate 1.0, got %f", c.Otel.Tracing.SampleRate)
				}
			},
		},
		{
			name: "missing endpoint with env var",
			yaml: `dir: /tmp/test
max_size: 100
otel:
  tracing:
    enabled: true
`,
			envVars: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": "env-endpoint:4317",
			},
			wantErr: false,
			checkFunc: func(t *testing.T, c *Config) {
				if c.Otel.Tracing.ExporterEndpoint != "env-endpoint:4317" {
					t.Errorf("Expected endpoint from env var, got %s", c.Otel.Tracing.ExporterEndpoint)
				}
			},
		},
		{
			name: "missing endpoint without env var",
			yaml: `dir: /tmp/test
max_size: 100
otel:
  tracing:
    enabled: true
`,
			wantErr:     true,
			errContains: "exporter_endpoint",
		},
		{
			name: "invalid sample rate negative",
			yaml: `dir: /tmp/test
max_size: 100
otel:
  tracing:
    enabled: true
    exporter_endpoint: localhost:4317
    sample_rate: -0.1
`,
			wantErr:     true,
			errContains: "sample_rate",
		},
		{
			name: "invalid sample rate greater than 1",
			yaml: `dir: /tmp/test
max_size: 100
otel:
  tracing:
    enabled: true
    exporter_endpoint: localhost:4317
    sample_rate: 1.5
`,
			wantErr:     true,
			errContains: "sample_rate",
		},
		{
			name: "disabled no validation",
			yaml: `dir: /tmp/test
max_size: 100
otel:
  tracing:
    enabled: false
`,
			wantErr: false,
		},
		{
			name: "no otel config",
			yaml: `dir: /tmp/test
max_size: 100
`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment variables
			cleanup := make([]func(), 0)
			for k, v := range tt.envVars {
				oldVal := os.Getenv(k)
				os.Setenv(k, v)
				cleanup = append(cleanup, func() {
					if oldVal != "" {
						os.Setenv(k, oldVal)
					} else {
						os.Unsetenv(k)
					}
				})
			}
			defer func() {
				for _, fn := range cleanup {
					fn()
				}
			}()

			// Clear env vars that might interfere
			if len(tt.envVars) == 0 {
				oldEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
				oldTracesEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
				oldServiceName := os.Getenv("OTEL_SERVICE_NAME")
				os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")
				os.Unsetenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
				os.Unsetenv("OTEL_SERVICE_NAME")
				defer func() {
					if oldEndpoint != "" {
						os.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", oldEndpoint)
					}
					if oldTracesEndpoint != "" {
						os.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", oldTracesEndpoint)
					}
					if oldServiceName != "" {
						os.Setenv("OTEL_SERVICE_NAME", oldServiceName)
					}
				}()
			}

			config, err := NewFromYaml([]byte(tt.yaml))
			if (err != nil) != tt.wantErr {
				t.Errorf("NewFromYaml() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && tt.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("NewFromYaml() error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			if !tt.wantErr && tt.checkFunc != nil {
				tt.checkFunc(t, config)
			}
		})
	}
}

func TestOtelDisabledByDefault(t *testing.T) {
	// Test that OTEL is disabled when no otel config is specified
	yaml := `dir: /tmp/test
max_size: 100
`
	config, err := NewFromYaml([]byte(yaml))
	if err != nil {
		t.Fatalf("NewFromYaml() failed: %v", err)
	}

	// Otel should be nil when not specified
	if config.Otel != nil {
		t.Errorf("Expected Otel to be nil when not specified, got %+v", config.Otel)
	}
}

func TestOtelDisabledWhenEnabledIsFalse(t *testing.T) {
	// Test that OTEL is disabled when explicitly set to false
	yaml := `dir: /tmp/test
max_size: 100
otel:
  tracing:
    enabled: false
    exporter_endpoint: localhost:4317
`
	config, err := NewFromYaml([]byte(yaml))
	if err != nil {
		t.Fatalf("NewFromYaml() failed: %v", err)
	}

	// Otel should exist but be disabled
	if config.Otel == nil {
		t.Fatal("Expected Otel config to exist")
	}
	if config.Otel.Tracing == nil {
		t.Fatal("Expected Otel.Tracing config to exist")
	}
	if config.Otel.Tracing.Enabled {
		t.Error("Expected Otel.Tracing.Enabled to be false")
	}
}
