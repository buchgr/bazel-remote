package flags

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/buchgr/bazel-remote/cache/s3proxy"
	"github.com/urfave/cli/v2"
)

func s3AuthMsg(authMethods ...string) string {
	return fmt.Sprintf("Applies to s3 auth method(s): %s.", strings.Join(authMethods, ", "))
}

// GetCliFlags returns a slice of cli.Flag's that bazel-remote accepts.
func GetCliFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:  "config_file",
			Value: "",
			Usage: "Path to a YAML configuration file. If this flag is specified then all other flags " +
				"are ignored.",
			EnvVars: []string{"BAZEL_REMOTE_CONFIG_FILE"},
		},
		&cli.StringFlag{
			Name:    "dir",
			Value:   "",
			Usage:   "Directory path where to store the cache contents. This flag is required.",
			EnvVars: []string{"BAZEL_REMOTE_DIR"},
		},
		&cli.Int64Flag{
			Name:    "max_size",
			Value:   -1,
			Usage:   "The maximum size of the remote cache in GiB. This flag is required.",
			EnvVars: []string{"BAZEL_REMOTE_MAX_SIZE"},
		},
		&cli.StringFlag{
			Name:    "storage_mode",
			Value:   "zstd",
			Usage:   "Which format to store CAS blobs in. Must be one of \"zstd\" or \"uncompressed\".",
			EnvVars: []string{"BAZEL_REMOTE_STORAGE_MODE"},
		},
		&cli.StringFlag{
			Name:    "host",
			Value:   "",
			Usage:   "Address to listen on. Listens on all network interfaces by default.",
			EnvVars: []string{"BAZEL_REMOTE_HOST"},
		},
		&cli.IntFlag{
			Name:    "port",
			Value:   8080,
			Usage:   "The port the HTTP server listens on.",
			EnvVars: []string{"BAZEL_REMOTE_PORT"},
		},
		&cli.StringFlag{
			Name:    "socket",
			Value:   "",
			Usage:   "Unix domain socket path to listen on. Takes precedence over host and port.",
			EnvVars: []string{"BAZEL_REMOTE_SOCKET"},
		},
		&cli.IntFlag{
			Name:    "grpc_port",
			Value:   9092,
			Usage:   "The port the gRPC server listens on. Set to 0 to disable.",
			EnvVars: []string{"BAZEL_REMOTE_GRPC_PORT"},
		},
		&cli.StringFlag{
			Name:    "grpc_socket",
			Value:   "",
			Usage:   "Unix domain socket path the gRPC server listens on. Takes precedence over gRPC port.",
			EnvVars: []string{"BAZEL_REMOTE_GRPC_SOCKET"},
		},
		&cli.StringFlag{
			Name:    "profile_host",
			Value:   "127.0.0.1",
			Usage:   "A host address to listen on for profiling, if enabled by a valid --profile_port setting.",
			EnvVars: []string{"BAZEL_REMOTE_PROFILE_HOST"},
		},
		&cli.IntFlag{
			Name:        "profile_port",
			Value:       0,
			Usage:       "If a positive integer, serve /debug/pprof/* URLs from http://profile_host:profile_port.",
			DefaultText: "0, ie profiling disabled",
			EnvVars:     []string{"BAZEL_REMOTE_PROFILE_PORT"},
		},
		&cli.DurationFlag{
			Name:        "http_read_timeout",
			Value:       0,
			Usage:       "The HTTP read timeout for a client request in seconds (does not apply to the proxy backends or the profiling endpoint)",
			DefaultText: "0s, ie disabled",
			EnvVars:     []string{"BAZEL_REMOTE_HTTP_READ_TIMEOUT"},
		},
		&cli.DurationFlag{
			Name:        "http_write_timeout",
			Value:       0,
			Usage:       "The HTTP write timeout for a server response in seconds (does not apply to the proxy backends or the profiling endpoint)",
			DefaultText: "0s, ie disabled",
			EnvVars:     []string{"BAZEL_REMOTE_HTTP_WRITE_TIMEOUT"},
		},
		&cli.StringFlag{
			Name:    "htpasswd_file",
			Value:   "",
			Usage:   "Path to a .htpasswd file. This flag is optional. Please read https://httpd.apache.org/docs/2.4/programs/htpasswd.html.",
			EnvVars: []string{"BAZEL_REMOTE_HTPASSWD_FILE"},
		},
		&cli.StringFlag{
			Name:    "tls_ca_file",
			Value:   "",
			Usage:   "Optional. Enables mTLS (authenticating client certificates), should be the certificate authority that signed the client certificates.",
			EnvVars: []string{"BAZEL_REMOTE_TLS_CA_FILE"},
		},
		&cli.StringFlag{
			Name:    "tls_cert_file",
			Value:   "",
			Usage:   "Path to a pem encoded certificate file.",
			EnvVars: []string{"BAZEL_REMOTE_TLS_CERT_FILE"},
		},
		&cli.StringFlag{
			Name:    "tls_key_file",
			Value:   "",
			Usage:   "Path to a pem encoded key file.",
			EnvVars: []string{"BAZEL_REMOTE_TLS_KEY_FILE"},
		},
		&cli.BoolFlag{
			Name:        "allow_unauthenticated_reads",
			Value:       false,
			Usage:       "If authentication is enabled (--htpasswd_file or --tls_ca_file), allow unauthenticated clients read access.",
			DefaultText: "false, ie if authentication is required, read-only requests must also be authenticated",
			EnvVars:     []string{"BAZEL_REMOTE_UNAUTHENTICATED_READS"},
		},
		&cli.DurationFlag{
			Name:        "idle_timeout",
			Value:       0,
			Usage:       "The maximum period of having received no request after which the server will shut itself down.",
			DefaultText: "0s, ie disabled",
			EnvVars:     []string{"BAZEL_REMOTE_IDLE_TIMEOUT"},
		},
		&cli.IntFlag{
			Name:    "max_queued_uploads",
			Value:   1000000,
			Usage:   "When using proxy backends, sets the maximum number of objects in queue for upload. If the queue is full, uploads will be skipped until the queue has space again.",
			EnvVars: []string{"BAZEL_REMOTE_MAX_QUEUED_UPLOADS"},
		},
		&cli.Int64Flag{
			Name:        "max_blob_size",
			Value:       math.MaxInt64,
			Usage:       "The maximum logical/uncompressed blob size that will be accepted from clients. Note that this limit is not applied to preexisting blobs in the cache.",
			DefaultText: strconv.FormatInt(math.MaxInt64, 10),
			EnvVars:     []string{"BAZEL_REMOTE_MAX_BLOB_SIZE"},
		},
		&cli.IntFlag{
			Name:    "num_uploaders",
			Value:   100,
			Usage:   "When using proxy backends, sets the number of Goroutines to process parallel uploads to backend.",
			EnvVars: []string{"BAZEL_REMOTE_NUM_UPLOADERS"},
		},
		&cli.StringFlag{
			Name:    "http_proxy.url",
			Value:   "",
			Usage:   "The base URL to use for a http proxy backend.",
			EnvVars: []string{"BAZEL_REMOTE_HTTP_PROXY_URL"},
		},
		&cli.StringFlag{
			Name:    "gcs_proxy.bucket",
			Value:   "",
			Usage:   "The bucket to use for the Google Cloud Storage proxy backend.",
			EnvVars: []string{"BAZEL_REMOTE_GCS_BUCKET"},
		},
		&cli.BoolFlag{
			Name:    "gcs_proxy.use_default_credentials",
			Value:   false,
			Usage:   "Whether or not to use authentication for the Google Cloud Storage proxy backend.",
			EnvVars: []string{"BAZEL_REMOTE_GCS_USE_DEFAULT_CREDENTIALS"},
		},
		&cli.StringFlag{
			Name:    "gcs_proxy.json_credentials_file",
			Value:   "",
			Usage:   "Path to a JSON file that contains Google credentials for the Google Cloud Storage proxy backend.",
			EnvVars: []string{"BAZEL_REMOTE_GCS_JSON_CREDENTIALS_FILE"},
		},
		&cli.StringFlag{
			Name:    "s3.endpoint",
			Value:   "",
			Usage:   "The S3/minio endpoint to use when using S3 proxy backend.",
			EnvVars: []string{"BAZEL_REMOTE_S3_ENDPOINT"},
		},
		&cli.StringFlag{
			Name:    "s3.bucket",
			Value:   "",
			Usage:   "The S3/minio bucket to use when using S3 proxy backend.",
			EnvVars: []string{"BAZEL_REMOTE_S3_BUCKET"},
		},
		&cli.StringFlag{
			Name:    "s3.prefix",
			Value:   "",
			Usage:   "The S3/minio object prefix to use when using S3 proxy backend.",
			EnvVars: []string{"BAZEL_REMOTE_S3_PREFIX"},
		},
		&cli.StringFlag{
			Name:    "s3.auth_method",
			Value:   "",
			Usage:   fmt.Sprintf("The S3/minio authentication method. This argument is required when an s3 proxy backend is used. Allowed values: %s.", strings.Join(s3proxy.GetAuthMethods(), ", ")),
			EnvVars: []string{"BAZEL_REMOTE_S3_AUTH_METHOD"},
		},
		&cli.StringFlag{
			Name:    "s3.access_key_id",
			Value:   "",
			Usage:   "The S3/minio access key to use when using S3 proxy backend. " + s3AuthMsg(s3proxy.AuthMethodAccessKey),
			EnvVars: []string{"BAZEL_REMOTE_S3_ACCESS_KEY_ID"},
		},
		&cli.StringFlag{
			Name:    "s3.secret_access_key",
			Value:   "",
			Usage:   "The S3/minio secret access key to use when using S3 proxy backend. " + s3AuthMsg(s3proxy.AuthMethodAccessKey),
			EnvVars: []string{"BAZEL_REMOTE_S3_SECRET_ACCESS_KEY"},
		},
		&cli.StringFlag{
			Name:    "s3.aws_shared_credentials_file",
			Value:   "",
			Usage:   "Path to the AWS credentials file. If not specified, the minio client will default to '~/.aws/credentials'. " + s3AuthMsg(s3proxy.AuthMethodAWSCredentialsFile),
			EnvVars: []string{"BAZEL_REMOTE_S3_AWS_SHARED_CREDENTIALS_FILE", "AWS_SHARED_CREDENTIALS_FILE"},
		},
		&cli.StringFlag{
			Name:    "s3.aws_profile",
			Value:   "default",
			Usage:   "The aws credentials profile to use from within s3.aws_shared_credentials_file. " + s3AuthMsg(s3proxy.AuthMethodAWSCredentialsFile),
			EnvVars: []string{"BAZEL_REMOTE_S3_AWS_PROFILE", "AWS_PROFILE"},
		},
		&cli.BoolFlag{
			Name:        "s3.disable_ssl",
			Usage:       "Whether to disable TLS/SSL when using the S3 proxy backend.",
			DefaultText: "false, ie enable TLS/SSL",
			EnvVars:     []string{"BAZEL_REMOTE_S3_DISABLE_SSL"},
		},
		&cli.StringFlag{
			Name:    "s3.iam_role_endpoint",
			Value:   "",
			Usage:   "Endpoint for using IAM security credentials. By default it will look for credentials in the standard locations for the AWS platform. " + s3AuthMsg(s3proxy.AuthMethodIAMRole),
			EnvVars: []string{"BAZEL_REMOTE_S3_IAM_ROLE_ENDPOINT"},
		},
		&cli.StringFlag{
			Name:    "s3.region",
			Value:   "",
			Usage:   "The AWS region. Required when not specifying S3/minio access keys.",
			EnvVars: []string{"BAZEL_REMOTE_S3_REGION"},
		},
		&cli.IntFlag{
			Name:        "s3.key_version",
			Usage:       "DEPRECATED. Key version 2 now is the only supported value. This flag will be removed.",
			Value:       2,
			DefaultText: "2",
			EnvVars:     []string{"BAZEL_REMOTE_S3_KEY_VERSION"},
		},
		&cli.BoolFlag{
			Name:        "disable_http_ac_validation",
			Usage:       "Whether to disable ActionResult validation for HTTP requests.",
			DefaultText: "false, ie enable validation",
			EnvVars:     []string{"BAZEL_REMOTE_DISABLE_HTTP_AC_VALIDATION"},
		},
		&cli.BoolFlag{
			Name:        "disable_grpc_ac_deps_check",
			Usage:       "Whether to disable ActionResult dependency checks for gRPC GetActionResult requests.",
			DefaultText: "false, ie enable ActionCache dependency checks",
			EnvVars:     []string{"BAZEL_REMOTE_DISABLE_GRPS_AC_DEPS_CHECK"},
		},
		&cli.BoolFlag{
			Name:        "enable_ac_key_instance_mangling",
			Usage:       "Whether to enable mangling ActionCache keys with non-empty instance names.",
			DefaultText: "false, ie disable mangling",
			EnvVars:     []string{"BAZEL_REMOTE_ENABLE_AC_KEY_INSTANCE_MANGLING"},
		},
		&cli.BoolFlag{
			Name:        "enable_endpoint_metrics",
			Usage:       "Whether to enable metrics for each HTTP/gRPC endpoint.",
			DefaultText: "false, ie disable metrics",
			EnvVars:     []string{"BAZEL_REMOTE_ENABLE_ENDPOINT_METRICS"},
		},
		&cli.BoolFlag{
			Name:        "experimental_remote_asset_api",
			Usage:       "Whether to enable the experimental remote asset API implementation.",
			DefaultText: "false, ie disable remote asset API",
			EnvVars:     []string{"BAZEL_REMOTE_EXPERIMENTAL_REMOTE_ASSET_API"},
		},
		&cli.StringFlag{
			Name:        "access_log_level",
			Usage:       "The access logger verbosity level. If supplied, must be one of \"none\" or \"all\".",
			Value:       "all",
			DefaultText: "all, ie enable full access logging",
			EnvVars:     []string{"BAZEL_REMOTE_ACCESS_LOG_LEVEL"},
		},
	}
}
