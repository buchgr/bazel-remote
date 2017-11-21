#!/bin/bash

go run generate_buildfile.go
mkdir -p $(pwd)/cache

go install ...
$GOPATH/bin/bazel-remote -port 9191 -dir "$(pwd)/cache" -max_size 1 & export CACHEPID=$!

bazel --host_jvm_args=-Dbazel.DigestFunction=sha256 build \
--spawn_strategy=remote --strategy=Javac=remote --genrule_strategy=remote \
--remote_rest_cache=http://localhost:9191 ... &> /dev/null

du -hs cache

# Cleanup
kill -9 $CACHEPID
bazel --host_jvm_args=-Dbazel.DigestFunction=sha256 clean --expunge
rm -rf cache BUILD
