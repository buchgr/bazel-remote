# Build stage
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src

# Copy go.mod and go.sum first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags "-s -w -X main.gitCommit=${VERSION}" \
    -o /bazel-remote .

# Final stage
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tini

COPY --from=builder /bazel-remote /bazel-remote

# Run as non-root user
RUN adduser -D -u 65532 bazel-remote
USER bazel-remote

ENTRYPOINT ["/sbin/tini", "--", "/bazel-remote"]
