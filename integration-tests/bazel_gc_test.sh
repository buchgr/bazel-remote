#!/bin/bash

go run generate_buildfile.go
mkdir -p $(pwd)/cache

go install github.com/buchgr/bazel-remote
$GOPATH/bin/bazel-remote -port 9191 -dir "$(pwd)/cache" -max_size 1 & export CACHEPID=$!

bazel build \
--spawn_strategy=remote --strategy=Javac=remote --genrule_strategy=remote \
--remote_rest_cache=http://localhost:9191 ... &> /dev/null

du -hs cache

# Cleanup
kill -9 $CACHEPID
bazel clean --expunge
rm -rf cache BUILD
