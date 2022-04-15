package config

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/s3proxy"

	"github.com/urfave/cli/v2"
	yaml "gopkg.in/yaml.v2"
)

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
	HTTPAddress                 string                    `yaml:"http_address"`
	GRPCAddress                 string                    `yaml:"grpc_address"`
	ProfileAddress              string                    `yaml:"profile_address"`
	Dir                         string                    `yaml:"dir"`
	MaxSize                     int                       `yaml:"max_size"`
	StorageMode                 string                    `yaml:"storage_mode"`
	HtpasswdFile                string                    `yaml:"htpasswd_file"`
	TLSCaFile                   string                    `yaml:"tls_ca_file"`
	TLSCertFile                 string                    `yaml:"tls_cert_file"`
	TLSKeyFile                  string                    `yaml:"tls_key_file"`
	AllowUnauthenticatedReads   bool                      `yaml:"allow_unauthenticated_reads"`
	S3CloudStorage              *S3CloudStorageConfig     `yaml:"s3_proxy,omitempty"`
	GoogleCloudStorage          *GoogleCloudStorageConfig `yaml:"gcs_proxy,omitempty"`
	HTTPBackend                 *HTTPBackendConfig        `yaml:"http_proxy,omitempty"`
	NumUploaders                int                       `yaml:"num_uploaders"`
	MaxQueuedUploads            int                       `yaml:"max_queued_uploads"`
	IdleTimeout                 time.Duration             `yaml:"idle_timeout"`
	DisableHTTPACValidation     bool                      `yaml:"disable_http_ac_validation"`
	DisableGRPCACDepsCheck      bool                      `yaml:"disable_grpc_ac_deps_check"`
	EnableACKeyInstanceMangling bool                      `yaml:"enable_ac_key_instance_mangling"`
	EnableEndpointMetrics       bool                      `yaml:"enable_endpoint_metrics"`
	MetricsDurationBuckets      []float64                 `yaml:"endpoint_metrics_duration_buckets"`
	ExperimentalRemoteAssetAPI  bool                      `yaml:"experimental_remote_asset_api"`
	HTTPReadTimeout             time.Duration             `yaml:"http_read_timeout"`
	HTTPWriteTimeout            time.Duration             `yaml:"http_write_timeout"`
	AccessLogLevel              string                    `yaml:"access_log_level"`
	MaxBlobSize                 int64                     `yaml:"max_blob_size"`
	MaxProxyBlobSize            int64                     `yaml:"max_proxy_blob_size"`

	// Fields that are created by combinations of the flags above.
	ProxyBackend cache.Proxy
	TLSConfig    *tls.Config
	AccessLogger *log.Logger
	ErrorLogger  *log.Logger
}

type YamlConfig struct {
	Config `yaml:",inline"`

	// Deprecated fields, retained for backwards compatibility when
	// parsing config files.

	// If set, these will be used to populate *Address fields.
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	GRPCPort    int    `yaml:"grpc_port"`
	ProfileHost string `yaml:"profile_host"`
	ProfilePort int    `yaml:"profile_port"`
}

const disabledGRPCListener = "none"

var defaultDurationBuckets = []float64{.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320}

// newFromArgs returns a validated Config with the specified values, and
// an error if there were any problems with the validation.
func newFromArgs(dir string, maxSize int, storageMode string,
	httpAddress string, grpcAddress string,
	profileAddress string,
	htpasswdFile string,
	maxQueuedUploads int,
	numUploaders int,
	tlsCaFile string,
	tlsCertFile string,
	tlsKeyFile string,
	allowUnauthenticatedReads bool,
	idleTimeout time.Duration,
	hc *HTTPBackendConfig,
	gcs *GoogleCloudStorageConfig,
	s3 *S3CloudStorageConfig,
	disableHTTPACValidation bool,
	disableGRPCACDepsCheck bool,
	enableACKeyInstanceMangling bool,
	enableEndpointMetrics bool,
	experimentalRemoteAssetAPI bool,
	httpReadTimeout time.Duration,
	httpWriteTimeout time.Duration,
	accessLogLevel string,
	maxBlobSize int64,
	maxProxyBlobSize int64) (*Config, error) {

	c := Config{
		HTTPAddress:                 httpAddress,
		GRPCAddress:                 grpcAddress,
		ProfileAddress:              profileAddress,
		Dir:                         dir,
		MaxSize:                     maxSize,
		StorageMode:                 storageMode,
		HtpasswdFile:                htpasswdFile,
		MaxQueuedUploads:            maxQueuedUploads,
		NumUploaders:                numUploaders,
		TLSCaFile:                   tlsCaFile,
		TLSCertFile:                 tlsCertFile,
		TLSKeyFile:                  tlsKeyFile,
		AllowUnauthenticatedReads:   allowUnauthenticatedReads,
		S3CloudStorage:              s3,
		GoogleCloudStorage:          gcs,
		HTTPBackend:                 hc,
		IdleTimeout:                 idleTimeout,
		DisableHTTPACValidation:     disableHTTPACValidation,
		DisableGRPCACDepsCheck:      disableGRPCACDepsCheck,
		EnableACKeyInstanceMangling: enableACKeyInstanceMangling,
		EnableEndpointMetrics:       enableEndpointMetrics,
		MetricsDurationBuckets:      defaultDurationBuckets,
		ExperimentalRemoteAssetAPI:  experimentalRemoteAssetAPI,
		HTTPReadTimeout:             httpReadTimeout,
		HTTPWriteTimeout:            httpWriteTimeout,
		AccessLogLevel:              accessLogLevel,
		MaxBlobSize:                 maxBlobSize,
		MaxProxyBlobSize:            maxProxyBlobSize,
	}

	err := validateConfig(&c)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// newFromYamlFile reads configuration settings from a YAML file then returns
// a validated Config with those settings, and an error if there were any
// problems.
func newFromYamlFile(path string) (*Config, error) {
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
	yc := YamlConfig{
		Config: Config{
			StorageMode:            "zstd",
			NumUploaders:           100,
			MaxQueuedUploads:       1000000,
			MaxBlobSize:            math.MaxInt64,
			MaxProxyBlobSize:       math.MaxInt64,
			MetricsDurationBuckets: defaultDurationBuckets,
			AccessLogLevel:         "all",
		},
	}

	err := yaml.Unmarshal(data, &yc)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse YAML config: %v", err)
	}
	c := yc.Config

	if c.HTTPAddress == "" {
		c.HTTPAddress = net.JoinHostPort(yc.Host, strconv.Itoa(yc.Port))
	}

	if c.GRPCAddress == "" && yc.GRPCPort > 0 {
		c.GRPCAddress = net.JoinHostPort(yc.Host, strconv.Itoa(yc.GRPCPort))
	}

	if c.ProfileAddress == "" && yc.ProfilePort > 0 {
		c.ProfileAddress = net.JoinHostPort(yc.ProfileHost, strconv.Itoa(yc.ProfilePort))
	}

	if c.MetricsDurationBuckets != nil {
		sort.Float64s(c.MetricsDurationBuckets)
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

	if c.StorageMode != "zstd" && c.StorageMode != "uncompressed" {
		return errors.New("storage_mode must be set to either \"zstd\" or \"uncompressed\"")
	}

	proxyCount := 0
	if c.S3CloudStorage != nil {
		proxyCount++
	}
	if c.HTTPBackend != nil {
		proxyCount++
	}
	if c.GoogleCloudStorage != nil {
		proxyCount++
	}

	if proxyCount > 1 {
		return errors.New("At most one of the S3/GCS/HTTP proxy backends is allowed")
	}

	var httpPort string
	if strings.HasPrefix(c.HTTPAddress, "unix://") {
		if c.HTTPAddress[len("unix://"):] == "" {
			return errors.New("'http_address' Unix socket specification is missing a socket path")
		}
	} else {
		var err error
		_, httpPort, err = net.SplitHostPort(c.HTTPAddress)
		if err != nil {
			return errors.New("'http_address' must either be formatted as [host]:port or unix://socket.path")
		}
	}

	if c.GRPCAddress != "" && c.GRPCAddress != disabledGRPCListener {
		if strings.HasPrefix(c.GRPCAddress, "unix://") {
			if c.GRPCAddress[len("unix://"):] == "" {
				return errors.New("'grpc_address' Unix socket specification is missing a socket path")
			}
		} else {
			_, grpcPort, err := net.SplitHostPort(c.GRPCAddress)
			if err != nil {
				return errors.New("'grpc_address' must either be formatted as [host]:port or unix://socket.path")
			}

			if httpPort != "" && grpcPort != "" && httpPort == grpcPort {
				return fmt.Errorf("HTTP and gRPC server TCP ports conflict: %s", httpPort)
			}
		}
	}

	if c.ProfileAddress != "" && c.ProfileAddress != disabledGRPCListener {
		if strings.HasPrefix(c.ProfileAddress, "unix://") {
			if c.ProfileAddress[len("unix://"):] == "" {
				return errors.New("'profile_address' Unix socket specification is missing a socket path")
			}
		}
	}

	if c.GRPCAddress == disabledGRPCListener && c.ExperimentalRemoteAssetAPI {
		return errors.New("Remote Asset API support depends on gRPC being enabled")
	}

	if (c.TLSCertFile != "" && c.TLSKeyFile == "") || (c.TLSCertFile == "" && c.TLSKeyFile != "") {
		return errors.New("When enabling TLS one must specify both " +
			"'tls_key_file' and 'tls_cert_file'")
	}

	if c.TLSCaFile != "" && (c.TLSCertFile == "" || c.TLSKeyFile == "") {
		return errors.New("When enabling mTLS (authenticating client " +
			"certificates) the server must have it's own 'tls_key_file' " +
			"and 'tls_cert_file' specified.")
	}

	if c.AllowUnauthenticatedReads && c.TLSCaFile == "" && c.HtpasswdFile == "" {
		return errors.New("AllowUnauthenticatedReads setting is only available when authentication is enabled")
	}

	if c.MaxBlobSize <= 0 {
		return errors.New("The 'max_blob_size' flag/key must be a positive integer")
	}

	if c.MaxProxyBlobSize <= 0 {
		return errors.New("The 'max_proxy_blob_size' flag/key must be a positive integer")
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
		if !s3proxy.IsValidAuthMethod(c.S3CloudStorage.AuthMethod) {
			return fmt.Errorf("invalid s3.auth_method: %s", c.S3CloudStorage.AuthMethod)
		}

		if c.S3CloudStorage.KeyVersion != nil && *c.S3CloudStorage.KeyVersion != 2 {
			return fmt.Errorf("s3.key_version (deprecated) must be 2, found %d", c.S3CloudStorage.KeyVersion)
		}
	}

	if c.MetricsDurationBuckets != nil {
		duplicates := make(map[float64]bool)
		for _, bucket := range c.MetricsDurationBuckets {
			_, dupe := duplicates[bucket]
			if dupe {
				return errors.New("'endpoint_metrics_duration_buckets' must not contain duplicate buckets")
			}
			duplicates[bucket] = true
		}
	}

	switch c.AccessLogLevel {
	case "none", "all":
	default:
		return errors.New("'access_log_level' must be set to either \"none\" or \"all\"")
	}

	return nil
}

func Get(ctx *cli.Context) (*Config, error) {
	// Get a Config with all the basic fields set.
	cfg, err := get(ctx)
	if err != nil {
		return nil, err
	}

	// Set the non-basic fields...

	err = cfg.setLogger()
	if err != nil {
		return nil, err
	}

	err = cfg.setProxy()
	if err != nil {
		return nil, err
	}

	err = cfg.setTLSConfig()
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

// Return a Config with all the basic fields set.
func get(ctx *cli.Context) (*Config, error) {
	configFile := ctx.String("config_file")
	if configFile != "" {
		return newFromYamlFile(configFile)
	}

	httpAddress := ctx.String("http_address")
	if httpAddress == "" {
		httpAddress = net.JoinHostPort(ctx.String("host"), strconv.Itoa(ctx.Int("port")))
	}

	grpcAddress := ctx.String("grpc_address")
	if grpcAddress == "" && ctx.Int("grpc_port") > 0 {
		grpcAddress = net.JoinHostPort(ctx.String("host"), strconv.Itoa(ctx.Int("grpc_port")))
	}

	profileAddress := ctx.String("profile_address")
	if profileAddress == "" && ctx.Int("profile_port") > 0 {
		profileAddress = net.JoinHostPort(ctx.String("profile_host"), strconv.Itoa(ctx.Int("profile_port")))
	} else if profileAddress == "none" {
		profileAddress = ""
	}

	var s3 *S3CloudStorageConfig
	if ctx.String("s3.bucket") != "" {
		s3 = &S3CloudStorageConfig{
			Endpoint:                 ctx.String("s3.endpoint"),
			Bucket:                   ctx.String("s3.bucket"),
			Prefix:                   ctx.String("s3.prefix"),
			AuthMethod:               ctx.String("s3.auth_method"),
			AccessKeyID:              ctx.String("s3.access_key_id"),
			SecretAccessKey:          ctx.String("s3.secret_access_key"),
			DisableSSL:               ctx.Bool("s3.disable_ssl"),
			UpdateTimestamps:         ctx.Bool("s3.update_timestamps"),
			IAMRoleEndpoint:          ctx.String("s3.iam_role_endpoint"),
			Region:                   ctx.String("s3.region"),
			AWSProfile:               ctx.String("s3.aws_profile"),
			AWSSharedCredentialsFile: ctx.String("s3.aws_shared_credentials_file"),
		}
	}

	var hc *HTTPBackendConfig
	if ctx.String("http_proxy.url") != "" {
		hc = &HTTPBackendConfig{
			BaseURL: ctx.String("http_proxy.url"),
		}
	}

	var gcs *GoogleCloudStorageConfig
	if ctx.String("gcs_proxy.bucket") != "" {
		gcs = &GoogleCloudStorageConfig{
			Bucket:                ctx.String("gcs_proxy.bucket"),
			UseDefaultCredentials: ctx.Bool("gcs_proxy.use_default_credentials"),
			JSONCredentialsFile:   ctx.String("gcs_proxy.json_credentials_file"),
		}
	}

	return newFromArgs(
		ctx.String("dir"),
		ctx.Int("max_size"),
		ctx.String("storage_mode"),
		httpAddress,
		grpcAddress,
		profileAddress,
		ctx.String("htpasswd_file"),
		ctx.Int("max_queued_uploads"),
		ctx.Int("num_uploaders"),
		ctx.String("tls_ca_file"),
		ctx.String("tls_cert_file"),
		ctx.String("tls_key_file"),
		ctx.Bool("allow_unauthenticated_reads"),
		ctx.Duration("idle_timeout"),
		hc,
		gcs,
		s3,
		ctx.Bool("disable_http_ac_validation"),
		ctx.Bool("disable_grpc_ac_deps_check"),
		ctx.Bool("enable_ac_key_instance_mangling"),
		ctx.Bool("enable_endpoint_metrics"),
		ctx.Bool("experimental_remote_asset_api"),
		ctx.Duration("http_read_timeout"),
		ctx.Duration("http_write_timeout"),
		ctx.String("access_log_level"),
		ctx.Int64("max_blob_size"),
		ctx.Int64("max_proxy_blob_size"),
	)
}
