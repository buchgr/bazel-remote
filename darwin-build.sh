#!/bin/bash

set -euxo pipefail

GOARCH=${GOARCH:-arm64}

VERSION_TAG="$(git rev-parse HEAD)"
VERSION_LINK_FLAG="main.gitCommit=${VERSION_TAG}"

CGO_ENABLED=1 GOOS=darwin GOARCH=$GOARCH go build -a -ldflags "--X ${VERSION_LINK_FLAG}" .
