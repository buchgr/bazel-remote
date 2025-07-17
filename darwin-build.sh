#!/bin/bash

set -euxo pipefail

GOARCH=${GOARCH:-arm64}

GIT_COMMIT=$(git rev-parse HEAD)
GIT_TAGS=$(git tag --points-at HEAD | sort -h | paste -sd "," -)

GIT_COMMIT_LINK_FLAG="main.gitCommit=${GIT_COMMIT}"
GIT_TAGS_LINK_FLAG="main.gitTags=${GIT_TAGS}"

CGO_ENABLED=1 GOOS=darwin GOARCH=$GOARCH go build -a -ldflags "-X \"${GIT_COMMIT_LINK_FLAG}\" -X \"${GIT_TAGS_LINK_FLAG}\"" .
