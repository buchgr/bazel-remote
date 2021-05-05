#!/usr/bin/env bash

set -v
set -e
set -u
set -o pipefail

SRC_ROOT=$(dirname "$0")/..
SRC_ROOT=$(realpath "$SRC_ROOT")
cd "$SRC_ROOT"

HTTP_PORT=8089
USER=topsecretusername
PASS=topsecretpassword

#export GRPC_GO_LOG_VERBOSITY_LEVEL=99
#export GRPC_GO_LOG_SEVERITY_LEVEL=info

tmpdir=$(mktemp -d bazel-remote-basic-auth-tests.XXXXXXX --tmpdir=${TMPDIR:-/tmp})

[ -e bazel-remote ] || ./linux-build.sh

# Generated with "htpasswd -b -c htpasswd $USER $PASS"
echo 'topsecretusername:$apr1$Ke2kcK4W$EyueqiHyoqhwXcpiEGNyJ1' \
	> "$tmpdir/htpasswd"

echo "Starting bazel-remote, allowing unauthenticated reads..."
./bazel-remote --dir "$tmpdir/cache" --max_size 1 --port "$HTTP_PORT" \
	--htpasswd_file "$tmpdir/htpasswd" \
	--allow_unauthenticated_reads > "$tmpdir/bazel-remote.log" 2>&1 &
server_pid=$!

# Wait a bit for the server start up...

running=false
for i in $(seq 1 20)
do
	sleep 1

	if wget --inet4-only -d -O - --timeout=2 \
		--http-user "$USER" --http-password "$PASS" \
		"http://localhost:$HTTP_PORT/status"
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
wget --inet4-only -d -O - \
	--http-user "$USER" --http-password "$PASS" \
	http://localhost:$HTTP_PORT/status

# Unauthenticated read.
wget --inet4-only -d -O - http://localhost:$HTTP_PORT/status

# Run without auth, expect readonly access.
bazel run //utils/grpcreadclient -- -server-addr localhost:9092 \
	-reads-should-work

# Run with auth, expect read-write access.
bazel run //utils/grpcreadclient -- -server-addr localhost:9092 \
	-basic-auth-user "$USER" -basic-auth-pass "$PASS"

# Authenticated build, populate the cache.
bazel clean
bazel build //:bazel-remote --remote_cache=grpc://$USER:$PASS@localhost:9092

# Unauthenticated build, don't attempt to upload (gRPC).
bazel clean
bazel build //:bazel-remote --remote_cache=grpc://localhost:9092 \
	--noremote_upload_local_results

# Unauthenticated build, don't attempt to upload (HTTP).
bazel clean
bazel build //:bazel-remote --remote_cache=http://localhost:$HTTP_PORT \
	--noremote_upload_local_results

# Unauthenticated gRPC client, should fail to write, but the build
# should succeed.
bazel clean
bazel build //:bazel-remote --remote_cache=grpc://localhost:9092 \
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
	--htpasswd_file "$tmpdir/htpasswd" > "$tmpdir/bazel-remote-authenticated.log" 2>&1 &
server_pid=$!

# Wait a bit for the server start up...

running=false
for i in $(seq 1 20)
do
	sleep 1

	if wget --inet4-only -d -O - --timeout=2 \
		--http-user "$USER" --http-password "$PASS" \
		"http://localhost:$HTTP_PORT/status"
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
wget --inet4-only -d -O - --timeout=2 \
	--http-user "$USER" --http-password "$PASS" \
	"http://localhost:$HTTP_PORT/cas/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

# Unauthenticated read should fail.
if wget --inet4-only -d -O - --timeout=2 \
	"http://localhost:$HTTP_PORT/cas/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
then
	echo "Error: expected unauthenticated read to fail"
	kill -9 $server_pid
	exit 1
fi

# Run without auth, expect no access.
bazel run //utils/grpcreadclient -- -server-addr localhost:9092

# Run with auth, expect full access.
bazel run //utils/grpcreadclient -- -server-addr localhost:9092 \
	-basic-auth-user "$USER" -basic-auth-pass "$PASS"

# Clean up...

kill -9 $server_pid
rm -rf "$tmpdir"
