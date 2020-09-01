# This is a two-stage Docker build:
# https://docs.docker.com/develop/develop-images/multistage-build

#
# Build container
FROM golang:1.15.1 AS builder

# overwrite GOARCH with --build-arg GOARCH=arm64 when invoking docker build
ARG GOARCH="amd64"

WORKDIR /src
COPY . .
RUN ./linux-build.sh

#
# Runtime container
FROM alpine:latest
WORKDIR /root
EXPOSE 80
COPY --from=0 /src/bazel-remote .
ENTRYPOINT ["./bazel-remote", "--port=80", "--dir=/data"]
