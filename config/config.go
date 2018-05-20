package config

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"

	yaml "gopkg.in/yaml.v2"
)

type GoogleCloudStorageConfig struct {
	Bucket                string `yaml:"bucket"`
	UseDefaultCredentials bool   `yaml:"use_default_credentials"`
	JSONCredentialsFile   string `yaml:"json_credentials_file"`
}

type HTTPBackendConfig struct {
	BaseURL string `yaml:"url"`
}

// Config provides the configuration
type Config struct {
	Host               string                    `yaml:"host"`
	Port               int                       `yaml:"port"`
	Dir                string                    `yaml:"dir"`
	MaxSize            int                       `yaml:"max_size"`
	HtpasswdFile       string                    `yaml:"htpasswd_file"`
	TLSCertFile        string                    `yaml:"tls_cert_file"`
	TLSKeyFile         string                    `yaml:"tls_key_file"`
	GoogleCloudStorage *GoogleCloudStorageConfig `yaml:"gcs_proxy"`
	HTTPBackend        *HTTPBackendConfig        `yaml:"http_proxy"`
}

// New ...
func New(dir string, maxSize int, host string, port int, htpasswdFile string,
	tlsCertFile string, tlsKeyFile string) (*Config, error) {
	c := Config{
		Host:               host,
		Port:               port,
		Dir:                dir,
		MaxSize:            maxSize,
		HtpasswdFile:       htpasswdFile,
		TLSCertFile:        tlsCertFile,
		TLSKeyFile:         tlsKeyFile,
		GoogleCloudStorage: nil,
		HTTPBackend:        nil,
	}

	err := validateConfig(&c)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// NewFromYamlFile ...
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

	if c.MaxSize == 0 {
		return errors.New("The 'max_size' flag/key is required")
	}

	if (c.TLSCertFile != "" && c.TLSKeyFile == "") || (c.TLSCertFile == "" && c.TLSKeyFile != "") {
		return errors.New("When enabling TLS one must specify both " +
			"'tls_key_file' and 'tls_cert_file'")
	}

	if c.GoogleCloudStorage != nil && c.HTTPBackend != nil {
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
	return nil
}
