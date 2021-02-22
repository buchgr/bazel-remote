#!/usr/bin/env bash

#set -x
set -e
set -u
set -o pipefail

SRC_ROOT=$(dirname "$0")/..
SRC_ROOT=$(realpath "$SRC_ROOT")
cd "$SRC_ROOT"

min_acceptable_hit_rate=95
overall_result=success

summary=""

### Begin minio setup.

if [ ! -e minio ]
then
	wget https://dl.min.io/server/minio/release/linux-amd64/minio
	chmod +x minio
fi
if [ ! -e mc ]
then
	wget https://dl.min.io/client/mc/release/linux-amd64/mc
	chmod +x mc
fi

rm -rf miniocachedir
killall -HUP minio && sleep 2 || true
./minio server miniocachedir &
minio_pid=$!
sleep 2
./mc config host add myminio http://127.0.0.1:9000 minioadmin minioadmin
./mc mb myminio/bazel-remote

### End minio setup.

echo -n "Building test binary (no cache): "
ti=$(date +%s)
bazel build //:bazel-remote 2> /dev/null
tf=$(date +%s)
duration=$((tf - ti))
echo "${duration}s"

# Copy the binary somewhere known, so we can run it manually.
bazel run --run_under "cp -f " //:bazel-remote $(pwd)/

echo "Starting test cache"
test_cache_dir=./bazel-remote-tmp-cache
rm -rf $test_cache_dir
./bazel-remote --max_size 1 --dir $test_cache_dir --port 8082 \
	--s3.endpoint 127.0.0.1:9000 \
	--s3.bucket bazel-remote \
	--s3.prefix files \
	--s3.access_key_id minioadmin \
	--s3.secret_access_key minioadmin \
	--s3.disable_ssl \
	> log.stdout 2> log.stderr &
test_cache_pid=$!
echo "Test cache pid: $test_cache_pid"

bazel clean 2> /dev/null

echo -n "Build with cold cache (HTTP, populating minio): "
ti=$(date +%s)
bazel build //:bazel-remote --remote_cache=http://127.0.0.1:8082 \
	2> http_cold
tf=$(date +%s)
duration=$(expr $tf - $ti)
echo "${duration}s"
grep process http_cold

bazel clean 2> /dev/null

echo "Restarting test cache"
kill -9 $test_cache_pid
sleep 1
./bazel-remote --max_size 1 --dir $test_cache_dir --port 8082 \
	> log.stdout 2> log.stderr &
test_cache_pid=$!
echo "Test cache pid: $test_cache_pid"

testsection="hot HTTP"
echo -n "Build with hot cache ($testsection): "
ti=$(date +%s)
bazel build //:bazel-remote --remote_cache=http://127.0.0.1:8082 \
	--execution_log_json_file=http_hot.json \
	2> http_hot
tf=$(date +%s)
duration=$(expr $tf - $ti)
echo "${duration}s"
grep process http_hot
hits=$(grep -c '"remoteCacheHit": true,' http_hot.json || true) # TODO: replace these with jq one day.
misses=$(grep -c '"remoteCacheHit": false,' http_hot.json || true)
hit_rate=$(echo -e "scale=2\n$hits * 100 / ($hits + $misses)" | bc)
result=$(echo "$hit_rate >= $min_acceptable_hit_rate" | bc | sed -e s/1/success/ -e s/0/failure/)
[ "$result" = "failure" ] && overall_result=failure
echo "hit rate: ${hit_rate}% (hits: $hits misses: $misses) => $result"
summary+="\n$testsection: hit rate: ${hit_rate}% (hits: $hits misses: $misses) => $result"

echo "Restarting test cache"
kill -9 $test_cache_pid
sleep 1
rm -rf $test_cache_dir
./bazel-remote --max_size 1 --dir $test_cache_dir --port 8082 \
	--s3.endpoint 127.0.0.1:9000 \
	--s3.bucket bazel-remote \
	--s3.prefix files \
	--s3.access_key_id minioadmin \
	--s3.secret_access_key minioadmin \
	--s3.disable_ssl \
	> log.stdout 2> log.stderr &
test_cache_pid=$!
echo "Test cache pid: $test_cache_pid"

bazel clean 2> /dev/null

testsection="cold HTTP, hot minio"
echo -n "Build with hot cache ($testsection): "
ti=$(date +%s)
bazel build //:bazel-remote --remote_cache=http://127.0.0.1:8082 \
	--execution_log_json_file=http_hot_minio.json \
	2> http_hot
tf=$(date +%s)
duration=$(expr $tf - $ti)
echo "${duration}s"
grep process http_hot
hits=$(grep -c '"remoteCacheHit": true,' http_hot_minio.json || true) # TODO: replace these with jq one day.
misses=$(grep -c '"remoteCacheHit": false,' http_hot_minio.json || true)
hit_rate=$(echo -e "scale=2\n$hits * 100 / ($hits + $misses)" | bc)
result=$(echo "$hit_rate >= $min_acceptable_hit_rate" | bc | sed -e s/1/success/ -e s/0/failure/)
[ "$result" = "failure" ] && overall_result=failure
echo "hit rate: ${hit_rate}% (hits: $hits misses: $misses) => $result"
summary+="\n$testsection: hit rate: ${hit_rate}% (hits: $hits misses: $misses) => $result"

echo "Stopping minio"
kill -9 $minio_pid
sleep 1

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

testsection="hot gRPC"
echo -n "Build with hot cache ($testsection): "
ti=$(date +%s)
bazel build //:bazel-remote --remote_cache=grpc://127.0.0.1:9092 \
	--execution_log_json_file=grpc_hot.json \
	2> grpc_hot
tf=$(date +%s)
duration=$(expr $tf - $ti)
echo "${duration}s"
grep process grpc_hot
hits=$(grep -c '"remoteCacheHit": true,' grpc_hot.json || true) # TODO: replace these with jq one day.
misses=$(grep -c '"remoteCacheHit": false,' grpc_hot.json || true)
hit_rate=$(echo -e "scale=2\n$hits * 100 / ($hits + $misses)" | bc)
result=$(echo "$hit_rate >= $min_acceptable_hit_rate" | bc | sed -e s/1/success/ -e s/0/failure/)
[ "$result" = "failure" ] && overall_result=failure
echo "hit rate: ${hit_rate}% (hits: $hits misses: $misses) => $result"
summary+="\n$testsection: hit rate: ${hit_rate}% (hits: $hits misses: $misses) => $result"

kill -9 $test_cache_pid

echo -e "\n##########"
echo -e "$summary\n"
echo "Done ($overall_result)"
echo "##########"

[ "$overall_result" != "success" ] && exit 1
