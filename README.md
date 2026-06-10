![Build status](https://badge.buildkite.com/c11240e6e9519111f2380dfcf5fcb49e69fd5b2326c11a3059.svg?branch=master)

# bazel-remote cache

bazel-remote is a HTTP/1.1 and gRPC server that is intended to be used as a
remote build cache for [REAPI](https://github.com/bazelbuild/remote-apis)
clients like [Bazel](https://bazel.build) or as a component of a remote
execution service.

The cache contents are stored in a directory on disk with a maximum cache size,
and bazel-remote will automatically enforce this limit as needed, by deleting
the least recently used files. S3, GCS and experimental Azure blob storage
proxy backends are also supported.

Note that while bazel-remote is consumable as a go module, we provide no
guarantees on the stability or backwards compatibility of the APIs. We do
attempt to keep the standalone executable backwards-compatible between
releases however, and cache directory format changes are only allowed in
major version upgrades.

**Project status**: bazel-remote has been serving TBs of cache artifacts per day since April 2018, both on
commodity hardware and AWS servers. Outgoing bandwidth can exceed 15 Gbit/s on the right AWS instance type.

## HTTP/1.1 REST API

Cache entries are set and retrieved by key, and there are two types of keys that can be used:

1. Content addressed storage (CAS), where the key is the lowercase SHA256 hash of the entry.
   The REST API for these entries is: `/cas/<key>` or with an optional but ignored instance name:
   `/<instance>/cas/<key>`.
2. Action cache, where the key is an arbitrary 64 character lowercase hexadecimal string.
   Bazel uses the SHA256 hash of an action as the key, to store the metadata created by the action.
   The REST API for these entries is: `/ac/<key>` or with an optional instance name: `/<instance>/ac/<key>`.

Values are stored via HTTP PUT requests, and retrieved via GET requests.
HEAD requests can be used to confirm whether a key exists or not.

If GET requests specify `zstd` in the `Accept-Encoding` header, then
zstandard-encoded data may be returned.

To upload zstandard compressed data, PUT requests must set
`Content-Encoding: zstd` and include a custom `X-Digest-SizeBytes` header
with the size of the uncompressed entry. The key must also refer to
the uncompressed entry.

If the `--enable_ac_key_instance_mangling` flag is specified and the instance
name is not empty, then action cache keys are hashed along with the instance
name to produce the action cache lookup key. Since the URL path is processed
with Go's [path.Clean](https://golang.org/pkg/path/#Clean) function before
extracting the instance name, clients should avoid using repeated slashes,
`./` and `../` in the URL.

Values stored in the action cache are validated as an ActionResult protobuf message as per the
[Bazel Remote Execution API v2](https://github.com/bazelbuild/remote-apis/blob/master/build/bazel/remote/execution/v2/remote_execution.proto)
unless validation is disabled by configuration. The HTTP server also supports reading and writing JSON
encoded protobuf ActionResult messages to the action cache by using HTTP headers `Accept: application/json`
for GET requests and `Content-type: application/json` for PUT requests.

### Useful endpoints

**/status**

Returns the cache status/info.

```
$ curl http://localhost:8080/status
{
 "CurrSize": 414081715503,
 "ReservedSize": 876400,
 "MaxSize": 8589934592000,
 "NumFiles": 621413,
 "ServerTime": 1746258977,
 "GitCommit": "d0f166cdd973342ec4aa8a51228cfd3a7a205414",
 "GitTags": "v2.5.1",
 "NumGoroutines": 12
}
```

**/cas/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855**

The empty CAS blob is always available, even if the cache is empty. This can be used to test that
a bazel-remote instance is running and accepting requests.

```
$ curl --head --fail http://localhost:8080/cas/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
HTTP/1.1 200 OK
Content-Length: 0
Date: Fri, 01 May 2020 10:42:06 GMT
```

### Prometheus Metrics

To query endpoint metrics see [github.com/slok/go-http-metrics's query examples](https://github.com/slok/go-http-metrics#prometheus-query-examples).

## gRPC API

bazel-remote also supports the ActionCache, ContentAddressableStorage and Capabilities services in the
[Bazel Remote Execution API v2](https://github.com/bazelbuild/remote-apis/blob/master/build/bazel/remote/execution/v2/remote_execution.proto),
and the corresponding parts of the [Byte Stream API](https://github.com/googleapis/googleapis/blob/master/google/bytestream/bytestream.proto).

When using the `--enable_ac_key_instance_mangling` feature, clients are
advised to avoid repeated slashes, `../` and `./` strings in the instance
name, for consistency with the HTTP interface.

### Prometheus Metrics

To query endpoint metrics see [github.com/grpc-ecosystem/go-grpc-prometheus's metrics documentation](https://github.com/grpc-ecosystem/go-grpc-prometheus#metrics).

### Experimental Remote Asset API Support

There is (very) experimental support for a subset of the Fetch service in the
[Remote Asset API](https://github.com/bazelbuild/remote-apis/blob/master/build/bazel/remote/asset/v1/remote_asset.proto)
which can be enabled with the `--experimental_remote_asset_api` flag.

To use this with Bazel, specify
[--experimental_remote_downloader=grpc://replace-with-your.host:port](https://docs.bazel.build/versions/master/command-line-reference.html#flag--experimental_remote_downloader).

### Byte Stream compressed-blobs

This version of bazel-remote supports the
[Byte Stream compressed-blobs REAPI feature](https://github.com/bazelbuild/remote-apis/pull/168),
which provides a way for clients to upload and download CAS blobs compressed
with zstandard, in order to improve network efficiency.

Uploaded CAS blobs are stored in a zstandard compressed format by default,
which can increase the effective cache size and reduce load on the server
if clients also download blobs in zstandard compressed form. If you would
rather store CAS blobs in uncompressed form, add `--storage_mode uncompressed`
to your configuration.

## Usage

If a YAML configuration file is specified by the `--config_file` command line
flag or `BAZEL_REMOTE_CONFIG_FILE` environment variable, then other command
line flags and environment variables are ignored. Otherwise, the flags and
environment variables listed in the help text below can be specified (flags
override the corresponding environment variables).

See [examples/bazel-remote.service](examples/bazel-remote.service) for an
example (systemd) linux setup.

### Command line flags

```
$ ./bazel-remote --help
bazel-remote - A remote build cache for Bazel and other REAPI clients

USAGE:
   bazel-remote [options]

OPTIONS:
   --config_file value Path to a YAML configuration file. If this flag is
      specified then all other flags are ignored. [$BAZEL_REMOTE_CONFIG_FILE]

   --dir value Directory path where to store the cache contents. This flag is
      required. [$BAZEL_REMOTE_DIR]

   --max_size value The maximum size of bazel-remote's disk cache in GiB.
      This flag is required. (default: 0) [$BAZEL_REMOTE_MAX_SIZE]

   --storage_mode value Which format to store CAS blobs in. Must be one of
      "zstd" or "uncompressed". (default: "zstd") [$BAZEL_REMOTE_STORAGE_MODE]

   --zstd_implementation value ZSTD implementation to use. Supported values:
      "cgo", "go" (default: "go") [$BAZEL_REMOTE_ZSTD_IMPLEMENTATION]

   --http_address value Address specification for the HTTP server listener,
      formatted either as [host]:port for TCP or unix://path.sock for Unix
      domain sockets. [$BAZEL_REMOTE_HTTP_ADDRESS]

   --host value DEPRECATED. Use --http_address to specify the HTTP server
      listener. [$BAZEL_REMOTE_HOST]

   --port value DEPRECATED. Use --http_address to specify the HTTP server
      listener. (default: 8080) [$BAZEL_REMOTE_PORT]

   --grpc_address value Address specification for the gRPC server listener,
      formatted either as [host]:port for TCP or unix://path.sock for Unix
      domain sockets. Set to 'none' to disable. [$BAZEL_REMOTE_GRPC_ADDRESS]

   --grpc_port value DEPRECATED. Use --grpc_address to specify the gRPC
      server listener. Set to 0 to disable. (default: 9092)
      [$BAZEL_REMOTE_GRPC_PORT]

   --profile_address value Address specification for a http server to listen
      on for profiling, formatted either as [host]:port for TCP or
      unix://path.sock for Unix domain sockets. Off by default, but can also be
      set to 'none' to disable explicitly. (default: "", ie profiling disabled)
      [$BAZEL_REMOTE_PROFILE_ADDRESS]

   --profile_host value DEPRECATED. Use --profile_address instead. A host
      address to listen on for profiling, if enabled by a valid --profile_port
      setting. (default: "127.0.0.1") [$BAZEL_REMOTE_PROFILE_HOST]

   --profile_port value DEPRECATED. Use --profile_address instead. If a
      positive integer, serve /debug/pprof/* URLs from
      http://profile_host:profile_port. (default: 0, ie profiling disabled)
      [$BAZEL_REMOTE_PROFILE_PORT]

   --http_read_timeout value The HTTP read timeout for a client request in
      seconds (does not apply to the proxy backends or the profiling endpoint)
      (default: 0s, ie disabled) [$BAZEL_REMOTE_HTTP_READ_TIMEOUT]

   --http_write_timeout value The HTTP write timeout for a server response in
      seconds (does not apply to the proxy backends or the profiling endpoint)
      (default: 0s, ie disabled) [$BAZEL_REMOTE_HTTP_WRITE_TIMEOUT]

   --htpasswd_file value Path to a .htpasswd file. This flag is optional.
      Please read https://httpd.apache.org/docs/2.4/programs/htpasswd.html.
      [$BAZEL_REMOTE_HTPASSWD_FILE]

   --min_tls_version value The minimum TLS version that is acceptable for
      incoming requests (does not apply to proxy backends). Allowed values: 1.0,
      1.1, 1.2, 1.3. (default: "1.0") [$BAZEL_REMOTE_MIN_TLS_VERSION]

   --tls_ca_file value Optional. Enables mTLS (authenticating client
      certificates), should be the certificate authority that signed the client
      certificates. [$BAZEL_REMOTE_TLS_CA_FILE]

   --tls_cert_file value Path to a pem encoded certificate file.
      [$BAZEL_REMOTE_TLS_CERT_FILE]

   --tls_key_file value Path to a pem encoded key file.
      [$BAZEL_REMOTE_TLS_KEY_FILE]

   --allow_unauthenticated_reads If authentication is enabled
      (--htpasswd_file or --tls_ca_file), allow unauthenticated clients read
      access. (default: false, ie if authentication is required, read-only
      requests must also be authenticated) [$BAZEL_REMOTE_UNAUTHENTICATED_READS]

   --idle_timeout value The maximum period of having received no request
      after which the server will shut itself down. (default: 0s, ie disabled)
      [$BAZEL_REMOTE_IDLE_TIMEOUT]

   --max_queued_uploads value When using proxy backends, sets the maximum
      number of objects in queue for upload. If the queue is full, uploads will
      be skipped until the queue has space again. (default: 1000000)
      [$BAZEL_REMOTE_MAX_QUEUED_UPLOADS]

   --max_blob_size value The maximum logical/uncompressed blob size that will
      be accepted from clients. Note that this limit is not applied to
      preexisting blobs in the cache. (default: 9223372036854775807)
      [$BAZEL_REMOTE_MAX_BLOB_SIZE]

   --max_proxy_blob_size value The maximum logical/uncompressed blob size
      that will be downloaded from proxies. Note that this limit is not applied
      to preexisting blobs in the cache. (default: 9223372036854775807)
      [$BAZEL_REMOTE_MAX_PROXY_BLOB_SIZE]

   --num_uploaders value When using proxy backends, sets the number of
      Goroutines to process parallel uploads to backend. (default: 100)
      [$BAZEL_REMOTE_NUM_UPLOADERS]

   --grpc_proxy.url value The base URL to use for the experimental grpc proxy
      backend, e.g. grpc://localhost:9090 or grpcs://example.com:7070. Note that
      this requires a backend with remote asset API support if you want http
      client requests to work. [$BAZEL_REMOTE_GRPC_PROXY_URL]

   --grpc_proxy.key_file value Path to a key used to authenticate with the
      proxy backend using mTLS. If this flag is provided, then
      grpc_proxy.cert_file must also be specified.
      [$BAZEL_REMOTE_GRPC_PROXY_KEY_FILE]

   --grpc_proxy.cert_file value Path to a certificate used to authenticate
      with the proxy backend using mTLS. If this flag is provided, then
      grpc_proxy.key_file must also be specified.
      [$BAZEL_REMOTE_GRPC_PROXY_CERT_FILE]

   --grpc_proxy.ca_file value Path to a certificate autority used to validate
      the grpc proxy backend certificate. [$BAZEL_REMOTE_GRPC_PROXY_CA_FILE]

   --http_proxy.url value The base URL to use for a http proxy backend.
      [$BAZEL_REMOTE_HTTP_PROXY_URL]

   --http_proxy.key_file value Path to a key used to authenticate with the
      proxy backend using mTLS. If this flag is provided, then
      http_proxy.cert_file must also be specified.
      [$BAZEL_REMOTE_HTTP_PROXY_KEY_FILE]

   --http_proxy.cert_file value Path to a certificate used to authenticate
      with the proxy backend using mTLS. If this flag is provided, then
      http_proxy.key_file must also be specified.
      [$BAZEL_REMOTE_HTTP_PROXY_CERT_FILE]

   --http_proxy.ca_file value Path to a certificate autority used to validate
      the http proxy backend certificate. [$BAZEL_REMOTE_HTTP_PROXY_CA_FILE]

   --gcs_proxy.bucket value The bucket to use for the Google Cloud Storage
      proxy backend. [$BAZEL_REMOTE_GCS_BUCKET]

   --gcs_proxy.use_default_credentials Whether or not to use authentication
      for the Google Cloud Storage proxy backend. (default: false)
      [$BAZEL_REMOTE_GCS_USE_DEFAULT_CREDENTIALS]

   --gcs_proxy.json_credentials_file value Path to a JSON file that contains
      Google credentials for the Google Cloud Storage proxy backend.
      [$BAZEL_REMOTE_GCS_JSON_CREDENTIALS_FILE]

   --ldap.url value The LDAP URL which may include a port. LDAP over SSL
      (LDAPs) is also supported. Note that this feature is currently considered
      experimental. [$BAZEL_REMOTE_LDAP_URL]

   --ldap.base_dn value The distinguished name of the search base.
      [$BAZEL_REMOTE_LDAP_BASE_DN]

   --ldap.bind_user value The user who is allowed to perform a search within
      the base DN. If none is specified the connection and the search is
      performed without an authentication. It is recommended to use a read-only
      account. [$BAZEL_REMOTE_LDAP_BIND_USER]

   --ldap.bind_password value The password of the bind user.
      [$BAZEL_REMOTE_LDAP_BIND_PASSWORD]

   --ldap.username_attribute value The user attribute of a connecting user.
      (default: "uid") [$BAZEL_REMOTE_LDAP_USER_ATTRIBUTE]

   --ldap.groups_query value Filter clause for searching groups.
      [$BAZEL_REMOTE_LDAP_GROUPS_QUERY]

   --ldap.cache_time value The amount of time to cache a successful
      authentication in seconds. (default: 3600) [$BAZEL_REMOTE_LDAP_CACHE_TIME]

   --s3.endpoint value The S3/minio endpoint to use when using S3 proxy
      backend. [$BAZEL_REMOTE_S3_ENDPOINT]

   --s3.bucket value The S3/minio bucket to use when using S3 proxy backend.
      [$BAZEL_REMOTE_S3_BUCKET]

   --s3.bucket_lookup_type value The S3/minio bucket lookup type to use when
      using S3 proxy backend. Allowed values: auto, dns, path. (default: "auto")
      [$BAZEL_REMOTE_S3_BUCKET_LOOKUP_TYPE]

   --s3.prefix value The S3/minio object prefix to use when using S3 proxy
      backend. [$BAZEL_REMOTE_S3_PREFIX]

   --s3.auth_method value The S3/minio authentication method. This argument
      is required when an s3 proxy backend is used. Allowed values: iam_role,
      access_key, aws_credentials_file. [$BAZEL_REMOTE_S3_AUTH_METHOD]

   --s3.access_key_id value The S3/minio access key to use when using S3
      proxy backend. Applies to s3 auth method(s): access_key.
      [$BAZEL_REMOTE_S3_ACCESS_KEY_ID]

   --s3.secret_access_key value The S3/minio secret access key to use when
      using S3 proxy backend. Applies to s3 auth method(s): access_key.
      [$BAZEL_REMOTE_S3_SECRET_ACCESS_KEY]

   --s3.session_token value The S3/minio session token to use when using S3
      proxy backend. Optional. Applies to s3 auth method(s): access_key.
      [$BAZEL_REMOTE_S3_SESSION_TOKEN]

   --s3.signature_type value Which type of s3 signature to use when using S3
      proxy backend. Only applies when using the s3 access_key auth method.
      Allowed values: v2, v4, v4streaming, anonymous. (default: v4)
      [$BAZEL_REMOTE_S3_SIGNATURE_TYPE]

   --s3.aws_shared_credentials_file value Path to the AWS credentials file.
      If not specified, the minio client will default to '~/.aws/credentials'.
      Applies to s3 auth method(s): aws_credentials_file.
      [$BAZEL_REMOTE_S3_AWS_SHARED_CREDENTIALS_FILE,
      $AWS_SHARED_CREDENTIALS_FILE]

   --s3.aws_profile value The aws credentials profile to use from within
      s3.aws_shared_credentials_file. Applies to s3 auth method(s):
      aws_credentials_file. (default: "default") [$BAZEL_REMOTE_S3_AWS_PROFILE,
      $AWS_PROFILE]

   --s3.disable_ssl Whether to disable TLS/SSL when using the S3 proxy
      backend. (default: false, ie enable TLS/SSL)
      [$BAZEL_REMOTE_S3_DISABLE_SSL]

   --s3.update_timestamps Whether to update timestamps of object on cache
      hit. (default: false) [$BAZEL_REMOTE_S3_UPDATE_TIMESTAMPS]

   --s3.iam_role_endpoint value Endpoint for using IAM security credentials.
      By default it will look for credentials in the standard locations for the
      AWS platform. Applies to s3 auth method(s): iam_role.
      [$BAZEL_REMOTE_S3_IAM_ROLE_ENDPOINT]

   --s3.region value The AWS region. Required when not specifying S3/minio
      access keys. [$BAZEL_REMOTE_S3_REGION]

   --s3.key_version value DEPRECATED. Key version 2 now is the only supported
      value. This flag will be removed. (default: 2)
      [$BAZEL_REMOTE_S3_KEY_VERSION]

   --s3.max_idle_conns value The maximum number of idle connections to use
      when using the S3 proxy backend. (default: 1024)
      [$BAZEL_REMOTE_S3_MAX_IDLE_CONNS]

   --azblob.tenant_id value The Azure blob storage tenant id to use when
      using azblob proxy backend. [$BAZEL_REMOTE_AZBLOB_TENANT_ID,
      $AZURE_TENANT_ID]

   --azblob.storage_account value The Azure blob storage storage account to
      use when using azblob proxy backend.
      [$BAZEL_REMOTE_AZBLOB_STORAGE_ACCOUNT]

   --azblob.container_name value The Azure blob storage container name to use
      when using azblob proxy backend. [$BAZEL_REMOTE_AZBLOB_CONTAINER_NAME]

   --azblob.prefix value The Azure blob storage object prefix to use when
      using azblob proxy backend. [$BAZEL_REMOTE_AZBLOB_PREFIX]

   --azblob.update_timestamps Whether to update timestamps of object on cache
      hit. (default: false) [$BAZEL_REMOTE_AZBLOB_UPDATE_TIMESTAMPS]

   --azblob.auth_method value The Azure blob storage authentication method.
      This argument is required when an azblob proxy backend is used. Allowed
      values: client_certificate, client_secret, environment_credential,
      shared_key, default. [$BAZEL_REMOTE_AZBLOB_AUTH_METHOD]

   --azblob.shared_key value The Azure blob storage account access key to use
      when using azblob proxy backend. Applies to AzBlob auth method(s):
      shared_key. [$BAZEL_REMOTE_AZBLOB_SHARED_KEY, $AZURE_STORAGE_ACCOUNT_KEY]

   --azblob.client_id value The Azure blob storage client id to use when
      using azblob proxy backend. Applies to AzBlob auth method(s):
      client_secret, client_certificate. [$BAZEL_REMOTE_AZBLOB_CLIENT_ID,
      $AZURE_CLIENT_ID]

   --azblob.client_secret value The Azure blob storage client secret key to
      use when using azblob proxy backend. Applies to AzBlob auth method(s):
      client_secret. [$BAZEL_REMOTE_AZBLOB_SECRET_CLIENT_SECRET,
      $AZURE_CLIENT_SECRET]

   --azblob.cert_path value Path to the certificates file. Applies to AzBlob
      auth method(s): client_certificate. [$BAZEL_REMOTE_AZBLOB_CERT_PATH,
      $AZURE_CLIENT_CERTIFICATE_PATH]

   --disable_http_ac_validation Whether to disable ActionResult validation
      for HTTP requests. (default: false, ie enable validation)
      [$BAZEL_REMOTE_DISABLE_HTTP_AC_VALIDATION]

   --disable_grpc_ac_deps_check Whether to disable ActionResult dependency
      checks for gRPC GetActionResult requests. (default: false, ie enable
      ActionCache dependency checks) [$BAZEL_REMOTE_DISABLE_GRPS_AC_DEPS_CHECK]

   --enable_ac_key_instance_mangling Whether to enable mangling ActionCache
      keys with non-empty instance names. (default: false, ie disable mangling)
      [$BAZEL_REMOTE_ENABLE_AC_KEY_INSTANCE_MANGLING]

   --enable_endpoint_metrics Whether to enable metrics for each HTTP/gRPC
      endpoint. (default: false, ie disable metrics)
      [$BAZEL_REMOTE_ENABLE_ENDPOINT_METRICS]

   --http_metrics_prefix Whether to prefix http metrics with "bazel_remote"
      or not (default: false, ie no prefix) [$BAZEL_REMOTE_HTTP_METRICS_PREFIX]

   --experimental_remote_asset_api Whether to enable the experimental remote
      asset API implementation. (default: false, ie disable remote asset API)
      [$BAZEL_REMOTE_EXPERIMENTAL_REMOTE_ASSET_API]

   --access_log_level value The access logger verbosity level. If supplied,
      must be one of "none" or "all". (default: all, ie enable full access
      logging) [$BAZEL_REMOTE_ACCESS_LOG_LEVEL]

   --log_timezone value The timezone to use for log timestamps. If supplied,
      must be one of "UTC", "local" or "none" for no timestamps. (default: UTC,
      ie use UTC timezone) [$BAZEL_REMOTE_LOG_TIMEZONE]

   --help, -h  show help
```

### Example configuration file

```yaml
# These two are the only required options:
dir: path/to/cache-dir
max_size: 100

# If specified, write requests will be rejected when max_size_hard_limit is
# reached. Clients can then decide which requests to retry. This setting can
# be used to avoid running out of disk space when new blobs are uploaded faster
# than old blobs can be evicted. A reasonable value might be 5% larger than
# max_size. A higher limit might be needed when using a proxy backend.
#
# The max_size_hard_limit can be tuned by watching how the prometheus query
# max_over_time(bazel_remote_disk_cache_size_bytes[$__interval]) varies
# between bazel_remote_disk_cache_size_bytes_limit{type="evict"} and
# bazel_remote_disk_cache_size_bytes_limit{type="reject"}.
#max_size_hard_limit: 105

# The form to store CAS blobs in ("zstd" or "uncompressed"):
#storage_mode: zstd

# The server listener address for HTTP/HTTPS. For TCP listeners,
# use [host]:port, where host is optional (default 0.0.0.0) and can
# be either a hostname or IP address. For Unix domain socket listeners,
# use unix:///path/to/socket.sock, where /path/to/socket.sock can be
# either an absolute or relative path to a socket path.
http_address: 0.0.0.0:8080
# The server listener address for gRPC (unix sockets are also supported
# as described above):
#grpc_address: 0.0.0.0:9092

# If profile_address (or the deprecated profile_port and/or profile_host)
# is specified, then serve /debug/pprof/* URLs here (unix sockets are also
# supported as described above):
#profile_address: 127.0.0.1:7070

# HTTP read/write timeouts. Note that these do not apply to the proxy
# backends or the profiling endpoint. Reasonable values might be twice
# the length of time that you expect a client to read/write the largest
# likely blob. Units can be one of: "s", "m", "h".
#http_read_timeout: 15s
#http_write_timeout: 20s

# Specify a certificate if you want to use HTTPS and gRPCs:
#tls_cert_file: path/to/tls.cert
#tls_key_file:  path/to/tls.key
# If you want to use mutual TLS with client certificates:
#tls_ca_file: path/to/ca/cert.pem

# Optionally specify the minimum supported TLS version for the
# HTTPS/gRPCs servers (must be one of 1.0, 1.1, 1.2, 1.3):
#min_tls_version: "1.0"

# Alternatively, you can use simple authentication:
#htpasswd_file: path/to/.htpasswd

# At most one authentication mechanism can be used
#ldap:
#  url: ldaps://ldap.example.com:636
#  base_dn: OU=My Users,DC=example,DC=com
#  username_attribute: sAMAccountName      # defaults to "uid"
#  bind_user: ldapuser
#  bind_password: ldappassword
#  cache_time: 3600                        # in seconds (default 1 hour)
#  groups_query: (memberOf=CN=bazel-users,OU=Groups,OU=My Users,DC=example,DC=com)

# If tls_ca_file or htpasswd_file are specified, you can choose
# whether or not to allow unauthenticated read access:
#allow_unauthenticated_reads: false

# If specified, bazel-remote should exit after being idle
# for this long. Time units can be one of: "s", "m", "h".
#idle_timeout: 45s

# If set to true, do not validate that ActionCache
# items are valid ActionResult protobuf messages.
#disable_http_ac_validation: false

# If set to true, do not check that CAS items referred
# to by ActionResult messages are in the cache.
#disable_grpc_ac_deps_check: false

# If set to true, enable metrics for each HTTP/gRPC endpoint.
#enable_endpoint_metrics: false

# Specify a custom list of histogram buckets for endpoint request duration metrics
#endpoint_metrics_duration_buckets: [.5, 1, 2.5, 5, 10, 20, 40, 80, 160, 320]

# At most one of the proxy backends can be selected:
#
# If this is 0, proxy backends won't upload blobs.
#num_uploaders: 100
# The maximum number of proxy uploads to queue, before dropping uploads.
#max_queued_uploads: 1000000
# The largest blob size that will be accepted, for example 10MB:
#max_blob_size: 10485760
#
#gcs_proxy:
#  bucket: gcs-bucket
#  use_default_credentials: false
#  json_credentials_file: path/to/creds.json
#
#s3_proxy:
#  endpoint: minio.example.com:9000
#  bucket: test-bucket
#  prefix: test-prefix
#  disable_ssl: true
#  bucket_lookup_type: auto
#  max_idle_conns: 1024
#
# Provide exactly one auth_method (access_key, iam_role, or credentials_file) and accompanying configuration.
#
# Access key authenticaiton:
#  auth_method: access_key
#  access_key_id: EXAMPLE_ACCESS_KEY
#  secret_access_key: EXAMPLE_SECRET_KEY
#  session_token: EXAMPLE_SESSION_TOKEN
#  signature_type: v4
#
# IAM Role authentication.
#  auth_method: iam_role
#  iam_role_endpoint: http://169.254.169.254
#  region: us-east-1
#
# AWS credentials file.
#  auth_method: credentials_file
#  aws_shared_credentials_file: path/to/aws/credentials
#  aws_profile: my-profile
#
#http_proxy:
#  url: https://remote-cache.com:8080/cache
# If you want to use mutual TLS with client certificates:
#  cert_file: path/to/client.cert
#  key_file:  path/to/client.key
# If you want to use a custom CA:
#  ca_file: path/to/ca.crt
#
# Note that the grpc proxy backend requires remote asset API support if
# you want client -http-> bazel-remote -grpc-> backend requests to work.
#grpc_proxy:
#  url: grpc://remote-cache.com:9092
# If you want to use mutual TLS with client certificates:
#  cert_file: path/to/client.cert
#  key_file:  path/to/client.key
# If you want to use a custom CA:
#  ca_file: path/to/ca.crt
#
#azblob_proxy:
#  tenant_id: TENANT_ID
#  storage_account: STORAGE_ACCOUNT
#  container_name: CONTAINER_NAME
#
# Provide exactly one auth_method (client_certificate, client_secret, environment_credential,
#￼shared_key, default) and accompanying configuration.
#
# Storage account shared key.
#  auth_method: shared_key
#  shared_key: APP_SHARED_KEY
#
# Client secret credentials.
#  auth_method: client_secret
#  client_id: APP_ID
#  client_secret: APP_SECRET
#
# Client certificate credentials.
#  auth_method: client_certificate
#  cert_path: path/to/cert_file
#
# Default and environment methods don't have any additional parameters.
#  auth_method: environment_credential
#
#  auth_method: default

# If set to a valid port number, then serve /debug/pprof/* URLs here:
#profile_port: 7070
# IP address to use, if profiling is enabled:
#profile_host: 127.0.0.1

# If true, enable experimental remote asset API support:
#experimental_remote_asset_api: true

# If supplied, controls the verbosity of the access logger ("none" or "all"):
#access_log_level: none

# If supplied, controls the timezone of the access logger ("UTC", "local" or "none"):
#log_timezone: local
```

## Docker

### Prebuilt Image

We publish docker images to [DockerHub](https://hub.docker.com/r/buchgr/bazel-remote-cache/)
and [quay.io](https://quay.io/repository/bazel-remote/bazel-remote)
that you can use with `docker run`. The following commands will start bazel-remote with uid
and gid `1000` on port `9090` for HTTP and `9092` for gRPC, with the default maximum cache
size of `5 GiB`.

```bash
# Dockerhub example:
$ docker pull buchgr/bazel-remote-cache
$ docker run -u 1000:1000 -v /path/to/cache/dir:/data \
	-p 9090:8080 -p 9092:9092 buchgr/bazel-remote-cache \
	--max_size 5
```

```bash
# quay.io example:
$ docker pull quay.io/bazel-remote/bazel-remote
$ docker run -u 1000:1000 -v /path/to/cache/dir:/data \
	-p 9090:8080 -p 9092:9092 quay.io/bazel-remote/bazel-remote \
	--max_size 5
```

Note that you will need to change `/path/to/cache/dir` to a valid directory that is readable
and writable by the specified user (or by uid/gid `65532` if no user was specified).

If you want the docker container to run in the background pass the `-d` flag right after `docker run`.

You can adjust the maximum cache size by appending `--max_size N`, where N is
the maximum size in Gibibytes.

### Docker Compose notes

See [examples/docker-compose.yml](examples/docker-compose.yml) for an example configuration (modify the `--max_size` flag in there to suit your needs).

### Kubernetes notes

- See [examples/kubernetes.yml](examples/kubernetes.yml) for an example
  configuration.

- Don't name your deployment `bazel-remote`!

  Kubernetes sets some environment variables based on this name, which conflict
  with the `BAZEL_REMOTE_*` environment variables that bazel-remote tries to
  parse.

- bazel-remote supports the `/grpc.health.v1.Health/Check` service, which you can
  configure like so:
  ```
  alb.ingress.kubernetes.io/backend-protocol: HTTP
  alb.ingress.kubernetes.io/backend-protocol-version: GRPC
  alb.ingress.kubernetes.io/healthcheck-path: /grpc.health.v1.Health/Check
  alb.ingress.kubernetes.io/healthcheck-port: 9092
  alb.ingress.kubernetes.io/listen-ports: [{"HTTPS": 9092}]
  alb.ingress.kubernetes.io/success-codes: 0
  alb.ingress.kubernetes.io/target-type: ip
  ```

### Build your own docker image

The command below will build a docker image from source and install it into your local docker registry.

```bash
$ bazel run :bazel-remote-image-amd64-tarball
# Ensure /your/path/to/data exists and is writable (e.g. by UID 65532)
$ docker run -v /your/path/to/data:/data bazel-remote-cache:tmp-amd64 --max_size 5 --dir /data
```

### ARM64 docker image

Bazel-remote can also run on ARM64 architecture devices, for example on a Raspberry Pi.

To build a docker image for ARM64:

```bash
$ bazel run :bazel-remote-image-arm64-tarball
# Ensure /your/path/to/data exists and is writable (e.g. by UID 65532)
$ docker run -v /your/path/to/data:/data bazel-remote-cache:tmp-arm64 --max_size 5 --dir /data
```

## Build a standalone Linux binary

```bash
$ bazel build :bazel-remote
```

### Authentication

bazel-remote defaults to allow unauthenticated access, but basic `.htpasswd`
style authentication, mutual TLS authentication and (experimental) LDAP are
also supported.

Note that only one authentication mechanism can be used at a time.

#### htpasswd

In order to pass a `.htpasswd` and/or server key file(s) to the cache
inside a docker container, you first need to mount the file in the
container and pass the path to the cache. The example below also
configures TLS which is technically optional but highly recommended
in order to not send passwords in plain text.

```bash
$ docker run -v /path/to/cache/dir:/data \
	-v /path/to/htpasswd:/etc/bazel-remote/htpasswd \
	-v /path/to/server_cert:/etc/bazel-remote/server_cert \
	-v /path/to/server_key:/etc/bazel-remote/server_key \
	-p 9090:8080 -p 9092:9092 buchgr/bazel-remote-cache \
	--tls_cert_file=/etc/bazel-remote/server_cert \
	--tls_key_file=/etc/bazel-remote/server_key \
	--htpasswd_file /etc/bazel-remote/htpasswd --max_size 5
```

#### mTLS

If you prefer not using `.htpasswd` files it is also possible to
authenticate with mTLS (also can be known as "authenticating client
certificates"). You can do this by passing in the the cert/key the
server should use, as well as the certificate authority that signed
the client certificates:

```bash
$ docker run -v /path/to/cache/dir:/data \
	-v /path/to/certificate_authority:/etc/bazel-remote/ca_cert \
	-v /path/to/server_cert:/etc/bazel-remote/server_cert \
	-v /path/to/server_key:/etc/bazel-remote/server_key \
	-p 9090:8080 -p 9092:9092 buchgr/bazel-remote-cache \
	--tls_ca_file=/etc/bazel-remote/ca_cert \
	--tls_cert_file=/etc/bazel-remote/server_cert \
	--tls_key_file=/etc/bazel-remote/server_key \
	--max_size 5
```

#### LDAP

There is also an experimental LDAP authentication method. A configuration
file is advised to avoid leaking the ldap.bind_password value to local
users, but command line arguments are also supported.

Note that the configuration options for this feature might change while
the feature is still considered "experimental".

```bash
$ docker run -v /path/to/cache/dir:/data \
   -p 9090:8080 -p 9092:9092 buchgr/bazel-remote-cache \
   --ldap.url="ldaps://ldap.example.com:636" \
   --ldap.base_dn="OU=My Users,DC=example,DC=com" \
   --ldap.groups_query="(|(memberOf=CN=bazel-users,OU=Groups,OU=My Users,DC=example,DC=com)(memberOf=CN=other-users,OU=Groups2,OU=Alien Users,DC=foo,DC=org))" \
   --ldap.cache_time=100 \
   --ldap.bind_user="cn=readonly.username,ou=readonly,OU=Other Users,DC=example,DC=com" \
   --ldap.bind_password="secret4Sure" \
   --max_size 5
```

### Using bazel-remote with AWS Credential file authentication for S3 inside a docker container

The following demonstrates how to configure a docker instance of bazel-remote to use an AWS S3
backend, authenticating using the `supercool` profile from your `$HOME/.aws/credentials` file.

```bash
$ docker run -u 1000:1000 -v /path/to/cache/dir:/data -v $HOME/.aws:/aws-config \
   -p 9090:8080 -p 9092:9092 buchgr/bazel-remote-cache \
   --s3.auth_method=aws_credentials_file --s3.aws_profile=supercool \
   --s3.aws_shared_credentials_file=/aws-config/credentials \
   --s3.bucket=my-bucket --s3.endpoint=s3.us-east-1.amazonaws.com \
   --max_size 5
```

Note that if you use the `--s3.auth_method=iam_role` flag with docker, then in
order to make the S3 host instance metadata service (located at 169.254.169.254)
reachable, then you may need to use the docker flag `--network=host`.

### Profiling

To enable pprof profiling, specify an address to listen to with
`--profile_address`.

If running inside docker, you will need to use a profile_address value
with a host other than `127.0.0.1` and add a `-p` mapping to the docker
run commandline for the port.

See [Profiling Go programs with pprof](https://jvns.ca/blog/2017/09/24/profiling-go-with-pprof/)
for more details.

## Configuring Bazel

To make bazel use remote cache, use the following flag:
`--remote_cache=http://replace-with-your.host:port`. You can also use the
following protocols instead of http: https, grpc or grpcs (depending on your
bazel-remote configuration).

Basic username/password authentication can be added like so:

`--remote_cache=http://user:pass@replace-with-your.host:port`

To avoid leaking your password in log files, you can place this flag in a
[user-specific (and .gitignore'd) bazelrc file](https://docs.bazel.build/versions/master/best-practices.html#bazelrc).

To use mutual TLS with bazel, use a `grpcs` URL for the `--remote_cache`
argument, and add the following flags:

```bash
	--tls_certificate=path/to/ca.cert
	--tls_client_certificate=path/to/client/cert.cert
	--tls_client_key=path/to/client/cert.key
```

For more details, see Bazel's [remote
caching](https://docs.bazel.build/versions/master/remote-caching.html#run-bazel-using-the-remote-cache)
documentation.
