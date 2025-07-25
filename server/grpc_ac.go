package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/utils/validate"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var (
	// This is an Internal error rather than InvalidArgument because
	// we modify incoming ActionResults to make them non-zero.
	errEmptyActionResult = status.Error(codes.Internal,
		"rejecting empty ActionResult")

	errNilActionDigest = status.Error(codes.InvalidArgument,
		"expected a non-nil ActionDigest")
	errNilGetActionResultRequest = status.Error(codes.InvalidArgument,
		"expected a non-nil GetActionResultRequest")
	errNilUpdateActionResultRequest = status.Error(codes.InvalidArgument,
		"expected a non-nil UpdateActionResultRequest")
)

const (
	// gRPC by default rejects messages larger than 4M.
	// Inline a little less than this, enough so we don't
	// need to worry about serialization overhead.
	maxInlineSize = 3 * 1024 * 1024 // 3M
)

// ActionCache interface:

func (s *grpcServer) GetActionResult(ctx context.Context,
	req *pb.GetActionResultRequest) (*pb.ActionResult, error) {

	logPrefix := "GRPC AC GET"

	if req == nil {
		return nil, errNilGetActionResultRequest
	}

	if req.ActionDigest == nil {
		return nil, errNilActionDigest
	}

	if s.mangleACKeys {
		req.ActionDigest.Hash = cache.TransformActionCacheKey(req.ActionDigest.Hash, req.InstanceName, s.accessLogger)
	}

	err := s.validateHash(req.ActionDigest.Hash, req.ActionDigest.SizeBytes, logPrefix)
	if err != nil {
		return nil, err
	}

	// Clients provides hash and size of the Action, but not size of the ActionResult
	// checked by the the disk cache.
	const unknownActionResultSize = -1

	if !s.depsCheck {
		logPrefix = "GRPC AC GET NODEPSCHECK"

		rdr, sizeBytes, err := s.cache.Get(ctx, cache.AC, req.ActionDigest.Hash, unknownActionResultSize, 0)
		if err != nil {
			s.accessLogger.Printf("%s %s %s", logPrefix, req.ActionDigest.Hash, err)
			return nil, status.Error(gRPCErrCode(err, codes.Unknown), err.Error())
		}
		if rdr == nil || sizeBytes <= 0 {
			s.accessLogger.Printf("%s %s %s", logPrefix, req.ActionDigest.Hash, "NOT FOUND")
			return nil, status.Error(codes.NotFound,
				fmt.Sprintf("%s not found in AC", req.ActionDigest.Hash))
		}
		defer func() { _ = rdr.Close() }()

		acdata, err := io.ReadAll(rdr)
		if err != nil {
			s.accessLogger.Printf("%s %s %s", logPrefix, req.ActionDigest.Hash, err)
			return nil, status.Error(codes.Unknown, err.Error())
		}

		result := &pb.ActionResult{}
		err = proto.Unmarshal(acdata, result)
		if err != nil {
			s.accessLogger.Printf("%s %s %s", logPrefix, req.ActionDigest.Hash, err)
			return nil, status.Error(codes.Unknown, err.Error())
		}

		// This doesn't check deps, but does check for invalid fields.
		err = validate.ActionResult(result)
		if err != nil {
			s.accessLogger.Printf("%s %s %s", logPrefix, req.ActionDigest.Hash, err)
			return nil, status.Error(codes.Internal, err.Error())
		}

		s.accessLogger.Printf("%s %s OK", logPrefix, req.ActionDigest.Hash)
		return result, nil
	}

	result, _, err := s.cache.GetValidatedActionResult(ctx, req.ActionDigest.Hash)
	if err != nil {
		s.accessLogger.Printf("%s %s %s", logPrefix, req.ActionDigest.Hash, err)
		return nil, status.Error(gRPCErrCode(err, codes.Unknown), err.Error())
	}

	if result == nil {
		s.accessLogger.Printf("%s %s NOT FOUND", logPrefix, req.ActionDigest.Hash)
		return nil, status.Error(codes.NotFound,
			fmt.Sprintf("%s not found in AC", req.ActionDigest.Hash))
	}

	// Don't inline stdout/stderr/output files unless they were requested.

	var inlinedSoFar int64

	err = s.maybeInline(ctx, req.InlineStdout,
		&result.StdoutRaw, &result.StdoutDigest, &inlinedSoFar)
	if err != nil {
		s.accessLogger.Printf("%s %s %s", logPrefix, req.ActionDigest.Hash, err)
		return nil, status.Error(codes.Unknown, err.Error())
	}

	err = s.maybeInline(ctx, req.InlineStderr,
		&result.StderrRaw, &result.StderrDigest, &inlinedSoFar)
	if err != nil {
		s.accessLogger.Printf("%s %s %s", logPrefix, req.ActionDigest.Hash, err)
		return nil, status.Error(codes.Unknown, err.Error())
	}

	inlinableFiles := make(map[string]struct{}, len(req.InlineOutputFiles))
	for _, p := range req.InlineOutputFiles {
		inlinableFiles[p] = struct{}{}
	}
	for _, of := range result.GetOutputFiles() {
		_, ok := inlinableFiles[of.Path]
		err = s.maybeInline(ctx, ok, &of.Contents, &of.Digest, &inlinedSoFar)
		if err != nil {
			s.accessLogger.Printf("%s %s %s", logPrefix, req.ActionDigest.Hash, err)
			return nil, status.Error(codes.Unknown, err.Error())
		}
	}

	s.accessLogger.Printf("GRPC AC GET %s OK", req.ActionDigest.Hash)

	return result, nil
}

func (s *grpcServer) maybeInline(ctx context.Context, inline bool, slice *[]byte, digest **pb.Digest, inlinedSoFar *int64) error {

	if (*inlinedSoFar + int64(len(*slice))) > maxInlineSize {
		inline = false
	} else if digest != nil && *digest != nil &&
		(*inlinedSoFar+(*digest).SizeBytes) > maxInlineSize {
		inline = false
	}

	if !inline {
		if len(*slice) == 0 {
			return nil // Not inlined, nothing to do.
		}

		if *digest == nil {
			hash := sha256.Sum256(*slice)
			*digest = &pb.Digest{
				Hash:      hex.EncodeToString(hash[:]),
				SizeBytes: int64(len(*slice)),
			}
		}

		found, _ := s.cache.Contains(ctx, cache.CAS, (*digest).Hash, (*digest).SizeBytes)
		if !found {
			err := s.cache.Put(ctx, cache.CAS, (*digest).Hash, (*digest).SizeBytes,
				bytes.NewReader(*slice))
			if err == nil || err == io.EOF {
				s.accessLogger.Printf("GRPC CAS PUT %s OK", (*digest).Hash)
			} else {
				// De-inline failed (possibly due to "resource overload"). Preserve
				// inlined data regardless of desire to de-inline.
				s.accessLogger.Printf("GRPC CAS PUT %s %s", (*digest).Hash, err)
				*inlinedSoFar += int64(len(*slice))
				return nil
			}
		}

		*slice = []byte{}
		return nil
	}

	if len(*slice) > 0 {
		*inlinedSoFar += int64(len(*slice))
		return nil // Already inlined.
	}

	if digest == nil || *digest == nil || (*digest).SizeBytes == 0 {
		return nil // Nothing to inline?
	}

	// Otherwise, attempt to inline.
	if (*digest).SizeBytes > 0 {
		data, err := s.getBlobData(ctx, (*digest).Hash, (*digest).SizeBytes)
		if err != nil {
			return err
		}
		*slice = data
		*inlinedSoFar += (*digest).SizeBytes
	}

	return nil
}

func (s *grpcServer) UpdateActionResult(ctx context.Context,
	req *pb.UpdateActionResultRequest) (*pb.ActionResult, error) {

	logPrefix := "GRPC AC PUT"

	if req == nil {
		return nil, errNilUpdateActionResultRequest
	}

	if req.ActionDigest == nil {
		return nil, errNilActionDigest
	}

	if s.mangleACKeys {
		req.ActionDigest.Hash = cache.TransformActionCacheKey(req.ActionDigest.Hash, req.InstanceName, s.accessLogger)
	}

	err := s.validateHash(req.ActionDigest.Hash, req.ActionDigest.SizeBytes, logPrefix)
	if err != nil {
		return nil, err
	}

	// Validate the ActionResult's immediate fields, but don't check for dependent blobs.
	err = validate.ActionResult(req.ActionResult)
	if err != nil {
		return nil, err
	}

	// Ensure that the serialized ActionResult has non-zero length.
	addWorkerMetadataGRPC(ctx, req.ActionResult)

	data, err := proto.Marshal(req.ActionResult)
	if err != nil {
		s.accessLogger.Printf("%s %s %s", logPrefix, req.ActionDigest.Hash, err)
		return nil, status.Error(codes.Internal, err.Error())
	}

	if len(data) == 0 {
		s.accessLogger.Printf("%s %s %s", logPrefix, req.ActionDigest.Hash,
			errEmptyActionResult.Error())
		return nil, errEmptyActionResult
	}

	err = s.cache.Put(ctx, cache.AC, req.ActionDigest.Hash,
		int64(len(data)), bytes.NewReader(data))
	if err != nil && err != io.EOF {
		s.logErrorPrintf(err, "%s %s %s", logPrefix, req.ActionDigest.Hash, err)
		code := gRPCErrCode(err, codes.Internal)
		return nil, status.Error(code, err.Error())
	}

	// Also cache any inlined blobs, separately in the CAS.
	//
	// TODO: consider normalizing what we store in the AC (store all results
	// inlined? or de-inline all results?)

	for _, f := range req.ActionResult.OutputFiles {
		if f != nil && len(f.Contents) > 0 {

			if f.Digest == nil {
				hashData := sha256.Sum256(f.Contents)
				f.Digest = &pb.Digest{
					Hash:      hex.EncodeToString(hashData[:]),
					SizeBytes: int64(len(f.Contents)),
				}
			}

			err = s.cache.Put(ctx, cache.CAS, f.Digest.Hash,
				f.Digest.SizeBytes, bytes.NewReader(f.Contents))
			if err != nil && err != io.EOF {
				s.logErrorPrintf(err, "%s %s %s", logPrefix, req.ActionDigest.Hash, err)
				code := gRPCErrCode(err, codes.Internal)
				return nil, status.Error(code, err.Error())
			}
			s.accessLogger.Printf("GRPC CAS PUT %s OK", f.Digest.Hash)
		}
	}

	if len(req.ActionResult.StdoutRaw) > 0 {
		var hash string
		var sizeBytes int64
		if req.ActionResult.StdoutDigest != nil {
			hash = req.ActionResult.StdoutDigest.Hash
			sizeBytes = req.ActionResult.StdoutDigest.SizeBytes
		} else {
			hashData := sha256.Sum256(req.ActionResult.StdoutRaw)
			hash = hex.EncodeToString(hashData[:])
			sizeBytes = int64(len(req.ActionResult.StdoutRaw))
		}

		err = s.cache.Put(ctx, cache.CAS, hash, sizeBytes,
			bytes.NewReader(req.ActionResult.StdoutRaw))
		if err != nil && err != io.EOF {
			s.logErrorPrintf(err, "%s %s %s", logPrefix, req.ActionDigest.Hash, err)
			code := gRPCErrCode(err, codes.Internal)
			return nil, status.Error(code, err.Error())
		}
		s.accessLogger.Printf("GRPC CAS PUT %s OK", hash)
	}

	if len(req.ActionResult.StderrRaw) > 0 {
		var hash string
		var sizeBytes int64
		if req.ActionResult.StderrDigest != nil {
			hash = req.ActionResult.StderrDigest.Hash
			sizeBytes = req.ActionResult.StderrDigest.SizeBytes
		} else {
			hashData := sha256.Sum256(req.ActionResult.StderrRaw)
			hash = hex.EncodeToString(hashData[:])
			sizeBytes = int64(len(req.ActionResult.StderrRaw))
		}

		err = s.cache.Put(ctx, cache.CAS, hash, sizeBytes,
			bytes.NewReader(req.ActionResult.StderrRaw))
		if err != nil && err != io.EOF {
			s.logErrorPrintf(err, "%s %s %s", logPrefix, req.ActionDigest.Hash, err)
			code := gRPCErrCode(err, codes.Internal)
			return nil, status.Error(code, err.Error())
		}
		s.accessLogger.Printf("GRPC CAS PUT %s OK", hash)
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
