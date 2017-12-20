#!/bin/bash

# Build static binary

CGO_ENABLED=0 GOOS=linux go build -a -ldflags '-extldflags "-static"' .
