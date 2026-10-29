package server

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	auth "github.com/abbot/go-http-auth"
	"github.com/buchgr/bazel-remote/v2/utils/authcache"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	grpc_status "google.golang.org/grpc/status"
)

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s contextServerStream) Context() context.Context { return s.ctx }

func TestGrpcBasicAuthInterceptors(t *testing.T) {
	// `htpasswd -nbB -C 4 u correct-horse`
	path := filepath.Join(t.TempDir(), "htpasswd")
	err := os.WriteFile(path,
		[]byte("u:$2y$04$OdIHfkOiKIewtUfKmiCpBejXsSIdEBTwMuR6KcInCnrAn9Xy1UCui\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	secrets := auth.HtpasswdFileProvider(path)

	testGrpcBasicAuthInterceptors(t, NewGrpcBasicAuth(authcache.New(secrets), false))
}

func testGrpcBasicAuthInterceptors(t *testing.T, gba *GrpcBasicAuth) {
	const method = "/build.bazel.remote.execution.v2.ContentAddressableStorage/FindMissingBlobs"

	unary := func(fullMethod string, md metadata.MD) error {
		ctx := metadata.NewIncomingContext(context.Background(), md)
		_, err := gba.UnaryServerInterceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: fullMethod},
			func(ctx context.Context, req interface{}) (interface{}, error) { return nil, nil })
		return err
	}
	stream := func(fullMethod string, md metadata.MD) error {
		ss := contextServerStream{ctx: metadata.NewIncomingContext(context.Background(), md)}
		return gba.StreamServerInterceptor(nil, ss, &grpc.StreamServerInfo{FullMethod: fullMethod},
			func(srv interface{}, ss grpc.ServerStream) error { return nil })
	}
	basic := func(user, password string) metadata.MD {
		return metadata.Pairs("authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+password)))
	}
	authority := func(user, password string) metadata.MD {
		return metadata.Pairs(":authority", user+":"+password+"@localhost")
	}

	for name, call := range map[string]func(string, metadata.MD) error{"unary": unary, "stream": stream} {
		for _, tc := range []struct {
			name   string
			method string
			md     metadata.MD
			want   codes.Code
		}{
			{"basic header", method, basic("u", "correct-horse"), codes.OK},
			{"authority", method, authority("u", "correct-horse"), codes.OK},
			{"wrong password", method, basic("u", "wrong"), codes.Unauthenticated},
			{"wrong password in authority", method, authority("u", "wrong"), codes.Unauthenticated},
			{"unknown user", method, basic("nobody", "correct-horse"), codes.Unauthenticated},
			{"no credentials", method, metadata.Pairs("x-other", "1"), codes.Unauthenticated},
			{"health check without credentials", grpcHealthServiceName, metadata.Pairs("x-other", "1"), codes.OK},
		} {
			if got := grpc_status.Code(call(tc.method, tc.md)); got != tc.want {
				t.Errorf("%s, %s: code %v, want %v", name, tc.name, got, tc.want)
			}
		}
	}
}
