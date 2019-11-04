package server

import (
	"context"
	"fmt"
	"net"
	"regexp"

	"google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	_ "google.golang.org/grpc/encoding/gzip" // Register gzip support.
	"google.golang.org/grpc/status"

	pb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/bazelbuild/remote-apis/build/bazel/semver"

	"github.com/buchgr/bazel-remote/cache"
)

const (
	hashKeyLength = 64
)

var (
	// Cache keys must be lower case asciified SHA256 sums.
	hashKeyRegex = regexp.MustCompile("^[a-f0-9]{64}$")
)

type grpcServer struct {
	cache        cache.Cache
	accessLogger cache.Logger
	errorLogger  cache.Logger
}

func ListenAndServeGRPC(addr string, opts []grpc.ServerOption,
	c cache.Cache, a cache.Logger, e cache.Logger) error {

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	return ServeGRPC(listener, opts, c, a, e)
}

func ServeGRPC(l net.Listener, opts []grpc.ServerOption,
	c cache.Cache, a cache.Logger, e cache.Logger) error {

	srv := grpc.NewServer(opts...)
	s := &grpcServer{cache: c, accessLogger: a, errorLogger: e}
	pb.RegisterActionCacheServer(srv, s)
	pb.RegisterCapabilitiesServer(srv, s)
	pb.RegisterContentAddressableStorageServer(srv, s)
	bytestream.RegisterByteStreamServer(srv, s)
	return srv.Serve(l)
}

// Capabilities interface:

func (s *grpcServer) GetCapabilities(ctx context.Context,
	req *pb.GetCapabilitiesRequest) (*pb.ServerCapabilities, error) {

	// Instance name is currently ignored.

	resp := pb.ServerCapabilities{
		CacheCapabilities: &pb.CacheCapabilities{
			DigestFunction: []pb.DigestFunction_Value{pb.DigestFunction_SHA256},
			ActionCacheUpdateCapabilities: &pb.ActionCacheUpdateCapabilities{
				UpdateEnabled: true,
			},
			CachePriorityCapabilities: &pb.PriorityCapabilities{
				Priorities: []*pb.PriorityCapabilities_PriorityRange{
					&pb.PriorityCapabilities_PriorityRange{
						MinPriority: 0,
						MaxPriority: 0,
					},
				},
			},
			MaxBatchTotalSizeBytes:      0, // "no limit"
			SymlinkAbsolutePathStrategy: pb.SymlinkAbsolutePathStrategy_ALLOWED,
		},
		LowApiVersion:  &semver.SemVer{Major: int32(2)},
		HighApiVersion: &semver.SemVer{Major: int32(2)},
	}

	s.accessLogger.Printf("GRPC GETCAPABILITIES")

	return &resp, nil
}

// Return an error if `hash` is not a valid cache key.
func (s *grpcServer) validateHash(hash string, logPrefix string) error {
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
