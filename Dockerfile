# This is a two-stage Docker build:
# https://docs.docker.com/develop/develop-images/multistage-build/#before-multi-stage-builds

# Build container
FROM golang:1.10 AS builder
WORKDIR /go/src/github.com/buchgr/bazel-remote
COPY . .
RUN ./linux-build.sh

# Runtime container
FROM alpine:latest
WORKDIR /root
EXPOSE 80
COPY --from=0 /go/src/github.com/buchgr/bazel-remote/bazel-remote .
ENTRYPOINT ["./bazel-remote", "--port=80", "--dir=/data"]
CMD ["--max_size=5"]
