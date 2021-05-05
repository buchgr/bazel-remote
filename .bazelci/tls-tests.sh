#!/usr/bin/env bash

set -v
set -e
set -u
set -o pipefail

SRC_ROOT=$(dirname "$0")/..
SRC_ROOT=$(realpath "$SRC_ROOT")
cd "$SRC_ROOT"

HTTP_PORT=8089

#export GRPC_GO_LOG_VERBOSITY_LEVEL=99
#export GRPC_GO_LOG_SEVERITY_LEVEL=info

tmpdir=$(mktemp -d bazel-remote-tls-tests.XXXXXXX --tmpdir=${TMPDIR:-/tmp})

generate_keys() {
	# Copied from https://github.com/bazelbuild/bazel/blob/master/src/test/testdata/test_tls_certificate/README.md

	local SERVER_CN=localhost
	local CLIENT_CN=localhost

	# Generate CA key:
	openssl genrsa -passout pass:1111 -des3 -out "$tmpdir/ca.key" 4096
	# Generate CA cert:
	openssl req -passin pass:1111 -new -x509 -days 358000 -key "$tmpdir/ca.key" \
		-out "$tmpdir/ca.crt" -subj "/CN=${SERVER_CN}"

	# Generate server key:
	openssl genrsa -passout pass:1111 -des3 -out "$tmpdir/server.key" 4096

	# Generate server signing request:
	openssl req -passin pass:1111 -new -key "$tmpdir/server.key" \
		-out "$tmpdir/server.csr" -subj "/CN=${SERVER_CN}"

	# Add subjectAltName aka "SAN", which replaces CN.
	# Required for Go >= 1.15.
	cat << EOF > domain.ext
authorityKeyIdentifier=keyid,issuer
basicConstraints=CA:FALSE
keyUsage = digitalSignature, nonRepudiation, keyEncipherment, dataEncipherment
subjectAltName = @alt_names
[alt_names]
DNS.1 = localhost
EOF

	# Self-signed server certificate:
	openssl x509 -req -passin pass:1111 -days 358000 -in "$tmpdir/server.csr" \
		-extfile domain.ext \
		-CA "$tmpdir/ca.crt" -CAkey "$tmpdir/ca.key" -set_serial 01 -out "$tmpdir/server.crt"

	# Remove passphrase from server key:
	openssl rsa -passin pass:1111 -in "$tmpdir/server.key" -out "$tmpdir/server.key"

	# Generate client key:
	openssl genrsa -passout pass:1111 -des3 -out "$tmpdir/client.key" 4096

	# Generate client signing request:
	openssl req -passin pass:1111 -new -key "$tmpdir/client.key" \
		-out "$tmpdir/client.csr" -subj "/CN=${CLIENT_CN}"

	# Self-signed client certificate:
	openssl x509 -passin pass:1111 -req -days 358000 -in "$tmpdir/client.csr" \
		-CA "$tmpdir/ca.crt" -CAkey "$tmpdir/ca.key" -set_serial 01 -out "$tmpdir/client.crt"

	# Remove passphrase from client key:
	openssl rsa -passin pass:1111 -in "$tmpdir/client.key" -out "$tmpdir/client.key"

	# Convert the private keys to X.509:
	openssl pkcs8 -topk8 -nocrypt -in "$tmpdir/client.key" -out "$tmpdir/client.pem"

	# Generate server.pem which is the privateKeyFile for the server:
	openssl pkcs8 -topk8 -nocrypt -in "$tmpdir/server.key" -out "$tmpdir/server.pem"
}

generate_keys

[ -e bazel-remote ] || ./linux-build.sh

echo "Starting bazel-remote, allowing unauthenticated reads..."
./bazel-remote --dir "$tmpdir/cache" --max_size 1 --port "$HTTP_PORT" \
	--tls_cert_file "$tmpdir/server.crt" \
	--tls_key_file "$tmpdir/server.key" \
	--tls_ca_file "$tmpdir/ca.crt" \
	--allow_unauthenticated_reads > "$tmpdir/bazel-remote.log" 2>&1 &
server_pid=$!

# Wait a bit for the server start up...

running=false
for i in $(seq 1 20)
do
	sleep 1

	if wget --inet4-only -d -O - --ca-certificate=$tmpdir/server.crt \
		--certificate=$tmpdir/client.crt \
		--private-key=$tmpdir/client.pem \
		--timeout=2 \
		"https://localhost:$HTTP_PORT/status"
	then
		running=true
		break
	fi
done

if [ "$running" != true ]
then
	echo "Error: bazel-remote took too long to start"
	kill -9 $server_pid
	exit 1
fi

# Authenticated read.
wget --inet4-only -d -O - --ca-certificate=$tmpdir/server.crt \
	--certificate=$tmpdir/client.crt \
	--private-key=$tmpdir/client.pem \
	https://localhost:$HTTP_PORT/status

# Unauthenticated read.
wget --inet4-only -d -O - --ca-certificate=$tmpdir/server.crt \
	https://localhost:$HTTP_PORT/status

# Run without auth, expect readonly access.
bazel run //utils/grpcreadclient -- -server-addr localhost:9092 \
	-ca-cert-file "$tmpdir/ca.crt" \
	-reads-should-work

# Run with auth, expect read-write access.
bazel run //utils/grpcreadclient -- -server-addr localhost:9092 \
	-ca-cert-file "$tmpdir/ca.crt" \
	-client-cert-file "$tmpdir/client.crt" \
	-client-key-file "$tmpdir/client.key"

# Authenticated build, populate the cache.
bazel clean
bazel build //:bazel-remote --remote_cache=grpcs://localhost:9092 \
	--tls_certificate "$tmpdir/ca.crt" \
	--tls_client_certificate "$tmpdir/client.crt" \
	--tls_client_key "$tmpdir/client.pem"

# Unauthenticated build, don't attempt to upload (gRPC).
bazel clean
bazel build //:bazel-remote --remote_cache=grpcs://localhost:9092 \
	--tls_certificate "$tmpdir/ca.crt" \
	--noremote_upload_local_results

# Unauthenticated build, don't attempt to upload (HTTP).
bazel clean
bazel build //:bazel-remote --remote_cache=https://localhost:$HTTP_PORT \
	--tls_certificate "$tmpdir/ca.crt" \
	--noremote_upload_local_results

# Unauthenticated gRPC client, should fail to write, but the build
# should succeed.
bazel clean
bazel build //:bazel-remote --remote_cache=grpcs://localhost:9092 \
	--tls_certificate "$tmpdir/ca.crt" \
	2>&1 | tee "$tmpdir/unauthenticated_write.log"

grep -A 1 "WARNING: Writing to Remote Cache:" "$tmpdir/unauthenticated_write.log" | \
	tr '\n' '|' > "$tmpdir/unauthenticated_write.log.singleline"
if ! grep --silent "WARNING: Writing to Remote Cache:|BulkTransferException|" "$tmpdir/unauthenticated_write.log.singleline"
then
	# We seem to always have one cache miss with a rebuild.
	# So we expect a single cache write attempt, and it should fail.
	echo "Error: expected a warning when writing to the remote cache fails"
	exit 1
fi

# Restart the server with authentication enabled but unauthenticated reads disabled.
kill -9 $server_pid
./bazel-remote --dir "$tmpdir/cache" --max_size 1 --port "$HTTP_PORT" \
	--tls_cert_file "$tmpdir/server.crt" \
	--tls_key_file "$tmpdir/server.key" \
	--tls_ca_file "$tmpdir/ca.crt" > "$tmpdir/bazel-remote-authenticated.log" 2>&1 &
server_pid=$!

# Wait a bit for the server start up...

running=false
for i in $(seq 1 20)
do
	sleep 1

	if wget --inet4-only -d -O - --ca-certificate=$tmpdir/server.crt \
		--certificate=$tmpdir/client.crt \
		--private-key=$tmpdir/client.pem \
		--timeout=2 \
		"https://localhost:$HTTP_PORT/status"
	then
		running=true
		break
	fi
done

if [ "$running" != true ]
then
	echo "Error: bazel-remote took too long to start"
	kill -9 $server_pid
	exit 1
fi

# Authenticated read should succeed.
wget --inet4-only -d -O - --ca-certificate=$tmpdir/server.crt \
	--certificate=$tmpdir/client.crt \
	--private-key=$tmpdir/client.pem \
	--timeout=2 \
	"https://localhost:$HTTP_PORT/cas/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

# Unauthenticated read should fail.
if wget --inet4-only -d -O - --ca-certificate=$tmpdir/server.crt \
	--timeout=2 \
	"https://localhost:$HTTP_PORT/cas/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
then
	echo "Error: expected unauthenticated read to fail"
	kill -9 $server_pid
	exit 1
fi

# Run without auth, expect no access.
bazel run //utils/grpcreadclient -- -server-addr localhost:9092

# Run with auth, expect full access.
bazel run //utils/grpcreadclient -- -server-addr localhost:9092 \
	-ca-cert-file "$tmpdir/ca.crt" \
	-client-cert-file "$tmpdir/client.crt" \
	-client-key-file "$tmpdir/client.key"

# Clean up...

kill -9 $server_pid
rm -rf "$tmpdir"
