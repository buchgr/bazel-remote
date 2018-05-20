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
