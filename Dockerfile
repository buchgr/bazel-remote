# This is a two-stage Docker build:
# https://docs.docker.com/develop/develop-images/multistage-build

#
# Build container
FROM golang:1.10 AS builder

# Install dep for vendoring
ENV GOLANG_DEP_URL https://github.com/golang/dep/releases/download/v0.4.1/dep-linux-amd64
RUN curl -L -o /usr/local/bin/dep ${GOLANG_DEP_URL} && chmod +x /usr/local/bin/dep

WORKDIR /go/src/github.com/buchgr/bazel-remote
COPY . .
RUN dep ensure
RUN ./linux-build.sh

#
# Runtime container
FROM alpine:latest
WORKDIR /root
EXPOSE 80
COPY --from=0 /go/src/github.com/buchgr/bazel-remote/bazel-remote .
ENV BAZEL_REMOTE_DIR=/data \
    BAZEL_REMOTE_PORT=80
ENTRYPOINT ["./bazel-remote"]
