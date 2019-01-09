#!/bin/bash

set -euxo pipefail

VERSION_TAG="$(git rev-parse HEAD)"
VERSION_LINK_FLAG="github.com/buchgr/bazel-remote/server.GitCommit=${VERSION_TAG}"

CGO_ENABLED=0 GOOS=linux go build -a -ldflags "-extldflags '-static' -X ${VERSION_LINK_FLAG}" .