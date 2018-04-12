#!/bin/bash

set -euxo pipefail

CGO_ENABLED=0 GOOS=linux go build -a -ldflags '-extldflags "-static"' .
