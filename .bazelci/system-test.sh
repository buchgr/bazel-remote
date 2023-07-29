#!/usr/bin/env bash

#set -x
set -e
set -u
set -o pipefail

SRC_ROOT=$(dirname "$0")/..
SRC_ROOT=$(realpath "$SRC_ROOT")
cd "$SRC_ROOT"

HTTP_PORT=8082

min_acceptable_hit_rate=95
overall_result=success

EXTRA_FLAGS=""
EXTRA_FLAGS_DESC=""
[ -n "$EXTRA_FLAGS" ] && EXTRA_FLAGS_DESC="(with $EXTRA_FLAGS)"

summary=""

### Begin fakes3 setup.

# Copy the binary somewhere known, so we can run it manually.
bazel run --run_under "cp -f " //utils/fakes3 $(pwd)/
./fakes3 &
fakes3_pid=$!
sleep 2

### End fakes3 setup.

wait_for_startup() {
	server_pid="$1"
	running=false

	for i in $(seq 1 10)
	do
		sleep 1

		ps -p $server_pid > /dev/null || break

		if wget --inet4-only -d -O - "http://127.0.0.1:$HTTP_PORT/status"
		then
			return
		fi
	done

	echo "Error: bazel-remote took too long to start"
	kill -9 "$server_pid"
	exit 1
}

echo -n "Building test binary (no cache): "
ti=$(date +%s)
bazel build //:bazel-remote 2> /dev/null
tf=$(date +%s)
duration=$((tf - ti))
echo "${duration}s"

# Copy the binary somewhere known, so we can run it manually.
bazel run --run_under "cp -f " //:bazel-remote $(pwd)/

test_cache_dir=./bazel-remote-tmp-cache

echo "Starting test cache (with --enable_endpoint_metrics)"
rm -rf $test_cache_dir
./bazel-remote --max_size 1 --dir "$test_cache_dir" --http_address "0.0.0.0:$HTTP_PORT" \
	--enable_endpoint_metrics &
test_cache_pid=$!
echo "Test cache pid: $test_cache_pid"
wait_for_startup "$test_cache_pid"
wget -O - http://127.0.0.1:$HTTP_PORT/cas/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
kill -9 $test_cache_pid
sleep 1
echo "Done testing --enable_endpoint_metrics"

echo "Starting test cache $EXTRA_FLAGS_DESC"
rm -rf $test_cache_dir
./bazel-remote --max_size 1 --dir "$test_cache_dir" --http_address "0.0.0.0:$HTTP_PORT" $EXTRA_FLAGS \
	--s3.endpoint 127.0.0.1:9000 \
	--s3.bucket bazel-remote \
	--s3.prefix files \
	--s3.auth_method access_key \
	--s3.access_key_id fakes3id \
	--s3.secret_access_key fakes3key \
	--s3.disable_ssl \
	--s3.update_timestamps \
	> log.stdout 2> log.stderr &
test_cache_pid=$!
echo "Test cache pid: $test_cache_pid"
wait_for_startup "$test_cache_pid"

bazel clean 2> /dev/null

echo -n "Build with cold cache (HTTP, populating fakes3): "
ti=$(date +%s)
bazel build //:bazel-remote "--remote_cache=http://127.0.0.1:$HTTP_PORT" \
	2> http_cold
tf=$(date +%s)
duration=$(expr $tf - $ti)
echo "${duration}s"
grep process http_cold

bazel clean 2> /dev/null

echo "Restarting test cache $EXTRA_FLAGS_DESC"
kill -9 $test_cache_pid
sleep 1
./bazel-remote --max_size 1 --dir $test_cache_dir --http_address "0.0.0.0:$HTTP_PORT" $EXTRA_FLAGS \
	> log.stdout 2> log.stderr &
test_cache_pid=$!
echo "Test cache pid: $test_cache_pid"
wait_for_startup "$test_cache_pid"

testsection="hot HTTP"
echo -n "Build with hot cache ($testsection): "
ti=$(date +%s)
bazel build //:bazel-remote "--remote_cache=http://127.0.0.1:$HTTP_PORT" \
	--execution_log_json_file=http_hot.json \
	2> http_hot
tf=$(date +%s)
duration=$(expr $tf - $ti)
echo "${duration}s"
grep process http_hot
hits=$(grep -c '"remoteCacheHit": true,' http_hot.json || true) # TODO: replace these with jq one day.
misses=$(grep -c '"remoteCacheHit": false,' http_hot.json || true)
hit_rate=$(awk -vhits=$hits -vmisses=$misses 'BEGIN { printf "%0.2f", hits*100/(hits+misses) }' </dev/null)
result=$(awk -vhit_rate=$hit_rate -vmin=$min_acceptable_hit_rate 'BEGIN {if (hit_rate >= min) print "success" ; else print "failure";}' < /dev/null)
[ "$result" = "failure" ] && overall_result=failure
echo "hit rate: ${hit_rate}% (hits: $hits misses: $misses) => $result"
summary+="\n$testsection: hit rate: ${hit_rate}% (hits: $hits misses: $misses) => $result"

echo "Restarting test cache $EXTRA_FLAGS_DESC"
kill -9 $test_cache_pid
sleep 1
rm -rf $test_cache_dir
./bazel-remote --max_size 1 --dir $test_cache_dir --http_address "0.0.0.0:$HTTP_PORT" $EXTRA_FLAGS \
	--s3.endpoint 127.0.0.1:9000 \
	--s3.bucket bazel-remote \
	--s3.prefix files \
	--s3.auth_method access_key \
	--s3.access_key_id fakes3id \
	--s3.secret_access_key fakes3key \
	--s3.disable_ssl \
	--s3.update_timestamps \
	> log.stdout 2> log.stderr &
test_cache_pid=$!
echo "Test cache pid: $test_cache_pid"
wait_for_startup "$test_cache_pid"

bazel clean 2> /dev/null

testsection="cold HTTP, hot fakes3"
echo -n "Build with hot cache ($testsection): "
ti=$(date +%s)
bazel build //:bazel-remote "--remote_cache=http://127.0.0.1:$HTTP_PORT" \
	--execution_log_json_file=http_hot_fakes3.json --verbose_failures \
	2> http_hot
tf=$(date +%s)
duration=$(expr $tf - $ti)
echo "${duration}s"
grep process http_hot
hits=$(grep -c '"remoteCacheHit": true,' http_hot_fakes3.json || true) # TODO: replace these with jq one day.
misses=$(grep -c '"remoteCacheHit": false,' http_hot_fakes3.json || true)
hit_rate=$(awk -vhits=$hits -vmisses=$misses 'BEGIN { printf "%0.2f", hits*100/(hits+misses) }' </dev/null)
result=$(awk -vhit_rate=$hit_rate -vmin=$min_acceptable_hit_rate 'BEGIN {if (hit_rate >= min) print "success" ; else print "failure";}' < /dev/null)
[ "$result" = "failure" ] && overall_result=failure
echo "hit rate: ${hit_rate}% (hits: $hits misses: $misses) => $result"
summary+="\n$testsection: hit rate: ${hit_rate}% (hits: $hits misses: $misses) => $result"

echo "Restarting test cache $EXTRA_FLAGS_DESC"
kill -9 $test_cache_pid
sleep 1
rm -rf $test_cache_dir
./bazel-remote --max_size 1 --dir $test_cache_dir --http_address "0.0.0.0:$HTTP_PORT" $EXTRA_FLAGS \
	> log.stdout 2> log.stderr &
test_cache_pid=$!
echo "Test cache pid: $test_cache_pid"
wait_for_startup "$test_cache_pid"

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
hit_rate=$(awk -vhits=$hits -vmisses=$misses 'BEGIN { printf "%0.2f", hits*100/(hits+misses) }' </dev/null)
result=$(awk -vhit_rate=$hit_rate -vmin=$min_acceptable_hit_rate 'BEGIN {if (hit_rate >= min) print "success" ; else print "failure";}' < /dev/null)
[ "$result" = "failure" ] && overall_result=failure
echo "hit rate: ${hit_rate}% (hits: $hits misses: $misses) => $result"
summary+="\n$testsection: hit rate: ${hit_rate}% (hits: $hits misses: $misses) => $result"

echo "Restarting test cache $EXTRA_FLAGS_DESC"
kill -9 $test_cache_pid
sleep 1
rm -rf $test_cache_dir
./bazel-remote --max_size 1 --dir $test_cache_dir --http_address "0.0.0.0:$HTTP_PORT" $EXTRA_FLAGS \
	--s3.endpoint 127.0.0.1:9000 \
	--s3.bucket bazel-remote \
	--s3.prefix files \
	--s3.auth_method access_key \
	--s3.access_key_id fakes3id \
	--s3.secret_access_key fakes3key \
	--s3.disable_ssl \
	--s3.update_timestamps \
	> log.stdout 2> log.stderr &
test_cache_pid=$!
echo "Test cache pid: $test_cache_pid"
wait_for_startup "$test_cache_pid"

bazel clean 2> /dev/null

testsection="cold gRPC, hot fakes3"
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
hit_rate=$(awk -vhits=$hits -vmisses=$misses 'BEGIN { printf "%0.2f", hits*100/(hits+misses) }' </dev/null)
result=$(awk -vhit_rate=$hit_rate -vmin=$min_acceptable_hit_rate 'BEGIN {if (hit_rate >= min) print "success" ; else print "failure";}' < /dev/null)
[ "$result" = "failure" ] && overall_result=failure
echo "hit rate: ${hit_rate}% (hits: $hits misses: $misses) => $result"
summary+="\n$testsection: hit rate: ${hit_rate}% (hits: $hits misses: $misses) => $result"

kill -9 $test_cache_pid

echo "Stopping fakes3"
kill -9 $fakes3_pid

echo -e "\n##########"
echo -e "$summary\n"
echo "Done ($overall_result)"
echo "##########"

if [ "$overall_result" != "success" ]
then
	exit 1
fi
