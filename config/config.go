package config

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"time"

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

type LDAPConfig struct {
	BaseURL           string        `yaml:"url"`
	BaseDN            string        `yaml:"base_dn"`
	BindUser          string        `yaml:"bind_user"`
	BindPassword      string        `yaml:"bind_password"`
	UsernameAttribute string        `yaml:"username_attribute"`
	Groups            []string      `yaml:"groups,flow"`
	CacheTime         time.Duration `yaml:"cache_time"`
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
	LDAP               *LDAPConfig               `yaml:"ldap"`
	IdleTimeout        time.Duration             `yaml:"idle_timeout"`
}

// New ...
func New(dir string, maxSize int, host string, port int, htpasswdFile string,
	tlsCertFile string, tlsKeyFile string, idleTimeout time.Duration) (*Config, error) {
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
		LDAP:               nil,
		IdleTimeout:        idleTimeout,
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

	if c.MaxSize <= 0 {
		return errors.New("The 'max_size' flag/key must be set to a value > 0")
	}

	if c.Port == 0 {
		return errors.New("A valid 'port' flag/key must be specified")
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

	if c.HtpasswdFile != "" && c.LDAP != nil {
		return errors.New("One can specify at most one authentication mechanism")
	}

	if c.LDAP != nil {
		if c.LDAP.BaseURL == "" {
			return errors.New("The 'url' field is required for 'ldap'")
		}
		if c.LDAP.BaseDN == "" {
			return errors.New("The 'base_dn' field is required for 'ldap'")
		}
		if c.LDAP.BindUser == "" {
			return errors.New("The 'bind_user' field is required for 'ldap'")
		}
		if c.LDAP.BindPassword == "" {
			return errors.New("The 'bind_password' field is required for 'ldap'")
		}
		if c.LDAP.UsernameAttribute == "" {
			c.LDAP.UsernameAttribute = "uid"
		}
		if c.LDAP.CacheTime == 0 {
			c.LDAP.CacheTime = 1 * time.Hour
		}
	}
	return nil
}
