package config

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	yaml "gopkg.in/yaml.v2"
)

// S3CloudStorageConfig stores the configuration of an S3 API proxy backend.
type S3CloudStorageConfig struct {
	Endpoint        string `yaml:"endpoint"`
	Bucket          string `yaml:"bucket"`
	Prefix          string `yaml:"prefix"`
	AccessKeyID     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
	DisableSSL      bool   `yaml:"disable_ssl"`
	IAMRoleEndpoint string `yaml:"iam_role_endpoint"`
	Region          string `yaml:"region"`
}

// GoogleCloudStorageConfig stores the configuration of a GCS proxy backend.
type GoogleCloudStorageConfig struct {
	Bucket                string `yaml:"bucket"`
	UseDefaultCredentials bool   `yaml:"use_default_credentials"`
	JSONCredentialsFile   string `yaml:"json_credentials_file"`
}

// HTTPBackendConfig stores the configuration for a HTTP proxy backend.
type HTTPBackendConfig struct {
	BaseURL string `yaml:"url"`
}

// Config holds the top-level configuration for bazel-remote.
type Config struct {
	Host                       string                    `yaml:"host"`
	Port                       int                       `yaml:"port"`
	GRPCPort                   int                       `yaml:"grpc_port"`
	ProfileHost                string                    `yaml:"profile_host"`
	ProfilePort                int                       `yaml:"profile_port"`
	Dir                        string                    `yaml:"dir"`
	MaxSize                    int                       `yaml:"max_size"`
	HtpasswdFile               string                    `yaml:"htpasswd_file"`
	TLSCertFile                string                    `yaml:"tls_cert_file"`
	TLSKeyFile                 string                    `yaml:"tls_key_file"`
	S3CloudStorage             *S3CloudStorageConfig     `yaml:"s3_proxy"`
	GoogleCloudStorage         *GoogleCloudStorageConfig `yaml:"gcs_proxy"`
	HTTPBackend                *HTTPBackendConfig        `yaml:"http_proxy"`
	IdleTimeout                time.Duration             `yaml:"idle_timeout"`
	DisableHTTPACValidation    bool                      `yaml:"disable_http_ac_validation"`
	DisableGRPCACDepsCheck     bool                      `yaml:"disable_grpc_ac_deps_check"`
	EnableEndpointMetrics      bool                      `yaml:"enable_endpoint_metrics"`
	ExperimentalRemoteAssetAPI bool                      `yaml:"experimental_remote_asset_api"`
	HTTPReadTimeout            time.Duration             `yaml:"http_read_timeout"`
	HTTPWriteTimeout           time.Duration             `yaml:"http_write_timeout"`
}

// New returns a validated Config with the specified values, and an error
// if there were any problems with the validation.
func New(dir string, maxSize int, host string, port int, grpcPort int,
	profileHost string, profilePort int, htpasswdFile string,
	tlsCertFile string, tlsKeyFile string, idleTimeout time.Duration,
	s3 *S3CloudStorageConfig, disableHTTPACValidation bool,
	disableGRPCACDepsCheck bool, enableEndpointMetrics bool,
	experimentalRemoteAssetAPI bool,
	httpReadTimeout time.Duration, httpWriteTimeout time.Duration) (*Config, error) {
	c := Config{
		Host:                       host,
		Port:                       port,
		GRPCPort:                   grpcPort,
		ProfileHost:                profileHost,
		ProfilePort:                profilePort,
		Dir:                        dir,
		MaxSize:                    maxSize,
		HtpasswdFile:               htpasswdFile,
		TLSCertFile:                tlsCertFile,
		TLSKeyFile:                 tlsKeyFile,
		S3CloudStorage:             s3,
		GoogleCloudStorage:         nil,
		HTTPBackend:                nil,
		IdleTimeout:                idleTimeout,
		DisableHTTPACValidation:    disableHTTPACValidation,
		DisableGRPCACDepsCheck:     disableGRPCACDepsCheck,
		EnableEndpointMetrics:      enableEndpointMetrics,
		ExperimentalRemoteAssetAPI: experimentalRemoteAssetAPI,
		HTTPReadTimeout:            httpReadTimeout,
		HTTPWriteTimeout:           httpWriteTimeout,
	}

	err := validateConfig(&c)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// NewFromYamlFile reads configuration settings from a YAML file then returns
// a validated Config with those settings, and an error if there were any
// problems.
func NewFromYamlFile(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("Failed to open config file '%s': %v", path, err)
	}
	defer file.Close()

	data, err := ioutil.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("Failed to read config file '%s': %v", path, err)
	}

	return newFromYaml(data)
}

func newFromYaml(data []byte) (*Config, error) {
	c := Config{}
	err := yaml.Unmarshal(data, &c)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse YAML config: %v", err)
	}

	err = validateConfig(&c)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func validateConfig(c *Config) error {
	if c.Dir == "" {
		return errors.New("The 'dir' flag/key is required")
	}

	if c.MaxSize <= 0 {
		return errors.New("The 'max_size' flag/key must be set to a value > 0")
	}

	if c.Port == 0 {
		return errors.New("A valid 'port' flag/key must be specified")
	}

	if c.GRPCPort < 0 {
		return errors.New("The 'grpc_port' flag/key must be 0 (disabled) or a positive integer")
	}

	if c.GRPCPort == 0 && c.ExperimentalRemoteAssetAPI {
		return errors.New("Remote Asset API support depends on gRPC being enabled")
	}

	if (c.TLSCertFile != "" && c.TLSKeyFile == "") || (c.TLSCertFile == "" && c.TLSKeyFile != "") {
		return errors.New("When enabling TLS one must specify both " +
			"'tls_key_file' and 'tls_cert_file'")
	}

	if c.GoogleCloudStorage != nil && c.HTTPBackend != nil && c.S3CloudStorage != nil {
		return errors.New("One can specify at most one proxying backend")
	}

	if c.GoogleCloudStorage != nil {
		if c.GoogleCloudStorage.Bucket == "" {
			return errors.New("The 'bucket' field is required for 'gcs_proxy'")
		}
	}

	if c.HTTPBackend != nil {
		if c.HTTPBackend.BaseURL == "" {
			return errors.New("The 'url' field is required for 'http_proxy'")
		}
	}

	if c.S3CloudStorage != nil {
		if c.S3CloudStorage.AccessKeyID != "" && c.S3CloudStorage.IAMRoleEndpoint != "" {
			return errors.New("Expected either 's3.access_key_id' or 's3.iam_role_endpoint', found both")
		}
	}

	return nil
}
