package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	_ "google.golang.org/grpc/encoding/gzip" // Register gzip support.
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	asset "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/asset/v1"
	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
	"github.com/buchgr/bazel-remote/v2/genproto/build/bazel/semver"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk"
	"github.com/buchgr/bazel-remote/v2/utils/validate"

	_ "github.com/mostynb/go-grpc-compression/snappy" // Register snappy
	_ "github.com/mostynb/go-grpc-compression/zstd"   // and zstd support.
)

const (
	hashKeyLength = 64
	emptySha256   = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

const grpcHealthServiceName = "/grpc.health.v1.Health/Check"

type grpcServer struct {
	cache               disk.Cache
	accessLogger        cache.Logger
	errorLogger         cache.Logger
	depsCheck           bool
	mangleACKeys        bool
	maxCasBlobSizeBytes int64
}

var readOnlyMethods = map[string]struct{}{
	"/build.bazel.remote.execution.v2.ActionCache/GetActionResult":                {},
	"/build.bazel.remote.execution.v2.ContentAddressableStorage/FindMissingBlobs": {},
	"/build.bazel.remote.execution.v2.ContentAddressableStorage/BatchReadBlobs":   {},
	"/build.bazel.remote.execution.v2.ContentAddressableStorage/GetTree":          {},
	"/build.bazel.remote.execution.v2.Capabilities/GetCapabilities":               {},
	"/google.bytestream.ByteStream/Read":                                          {},
}

// ListenAndServeGRPC creates a new gRPC server and listens on the given
// address. This function either returns an error quickly, or triggers a
// blocking call to https://godoc.org/google.golang.org/grpc#Server.Serve
func ListenAndServeGRPC(
	srv *grpc.Server,
	network string, addr string,
	validateACDeps bool,
	mangleACKeys bool,
	enableRemoteAssetAPI bool,
	maxCasBlobSizeBytes int64,
	c disk.Cache, a cache.Logger, e cache.Logger) error {

	listener, err := net.Listen(network, addr)
	if err != nil {
		return err
	}

	return ServeGRPC(listener, srv, validateACDeps, mangleACKeys, enableRemoteAssetAPI, maxCasBlobSizeBytes, c, a, e)
}

func ServeGRPC(l net.Listener, srv *grpc.Server,
	validateACDepsCheck bool,
	mangleACKeys bool,
	enableRemoteAssetAPI bool,
	maxCasBlobSizeBytes int64,
	c disk.Cache, a cache.Logger, e cache.Logger) error {

	s := &grpcServer{
		cache:               c,
		accessLogger:        a,
		errorLogger:         e,
		depsCheck:           validateACDepsCheck,
		mangleACKeys:        mangleACKeys,
		maxCasBlobSizeBytes: maxCasBlobSizeBytes,
	}
	pb.RegisterActionCacheServer(srv, s)
	pb.RegisterCapabilitiesServer(srv, s)
	pb.RegisterContentAddressableStorageServer(srv, s)
	bytestream.RegisterByteStreamServer(srv, s)
	if enableRemoteAssetAPI {
		asset.RegisterFetchServer(srv, s)
	}

	h := health.NewServer()
	grpc_health_v1.RegisterHealthServer(srv, h)
	h.SetServingStatus(grpcHealthServiceName, grpc_health_v1.HealthCheckResponse_SERVING)

	return srv.Serve(l)
}

// Capabilities interface:

func (s *grpcServer) GetCapabilities(ctx context.Context,
	req *pb.GetCapabilitiesRequest) (*pb.ServerCapabilities, error) {

	// Instance name is currently ignored.

	resp := pb.ServerCapabilities{
		CacheCapabilities: &pb.CacheCapabilities{
			DigestFunctions: []pb.DigestFunction_Value{pb.DigestFunction_SHA256},
			ActionCacheUpdateCapabilities: &pb.ActionCacheUpdateCapabilities{
				UpdateEnabled: true,
			},
			CachePriorityCapabilities: &pb.PriorityCapabilities{
				Priorities: []*pb.PriorityCapabilities_PriorityRange{
					{
						MinPriority: 0,
						MaxPriority: 0,
					},
				},
			},
			MaxBatchTotalSizeBytes:          0, // "no limit"
			SymlinkAbsolutePathStrategy:     pb.SymlinkAbsolutePathStrategy_ALLOWED,
			SupportedCompressors:            []pb.Compressor_Value{pb.Compressor_ZSTD},
			SupportedBatchUpdateCompressors: []pb.Compressor_Value{pb.Compressor_ZSTD},
			MaxCasBlobSizeBytes:             s.maxCasBlobSizeBytes,
		},
		LowApiVersion:  &semver.SemVer{Major: int32(2)},
		HighApiVersion: &semver.SemVer{Major: int32(2), Minor: int32(3)},
	}

	s.accessLogger.Printf("GRPC GETCAPABILITIES")

	return &resp, nil
}

// Return an error if `hash` is not a valid cache key.
func (s *grpcServer) validateHash(hash string, size int64, logPrefix string) error {
	if size == int64(0) {
		if hash == emptySha256 {
			return nil
		}

		msg := "Invalid zero-length SHA256 hash"
		s.accessLogger.Printf("%s %s: %s", logPrefix, hash, msg)
		return status.Error(codes.InvalidArgument, msg)
	}

	if len(hash) != hashKeyLength {
		msg := fmt.Sprintf("Hash length must be length %d", hashKeyLength)
		s.accessLogger.Printf("%s %s: %s", logPrefix, hash, msg)
		return status.Error(codes.InvalidArgument, msg)
	}

	if !validate.HashKeyRegex.MatchString(hash) {
		msg := "Malformed hash"
		s.accessLogger.Printf("%s %s: %s", logPrefix, hash, msg)
		return status.Error(codes.InvalidArgument, msg)
	}

	return nil
}

// Return a grpc.StreamServerInterceptor that checks for mTLS/client cert
// authentication, and optionally allows unauthenticated access to readonly
// RPCs.
func GRPCmTLSStreamServerInterceptor(allowUnauthenticatedReads bool) grpc.StreamServerInterceptor {

	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {

		if allowUnauthenticatedReads {
			_, ro := readOnlyMethods[info.FullMethod]
			if ro {
				return handler(srv, ss)
			}
		}

		err := checkGRPCClientCert(ss.Context())
		if err != nil {
			return err
		}

		return handler(srv, ss)
	}
}

// Return a grpc.UnaryServerInterceptor that checks for mTLS/client cert
// authentication, and optionally allows unauthenticated access to readonly
// RPCs, and allows all clients access to the health service.
func GRPCmTLSUnaryServerInterceptor(allowUnauthenticatedReads bool) grpc.UnaryServerInterceptor {

	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {

		// Always allow health service requests.
		if info.FullMethod == grpcHealthServiceName {
			return handler(ctx, req)
		}

		if allowUnauthenticatedReads {
			_, ro := readOnlyMethods[info.FullMethod]
			if ro {
				return handler(ctx, req)
			}
		}

		err := checkGRPCClientCert(ctx)
		if err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

// Return a non-nil grpc error if a valid client certificate can't be
// extracted from ctx. This is only used with mTLS authentication.
func checkGRPCClientCert(ctx context.Context) error {

	p, ok := peer.FromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "no peer found")
	}

	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return status.Error(codes.Unauthenticated, "unrecognised peer transport credentials")
	}

	if len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return status.Error(codes.Unauthenticated, "could not verify peer certificate")
	}

	return nil
}

// Return a grpc code based on err, or fall back to returning
// a default Code.
func gRPCErrCode(err error, dflt codes.Code) codes.Code {
	if err == nil {
		return codes.OK
	}

	var cerr *cache.Error
	if errors.As(err, &cerr) {
		switch cerr.Code {
		case http.StatusInsufficientStorage:
			return codes.ResourceExhausted
		case http.StatusBadRequest:
			return codes.InvalidArgument
		case http.StatusNotFound:
			return codes.NotFound
		}

	}

	return dflt
}

// Translate error codes, received by server when streaming back to client, into
// an error code suitable to return as result from the original server invocation
// that originated the streaming.
func translateGRPCErrCodeFromClient(err error) codes.Code {

	resultingCode := status.Code(err)

	// Client rejecting the streaming with
	// "code = Unavailable desc = transport is closing"
	// indicates that client canceled the call and is closing down. Client
	// being unavailable should not be confused as server being unavailable,
	// and is therefore instead mapped to Canceled.
	if resultingCode == codes.Unavailable {
		return codes.Canceled
	}

	// Internal error from client should not be mapped to internal error
	// in server, and is therefore translated to Unknown.
	if resultingCode == codes.Internal {
		return codes.Unknown
	}

	return resultingCode
}

func (s *grpcServer) logErrorPrintf(err error, format string, a ...any) {
	if translateGRPCErrCodeFromClient(err) == codes.ResourceExhausted {
		// Using accessLogger to prevent too verbose logging to errorLogger.
		s.accessLogger.Printf(format, a...)
	} else {
		s.errorLogger.Printf(format, a...)
	}
}
