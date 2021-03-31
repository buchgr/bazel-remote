#!/usr/bin/env bash

#set -x
set -e
set -u
set -o pipefail

SRC_ROOT=$(dirname "$0")/..
SRC_ROOT=$(realpath "$SRC_ROOT")
cd "$SRC_ROOT"

HTTP_PORT=8089

tmpdir=$(mktemp -d bazel-remote-mtls-tests.XXXXXXX --tmpdir=${TMPDIR:-/tmp})

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

	# Self-signed server certificate:
	openssl x509 -req -passin pass:1111 -days 358000 -in "$tmpdir/server.csr" \
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

echo "Starting bazel-remote..."
./bazel-remote --dir "$tmpdir/cache" --max_size 1 --port "$HTTP_PORT" \
	--tls_cert_file "$tmpdir/server.crt" \
	--tls_key_file "$tmpdir/server.key" \
	--tls_ca_file "$tmpdir/ca.crt" > "$tmpdir/bazel-remote.log" 2>&1 &
server_pid=$!

# Wait a bit for the server start up...

running=false
for i in $(seq 1 10)
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

bazel clean
bazel build //:bazel-remote --remote_cache=grpcs://localhost:9092 \
	--tls_certificate "$tmpdir/ca.crt" \
	--tls_client_certificate "$tmpdir/client.crt" \
	--tls_client_key "$tmpdir/client.pem"


kill -9 $server_pid
rm -rf "$tmpdir"
