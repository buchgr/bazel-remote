#!/usr/bin/env bash

#set -x
set -e
set -u
set -o pipefail

SRC_ROOT=$(dirname "$0")/..
SRC_ROOT=$(realpath "$SRC_ROOT")
cd "$SRC_ROOT"

echo -n "Building test binary (no cache): "
ti=$(date +%s)
bazel build //:bazel-remote 2> /dev/null
tf=$(date +%s)
duration=$((tf - ti))
echo "${duration}s"

cp -f bazel-bin/linux_amd64_static_pure_stripped/bazel-remote ./

echo "Starting test cache"
test_cache_dir=./bazel-remote-tmp-cache
rm -rf $test_cache_dir
./bazel-remote --max_size 1 --dir $test_cache_dir --port 8082 \
	> log.stdout 2> log.stderr &
test_cache_pid=$!
echo "Test cache pid: $test_cache_pid"

bazel clean 2> /dev/null

echo -n "Build with cold cache (HTTP): "
ti=$(date +%s)
bazel build //:bazel-remote --remote_cache=http://127.0.0.1:8082 \
	2> http_cold
tf=$(date +%s)
duration=$(expr $tf - $ti)
echo "${duration}s"
grep process http_cold

bazel clean 2> /dev/null

echo -n "Build with hot cache (HTTP): "
ti=$(date +%s)
bazel build //:bazel-remote --remote_cache=http://127.0.0.1:8082 \
	2> http_hot
tf=$(date +%s)
duration=$(expr $tf - $ti)
echo "${duration}s"
grep process http_hot

echo "Restarting test cache"
kill -9 $test_cache_pid
sleep 1
rm -rf $test_cache_dir
./bazel-remote --max_size 1 --dir $test_cache_dir --port 8082 \
	> log.stdout 2> log.stderr &
test_cache_pid=$!
echo "Test cache pid: $test_cache_pid"
bazel clean 2> /dev/null

echo -n "Build with cold cache (gRPC): "
ti=$(date +%s)
bazel build //:bazel-remote --remote_cache=grpc://127.0.0.1:9092 \
	2> grpc_cold
tf=$(date +%s)
duration=$(expr $tf - $ti)
echo "${duration}s"
grep process grpc_cold

bazel clean 2> /dev/null

echo -n "Build with hot cache (gRPC): "
ti=$(date +%s)
bazel build //:bazel-remote --remote_cache=grpc://127.0.0.1:9092 \
	2> grpc_hot
tf=$(date +%s)
duration=$(expr $tf - $ti)
echo "${duration}s"
grep process grpc_hot

echo "Done"
