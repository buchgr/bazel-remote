package config

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/azblobproxy"
	"github.com/buchgr/bazel-remote/v2/cache/hashing"
	"github.com/buchgr/bazel-remote/v2/cache/s3proxy"
	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"

	"github.com/urfave/cli/v2"
	yaml "gopkg.in/yaml.v3"
)

// GoogleCloudStorageConfig stores the configuration of a GCS proxy backend.
type GoogleCloudStorageConfig struct {
	Bucket                string `yaml:"bucket"`
	UseDefaultCredentials bool   `yaml:"use_default_credentials"`
	JSONCredentialsFile   string `yaml:"json_credentials_file"`
}

// URLBackendConfig stores the configuration for a HTTP or GRPC proxy backend.
type URLBackendConfig struct {
	BaseURL  *url.URL `yaml:"url"`
	CertFile string   `yaml:"cert_file"`
	KeyFile  string   `yaml:"key_file"`
	CaFile   string   `yaml:"ca_file"`
}

func (c *URLBackendConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type Aux URLBackendConfig
	aux := &struct {
		URLStr string `yaml:"url"`
		*Aux
	}{
		Aux: (*Aux)(c),
	}

	if err := unmarshal(aux); err != nil {
		return err
	}
	u, err := url.Parse(aux.URLStr)
	if err != nil {
		return err
	}
	c.BaseURL = u
	return nil
}

func (c *URLBackendConfig) validate(protocol string) error {
	if c.BaseURL == nil {
		return fmt.Errorf("The 'url' field is required for '%s_proxy'", protocol)
	}
	if c.BaseURL.Scheme != protocol && c.BaseURL.Scheme != protocol+"s" {
		return fmt.Errorf("The %[1]s proxy backend protocol must be either %[1]s or %[1]ss", protocol)
	}
	if c.KeyFile != "" || c.CertFile != "" {
		if c.KeyFile == "" || c.CertFile == "" {
			return fmt.Errorf("To use mTLS with the %s proxy, both a key and a certificate must be provided", protocol)
		}
		if c.BaseURL.Scheme != protocol+"s" {
			return fmt.Errorf("When mTLS is enabled, the %[1]s proxy backend protocol must be %[1]ss", protocol)
		}
	}
	if c.CaFile != "" && c.BaseURL.Scheme != protocol+"s" {
		return fmt.Errorf("When TLS is enabled, the %[1]s proxy backend protocol must be %[1]s", protocol)
	}
	return nil
}

// Config holds the top-level configuration for bazel-remote.
type Config struct {
	HTTPAddress                 string                    `yaml:"http_address"`
	GRPCAddress                 string                    `yaml:"grpc_address"`
	ProfileAddress              string                    `yaml:"profile_address"`
	Dir                         string                    `yaml:"dir"`
	MaxSize                     int                       `yaml:"max_size"`
	StorageMode                 string                    `yaml:"storage_mode"`
	ZstdImplementation          string                    `yaml:"zstd_implementation"`
	HtpasswdFile                string                    `yaml:"htpasswd_file"`
	MinTLSVersion               string                    `yaml:"min_tls_version"`
	TLSCaFile                   string                    `yaml:"tls_ca_file"`
	TLSCertFile                 string                    `yaml:"tls_cert_file"`
	TLSKeyFile                  string                    `yaml:"tls_key_file"`
	AllowUnauthenticatedReads   bool                      `yaml:"allow_unauthenticated_reads"`
	S3CloudStorage              *S3CloudStorageConfig     `yaml:"s3_proxy,omitempty"`
	AzBlobConfig                *AzBlobStorageConfig      `yaml:"azblob_proxy,omitempty"`
	GoogleCloudStorage          *GoogleCloudStorageConfig `yaml:"gcs_proxy,omitempty"`
	HTTPBackend                 *URLBackendConfig         `yaml:"http_proxy,omitempty"`
	GRPCBackend                 *URLBackendConfig         `yaml:"grpc_proxy,omitempty"`
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
	LogTimezone                 string                    `yaml:"log_timezone"`
	MaxBlobSize                 int64                     `yaml:"max_blob_size"`
	MaxProxyBlobSize            int64                     `yaml:"max_proxy_blob_size"`
	DigestFunctions             []pb.DigestFunction_Value

	// Fields that are created by combinations of the flags above.
	ProxyBackend cache.Proxy
	TLSConfig    *tls.Config
	AccessLogger *log.Logger
	ErrorLogger  *log.Logger
}

type YamlConfig struct {
	Config `yaml:",inline"`

	// Complext types that are converted later
	DigestFunctionNames []string `yaml:"digest_functions"`

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
func newFromArgs(dir string, maxSize int, storageMode string, zstdImplementation string,
	httpAddress string, grpcAddress string,
	profileAddress string,
	htpasswdFile string,
	maxQueuedUploads int,
	numUploaders int,
	minTLSVersion string,
	tlsCaFile string,
	tlsCertFile string,
	tlsKeyFile string,
	allowUnauthenticatedReads bool,
	idleTimeout time.Duration,
	hc *URLBackendConfig,
	grpcb *URLBackendConfig,
	gcs *GoogleCloudStorageConfig,
	s3 *S3CloudStorageConfig,
	azblob *AzBlobStorageConfig,
	disableHTTPACValidation bool,
	disableGRPCACDepsCheck bool,
	enableACKeyInstanceMangling bool,
	enableEndpointMetrics bool,
	experimentalRemoteAssetAPI bool,
	httpReadTimeout time.Duration,
	httpWriteTimeout time.Duration,
	accessLogLevel string,
	logTimezone string,
	maxBlobSize int64,
	maxProxyBlobSize int64,
	digestFunctions []pb.DigestFunction_Value) (*Config, error) {

	c := Config{
		HTTPAddress:                 httpAddress,
		GRPCAddress:                 grpcAddress,
		ProfileAddress:              profileAddress,
		Dir:                         dir,
		MaxSize:                     maxSize,
		StorageMode:                 storageMode,
		ZstdImplementation:          zstdImplementation,
		HtpasswdFile:                htpasswdFile,
		MaxQueuedUploads:            maxQueuedUploads,
		NumUploaders:                numUploaders,
		MinTLSVersion:               minTLSVersion,
		TLSCaFile:                   tlsCaFile,
		TLSCertFile:                 tlsCertFile,
		TLSKeyFile:                  tlsKeyFile,
		AllowUnauthenticatedReads:   allowUnauthenticatedReads,
		S3CloudStorage:              s3,
		AzBlobConfig:                azblob,
		GoogleCloudStorage:          gcs,
		HTTPBackend:                 hc,
		GRPCBackend:                 grpcb,
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
		LogTimezone:                 logTimezone,
		MaxBlobSize:                 maxBlobSize,
		MaxProxyBlobSize:            maxProxyBlobSize,
		DigestFunctions:             digestFunctions,
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

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("Failed to read config file '%s': %v", path, err)
	}

	return newFromYaml(data)
}

func newFromYaml(data []byte) (*Config, error) {
	yc := YamlConfig{
		DigestFunctionNames: []string{"sha256"},
		Config: Config{
			StorageMode:            "zstd",
			ZstdImplementation:     "go",
			NumUploaders:           100,
			MinTLSVersion:          "1.0",
			MaxQueuedUploads:       1000000,
			MaxBlobSize:            math.MaxInt64,
			MaxProxyBlobSize:       math.MaxInt64,
			MetricsDurationBuckets: defaultDurationBuckets,
			AccessLogLevel:         "all",
			LogTimezone:            "UTC",
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

	dfs := make([]pb.DigestFunction_Value, 0)
	for _, dfn := range yc.DigestFunctionNames {
		df := hashing.DigestFunction(dfn)
		if df == pb.DigestFunction_UNKNOWN {
			return nil, fmt.Errorf("unknown digest function %s", dfn)
		}
		dfs = append(dfs, hashing.DigestFunction(dfn))
	}
	c.DigestFunctions = dfs

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
	if c.ZstdImplementation != "go" && c.ZstdImplementation != "cgo" {
		return errors.New("zstd_implementation must be set to either \"go\" or \"cgo\", got: " + c.ZstdImplementation)
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
	if c.AzBlobConfig != nil {
		proxyCount++
	}
	if c.GRPCBackend != nil {
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
		if err := c.HTTPBackend.validate("http"); err != nil {
			return err
		}
	}

	if c.GRPCBackend != nil {
		if err := c.GRPCBackend.validate("grpc"); err != nil {
			return err
		}
	}

	if c.S3CloudStorage != nil {
		if !s3proxy.IsValidAuthMethod(c.S3CloudStorage.AuthMethod) {
			return fmt.Errorf("invalid s3.auth_method: %s", c.S3CloudStorage.AuthMethod)
		}

		if c.S3CloudStorage.KeyVersion != nil && *c.S3CloudStorage.KeyVersion != 2 {
			return fmt.Errorf("s3.key_version (deprecated) must be 2, found %d", c.S3CloudStorage.KeyVersion)
		}

		if c.S3CloudStorage.BucketLookupType != "" && c.S3CloudStorage.BucketLookupType != "auto" &&
			c.S3CloudStorage.BucketLookupType != "dns" && c.S3CloudStorage.BucketLookupType != "path" {
			return fmt.Errorf("s3.bucket_lookup_type must be one of: \"auto\", \"dns\", \"path\" or empty/unspecified, found: \"%s\"",
				c.S3CloudStorage.BucketLookupType)
		}

		if c.S3CloudStorage.SignatureType != "" && c.S3CloudStorage.SignatureType != "v2" &&
			c.S3CloudStorage.SignatureType != "v4" && c.S3CloudStorage.SignatureType != "v4streaming" &&
			c.S3CloudStorage.SignatureType != "anonymous" {
			return fmt.Errorf("s3.signature_type must be one of: \"v2\", \"v4\", \"v4streaming\", \"anonymous\" or empty/unspecified, found: \"%s\"",
				c.S3CloudStorage.SignatureType)
		}
	}

	if c.AzBlobConfig != nil {
		if c.AzBlobConfig.StorageAccount == "" {
			return errors.New("The 'storage_account' field is required for 'azblob_proxy'")
		}

		if c.AzBlobConfig.ContainerName == "" {
			return errors.New("The 'container_name' field is required for 'azblob_proxy'")
		}

		if !azblobproxy.IsValidAuthMethod(c.AzBlobConfig.AuthMethod) {
			return fmt.Errorf("Invalid azblob.auth_method: %s", c.AzBlobConfig.AuthMethod)
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

	switch c.LogTimezone {
	case "UTC", "local", "none":
	default:
		return errors.New("'log_timezone' must be set to either \"UTC\", \"local\" or \"none\"")
	}

	if c.DigestFunctions == nil {
		return errors.New("at least on digest function must be supported")
	}
	for _, df := range c.DigestFunctions {
		if !hashing.Supported(df) {
			return fmt.Errorf("unsupported hashing function %s", df)
		}
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
			BucketLookupType:         ctx.String("s3.bucket_lookup_type"),
			Prefix:                   ctx.String("s3.prefix"),
			AuthMethod:               ctx.String("s3.auth_method"),
			AccessKeyID:              ctx.String("s3.access_key_id"),
			SecretAccessKey:          ctx.String("s3.secret_access_key"),
			SignatureType:            ctx.String("s3.signature_type"),
			DisableSSL:               ctx.Bool("s3.disable_ssl"),
			UpdateTimestamps:         ctx.Bool("s3.update_timestamps"),
			IAMRoleEndpoint:          ctx.String("s3.iam_role_endpoint"),
			Region:                   ctx.String("s3.region"),
			AWSProfile:               ctx.String("s3.aws_profile"),
			AWSSharedCredentialsFile: ctx.String("s3.aws_shared_credentials_file"),
		}
	}

	var hc *URLBackendConfig
	if ctx.String("http_proxy.url") != "" {
		u, err := url.Parse(ctx.String("http_proxy.url"))
		if err != nil {
			return nil, err
		}
		hc = &URLBackendConfig{
			BaseURL:  u,
			KeyFile:  ctx.String("http_proxy.key_file"),
			CertFile: ctx.String("http_proxy.cert_file"),
			CaFile:   ctx.String("http_proxy.ca_file"),
		}
	}

	var grpcb *URLBackendConfig
	if ctx.String("grpc_proxy.url") != "" {
		u, err := url.Parse(ctx.String("grpc_proxy.url"))
		if err != nil {
			return nil, err
		}

		grpcb = &URLBackendConfig{
			BaseURL:  u,
			KeyFile:  ctx.String("grpc_proxy.key_file"),
			CertFile: ctx.String("grpc_proxy.cert_file"),
			CaFile:   ctx.String("grpc_proxy.ca_file"),
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

	var azblob *AzBlobStorageConfig
	if ctx.String("azblob.tenant_id") != "" {
		azblob = &AzBlobStorageConfig{
			TenantID:         ctx.String("azblob.tenant_id"),
			StorageAccount:   ctx.String("azblob.storage_account"),
			ContainerName:    ctx.String("azblob.container_name"),
			Prefix:           ctx.String("azblob.prefix"),
			AuthMethod:       ctx.String("azblob.auth_method"),
			ClientID:         ctx.String("azblob.client_id"),
			ClientSecret:     ctx.String("azblob.client_secret"),
			CertPath:         ctx.String("azblob.cert_path"),
			SharedKey:        ctx.String("azblob.shared_key"),
			UpdateTimestamps: ctx.Bool("azblob.update_timestamps"),
		}
	}

	dfs := make([]pb.DigestFunction_Value, 0)
	if ctx.String("digest_functions") != "" {
		for _, dfn := range strings.Split(ctx.String("digest_functions"), ",") {
			df := hashing.DigestFunction(dfn)
			if df == pb.DigestFunction_UNKNOWN {
				return nil, fmt.Errorf("unknown digest function %s", dfn)
			}
			dfs = append(dfs, df)
		}
	}

	return newFromArgs(
		ctx.String("dir"),
		ctx.Int("max_size"),
		ctx.String("storage_mode"),
		ctx.String("zstd_implementation"),
		httpAddress,
		grpcAddress,
		profileAddress,
		ctx.String("htpasswd_file"),
		ctx.Int("max_queued_uploads"),
		ctx.Int("num_uploaders"),
		ctx.String("min_tls_version"),
		ctx.String("tls_ca_file"),
		ctx.String("tls_cert_file"),
		ctx.String("tls_key_file"),
		ctx.Bool("allow_unauthenticated_reads"),
		ctx.Duration("idle_timeout"),
		hc,
		grpcb,
		gcs,
		s3,
		azblob,
		ctx.Bool("disable_http_ac_validation"),
		ctx.Bool("disable_grpc_ac_deps_check"),
		ctx.Bool("enable_ac_key_instance_mangling"),
		ctx.Bool("enable_endpoint_metrics"),
		ctx.Bool("experimental_remote_asset_api"),
		ctx.Duration("http_read_timeout"),
		ctx.Duration("http_write_timeout"),
		ctx.String("access_log_level"),
		ctx.String("log_timezone"),
		ctx.Int64("max_blob_size"),
		ctx.Int64("max_proxy_blob_size"),
		dfs,
	)
}
