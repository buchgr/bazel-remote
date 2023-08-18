package server

import (
	"context"
	"encoding/base64"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	grpc_status "google.golang.org/grpc/status"

	auth "github.com/abbot/go-http-auth"
)

var (
	errNoMetadata = grpc_status.Error(codes.Unauthenticated,
		"no metadata found")
	errNoAuthMetadata = grpc_status.Error(codes.Unauthenticated,
		"no authentication metadata found")
	errAccessDenied = grpc_status.Error(codes.Unauthenticated,
		"access denied")
)

// GrpcBasicAuth wraps an auth.SecretProvider, and provides gRPC interceptors
// that verify that requests can be authenticated using HTTP basic auth.
type GrpcBasicAuth struct {
	secrets                      auth.SecretProvider
	allowUnauthenticatedReadOnly bool
}

// NewGrpcBasicAuth returns a GrpcBasicAuth that wraps the given
// auth.SecretProvider.
func NewGrpcBasicAuth(secrets auth.SecretProvider, allowUnauthenticatedReadOnly bool) *GrpcBasicAuth {
	return &GrpcBasicAuth{
		secrets:                      secrets,
		allowUnauthenticatedReadOnly: allowUnauthenticatedReadOnly,
	}
}

// StreamServerInterceptor verifies that each request can be authenticated
// using HTTP basic auth, or is allowed without authentication.
func (b *GrpcBasicAuth) StreamServerInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {

	// Always allow health service requests.
	if info.FullMethod == grpcHealthServiceName {
		return handler(srv, ss)
	}

	if b.allowUnauthenticatedReadOnly {
		_, ro := readOnlyMethods[info.FullMethod]
		if ro {
			return handler(srv, ss)
		}
	}

	username, password, err := getLogin(ss.Context())
	if err != nil {
		return err
	}
	if username == "" || password == "" {
		return errAccessDenied
	}

	if !b.allowed(username, password) {
		return errAccessDenied
	}

	return handler(srv, ss)
}

// UnaryServerInterceptor verifies that each request can be authenticated
// using HTTP basic auth, or is allowed without authenticated.
func (b *GrpcBasicAuth) UnaryServerInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {

	// Always allow health service requests.
	if info.FullMethod == grpcHealthServiceName {
		return handler(ctx, req)
	}

	if b.allowUnauthenticatedReadOnly {
		_, ro := readOnlyMethods[info.FullMethod]
		if ro {
			return handler(ctx, req)
		}
	}

	username, password, err := getLogin(ctx)
	if err != nil {
		return nil, err
	}
	if username == "" || password == "" {
		return nil, errAccessDenied
	}

	if !b.allowed(username, password) {
		return nil, errAccessDenied
	}

	return handler(ctx, req)
}

func getLogin(ctx context.Context) (username, password string, err error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", "", errNoMetadata
	}

	for k, v := range md {
		if k == ":authority" && len(v) > 0 {
			// When bazel is run with --remote_cache=grpc://user:pass@address/"
			// the value looks like "user:pass@address".
			fields := strings.SplitN(v[0], ":", 2)
			if len(fields) < 2 {
				continue
			}
			username = fields[0]

			fields = strings.SplitN(fields[1], "@", 2)
			if len(fields) < 2 {
				continue
			}
			password = fields[0]

			return username, password, nil
		}

		if k == "authorization" && len(v) > 0 && strings.HasPrefix(v[0], "Basic ") {
			// When bazel-remote is run with --grpc_proxy.url=grpc://user:pass@address/"
			// the value looks like "Basic <base64(user:pass)>".
			auth, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v[0], "Basic "))
			if err != nil {
				continue
			}
			parts := strings.SplitN(string(auth), ":", 2)
			if len(parts) < 2 {
				continue
			}

			username, password = parts[0], parts[1]

			return username, password, nil
		}
	}

	return "", "", errNoAuthMetadata
}

func (b *GrpcBasicAuth) allowed(username, password string) bool {
	ignoredRealm := ""
	requiredSecret := b.secrets(username, ignoredRealm)
	if requiredSecret == "" {
		return false // User does not exist.
	}

	return auth.CheckSecret(password, requiredSecret)
}
