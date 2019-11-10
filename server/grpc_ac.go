package server

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"

	pb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/golang/protobuf/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/buchgr/bazel-remote/cache"
)

var (
	// This is an Internal error rather than InvalidArgument because
	// we modify incoming ActionResults to make them non-zero.
	errEmptyActionResult = status.Error(codes.Internal,
		"rejecting empty ActionResult")
)

// ActionCache interface:

func (s *grpcServer) GetActionResult(ctx context.Context,
	req *pb.GetActionResultRequest) (*pb.ActionResult, error) {

	errorPrefix := "GRPC AC GET"
	err := s.validateHash(req.ActionDigest.Hash, errorPrefix)
	if err != nil {
		return nil, err
	}

	result, _, err := cache.GetValidatedActionResult(s.cache,
		req.ActionDigest.Hash)
	if err != nil {
		s.accessLogger.Printf("%s %s %s", errorPrefix, req.ActionDigest.Hash, err)
		return nil, status.Error(codes.Unknown, err.Error())
	}

	if result == nil {
		s.accessLogger.Printf("%s %s NOT FOUND", errorPrefix, req.ActionDigest.Hash)
		return nil, status.Error(codes.NotFound,
			fmt.Sprintf("%s not found in AC", req.ActionDigest.Hash))
	}

	s.accessLogger.Printf("GRPC AC GET %s OK", req.ActionDigest.Hash)

	return result, nil
}

func (s *grpcServer) UpdateActionResult(ctx context.Context,
	req *pb.UpdateActionResultRequest) (*pb.ActionResult, error) {

	errorPrefix := "GRPC AC PUT"
	err := s.validateHash(req.ActionDigest.Hash, errorPrefix)
	if err != nil {
		return nil, err
	}

	// Ensure that the serialized ActionResult has non-zero length.
	addWorkerMetadataGRPC(ctx, req.ActionResult)

	data, err := proto.Marshal(req.ActionResult)
	if err != nil {
		s.accessLogger.Printf("%s %s %s", errorPrefix, req.ActionDigest.Hash, err)
		return nil, status.Error(codes.Unknown, err.Error())
	}

	if len(data) == 0 {
		s.accessLogger.Printf("%s %s %s", errorPrefix, req.ActionDigest.Hash,
			errEmptyActionResult.Error())
		return nil, errEmptyActionResult
	}

	err = s.cache.Put(cache.AC, req.ActionDigest.Hash,
		int64(len(data)), bytes.NewReader(data))
	if err != nil {
		s.accessLogger.Printf("%s %s %s", errorPrefix, req.ActionDigest.Hash, err)
		return nil, status.Error(codes.Unknown, err.Error())
	}

	s.accessLogger.Printf("GRPC AC PUT %s OK", req.ActionDigest.Hash)

	// Trivia: the RE API wants us to return the ActionResult from the
	// request, in order to follow this standard method style guide:
	// https://cloud.google.com/apis/design/standard_methods
	return req.ActionResult, nil
}

func addWorkerMetadataGRPC(ctx context.Context, ar *pb.ActionResult) {
	if ar.ExecutionMetadata == nil {
		ar.ExecutionMetadata = &pb.ExecutedActionMetadata{}
	} else if ar.ExecutionMetadata.Worker != "" {
		return
	}

	p, ok := peer.FromContext(ctx)
	if !ok {
		ar.ExecutionMetadata.Worker = "unknown"
		return
	}

	addr := p.Addr.String()

	if addr == "" {
		ar.ExecutionMetadata.Worker = "unknown"
		return
	}

	if !strings.ContainsAny(addr, ":") {
		// The addr in our unit tests is "bufconn".
		ar.ExecutionMetadata.Worker = addr
		return
	}

	worker, _, err := net.SplitHostPort(addr)
	if err != nil {
		ar.ExecutionMetadata.Worker = addr
		return
	}

	ar.ExecutionMetadata.Worker = worker
}
