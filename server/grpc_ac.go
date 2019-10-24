package server

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"

	pb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/golang/protobuf/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/buchgr/bazel-remote/cache"
)

// ActionCache interface:

func (s *grpcServer) GetActionResult(ctx context.Context,
	req *pb.GetActionResultRequest) (*pb.ActionResult, error) {

	errorPrefix := "GRPC AC GET"
	err := s.validateHash(req.ActionDigest.Hash, errorPrefix)
	if err != nil {
		return nil, err
	}

	dr, _, err := s.cache.Get(cache.AC, req.ActionDigest.Hash)
	if err != nil {
		s.accessLogger.Printf("%s %s %s", errorPrefix, req.ActionDigest.Hash, err)
		return nil, status.Error(codes.Unknown, err.Error())
	}
	if dr == nil {
		s.accessLogger.Printf("%s %s NOT FOUND", errorPrefix, req.ActionDigest.Hash)
		return nil, status.Error(codes.NotFound,
			fmt.Sprintf("%s not found in AC", req.ActionDigest.Hash))
	}

	// Note: req.ActionDigest refers to the Action, not the ActionResult.
	// There's nothing useful we can do with req.ActionDigest.SizeBytes.

	data, err := ioutil.ReadAll(dr)
	if err != nil {
		s.accessLogger.Printf("%s %s %s", errorPrefix, req.ActionDigest.Hash, err)
		return nil, status.Error(codes.Unknown, err.Error())
	}

	result := &pb.ActionResult{}
	err = proto.Unmarshal(data, result)
	if err != nil {
		// Invalid cache item?
		s.accessLogger.Printf("%s %s %s", errorPrefix, req.ActionDigest.Hash, err)
		return nil, status.Error(codes.DataLoss, err.Error())
	}

	// Check that referenced blobs are available.

	for _, f := range result.OutputFiles {
		if len(f.Contents) == 0 && f.Digest.SizeBytes > 0 {
			if !s.cache.Contains(cache.CAS, f.Digest.Hash) {
				s.accessLogger.Printf("GRPC CAS CONTAINS %s NOT FOUND",
					f.Digest.Hash)
				return nil, status.Error(codes.NotFound,
					fmt.Sprintf("%s not found in CAS", f.Digest.Hash))
			}
		}
	}

	for _, d := range result.OutputDirectories {
		if !s.cache.Contains(cache.CAS, d.TreeDigest.Hash) {
			s.accessLogger.Printf("GRPC CAS CONTAINS %s NOT FOUND",
				d.TreeDigest.Hash)
			return nil, status.Error(codes.NotFound,
				fmt.Sprintf("%s not found in CAS", d.TreeDigest.Hash))
		}
	}

	if result.StdoutDigest != nil {
		if !s.cache.Contains(cache.CAS, result.StdoutDigest.Hash) {
			s.accessLogger.Printf("GRPC CAS CONTAINS %s NOT FOUND",
				result.StdoutDigest.Hash)
			return nil, status.Error(codes.NotFound,
				fmt.Sprintf("%s not found in CAS", result.StdoutDigest.Hash))
		}
	}

	if result.StderrDigest != nil {
		if !s.cache.Contains(cache.CAS, result.StderrDigest.Hash) {
			s.accessLogger.Printf("GRPC CAS CONTAINS %s NOT FOUND",
				result.StderrDigest.Hash)
			return nil, status.Error(codes.NotFound,
				fmt.Sprintf("%s not found in CAS", result.StderrDigest.Hash))
		}
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

	data, err := proto.Marshal(req.ActionResult)
	if err != nil {
		s.accessLogger.Printf("%s %s %s", errorPrefix, req.ActionDigest.Hash, err)
		return nil, status.Error(codes.Unknown, err.Error())
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
