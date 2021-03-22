#!/usr/bin/env bash

set -v
set -x
set -e
set -u
set -o pipefail

SRC_ROOT=$(dirname "$0")/..
SRC_ROOT=$(realpath "$SRC_ROOT")
cd "$SRC_ROOT"

tmpdir=$(mktemp -d bazel-remote-migration-tests.XXXXXXX --tmpdir=${TMPDIR:-/tmp})

if [ ! -e bazel-remote-1.3.1-linux-x86_64 ]
then
	# Download a v1 bazel-remote build.
	wget https://github.com/buchgr/bazel-remote/releases/download/v1.3.1/bazel-remote-1.3.1-linux-x86_64
	chmod +x bazel-remote-1.3.1-linux-x86_64
fi

# Build a v2 bazel-remote binary
[ -e bazel-remote ] || ./linux-build.sh

# Run the v1 bazel-remote.
./bazel-remote-1.3.1-linux-x86_64 --dir "$tmpdir/cache" --max_size 1 \
	--port 8089 > "$tmpdir/v1.log" 2>&1 &
server_pid=$!

# Let the server start up...
sleep 2

# Populate the cache.
bazel clean
bazel build //:bazel-remote --remote_cache=grpc://localhost:9092

if ! kill -0 "$server_pid"
then
	echo "Error: bazel-remote no longer running. Check $tmpdir/v1.log"
	exit 1
fi

# Stop the v1 bazel-remote.
kill -9 $server_pid
sleep 1

# Count the number of files in the cache.
find "$tmpdir/cache" -type f | sort > "$tmpdir/v1.files"
count_v1=$(wc -l < "$tmpdir/v1.files")

# Start the v2 bazel-remote.
./bazel-remote --dir "$tmpdir/cache" --max_size 1 \
	--port 8089 > "$tmpdir/v2.log" 2>&1 &
server_pid=$!

# Let the server start up...
sleep 5

if ! kill -0 "$server_pid"
then
	echo "Error: bazel-remote no longer running. Check $tmpdir/v2.log"
	exit 1
fi

kill -9 $server_pid

# Count the number of files in the cache.
find "$tmpdir/cache" -type f | sort > "$tmpdir/v2.files"
count_v2=$(wc -l < "$tmpdir/v2.files")

if [ "$count_v1" != "$count_v2" ]
then
	echo "Error: v1 directory contained \"$count_v1\" files, but migrated v2 directory contains \"$count_v2\""

	rm -rf "$tmpdir"
	exit 1
fi

# Clean up...
rm -rf "$tmpdir"
