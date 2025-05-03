#!/bin/bash

set -euxo pipefail

GOARCH=${GOARCH:-amd64}

GIT_COMMIT_TAG="$(git rev-parse HEAD)"
GIT_COMMIT_LINK_FLAG="main.gitCommit=${GIT_COMMIT_TAG}"

GIT_DESCRIBE_TAG="$(git describe --tags || true)"
GIT_DESCRIBE_LINK_FLAG="main.gitDescribe=${GIT_DESCRIBE_TAG}"

CGO_ENABLED=1 GOOS=linux GOARCH=$GOARCH go build -a -ldflags "-X \"${GIT_COMMIT_LINK_FLAG}\" -X \"${GIT_DESCRIBE_LINK_FLAG}\"" .
