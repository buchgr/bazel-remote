package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"regexp"

	"google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	_ "google.golang.org/grpc/encoding/gzip" // Register gzip support.
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	asset "github.com/buchgr/bazel-remote/genproto/build/bazel/remote/asset/v1"
	pb "github.com/buchgr/bazel-remote/genproto/build/bazel/remote/execution/v2"
	"github.com/buchgr/bazel-remote/genproto/build/bazel/semver"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/disk"

	_ "github.com/mostynb/go-grpc-compression/snappy" // Register snappy
	_ "github.com/mostynb/go-grpc-compression/zstd"   // and zstd support.
)

const (
	hashKeyLength = 64
	emptySha256   = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

var (
	// Cache keys must be lower case asciified SHA256 sums.
	hashKeyRegex = regexp.MustCompile("^[a-f0-9]{64}$")
)

type grpcServer struct {
	cache                    *disk.Cache
	accessLogger             cache.Logger
	errorLogger              cache.Logger
	depsCheck                bool
	mangleACKeys             bool
	checkClientCertForWrites bool
}

// ListenAndServeGRPC creates a new gRPC server and listens on the given
// address. This function either returns an error quickly, or triggers a
// blocking call to https://godoc.org/google.golang.org/grpc#Server.Serve
func ListenAndServeGRPC(addr string, opts []grpc.ServerOption,
	validateACDeps bool,
	mangleACKeys bool,
	enableRemoteAssetAPI bool,
	checkClientCertForWrites bool,
	c *disk.Cache, a cache.Logger, e cache.Logger) error {

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	return serveGRPC(listener, opts, validateACDeps, mangleACKeys, enableRemoteAssetAPI, checkClientCertForWrites, c, a, e)
}

func serveGRPC(l net.Listener, opts []grpc.ServerOption,
	validateACDepsCheck bool,
	mangleACKeys bool,
	enableRemoteAssetAPI bool,
	checkClientCertForWrites bool,
	c *disk.Cache, a cache.Logger, e cache.Logger) error {

	srv := grpc.NewServer(opts...)
	s := &grpcServer{
		cache: c, accessLogger: a, errorLogger: e,
		depsCheck:                validateACDepsCheck,
		mangleACKeys:             mangleACKeys,
		checkClientCertForWrites: checkClientCertForWrites,
	}
	pb.RegisterActionCacheServer(srv, s)
	pb.RegisterCapabilitiesServer(srv, s)
	pb.RegisterContentAddressableStorageServer(srv, s)
	bytestream.RegisterByteStreamServer(srv, s)
	if enableRemoteAssetAPI {
		asset.RegisterFetchServer(srv, s)
	}
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
			MaxBatchTotalSizeBytes:      0, // "no limit"
			SymlinkAbsolutePathStrategy: pb.SymlinkAbsolutePathStrategy_ALLOWED,
			SupportedCompressors:        []pb.Compressor_Value{pb.Compressor_ZSTD},
		},
		LowApiVersion:  &semver.SemVer{Major: int32(2)},
		HighApiVersion: &semver.SemVer{Major: int32(2), Minor: int32(1)},
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

	if !hashKeyRegex.MatchString(hash) {
		msg := "Malformed hash"
		s.accessLogger.Printf("%s %s: %s", logPrefix, hash, msg)
		return status.Error(codes.InvalidArgument, msg)
	}

	return nil
}

// Return a non-nil grpc error if a valid client certificate can't be
// extracted from ctx.
//
// This is only used when mutual TLS authentication and unauthenticated
// reads are enabled.
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

	cerr, ok := err.(*cache.Error)
	if ok && cerr.Code == http.StatusBadRequest {
		return codes.InvalidArgument
	}

	return dflt
}
