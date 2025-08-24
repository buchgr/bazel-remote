package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"google.golang.org/genproto/googleapis/rpc/code"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	grpc_status "google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/utils/validate"
)

var (
	errBadSize      = errors.New("unexpected size")
	errBlobNotFound = errors.New("blob not found")

	errNilBatchUpdateBlobsRequest_Request = grpc_status.Error(codes.InvalidArgument,
		"expected a non-nil *BatchUpdateBlobsRequest_Request")
	errNilDigest = grpc_status.Error(codes.InvalidArgument,
		"expected a non-nil *Digest")
	errNilGetTreeRequest = grpc_status.Error(codes.InvalidArgument,
		"expected a non-nil *GetTreeRequest")
	errNilFindMissingBlobsRequest = grpc_status.Error(codes.InvalidArgument,
		"expected a non-nil *FindMissingBlobsRequest")
	errNilBatchUpdateBlobsRequest = grpc_status.Error(codes.InvalidArgument,
		"expected a non-nil *BatchUpdateBlobsRequest")
	errNilBatchReadBlobsRequest = grpc_status.Error(codes.InvalidArgument,
		"expected a non-nil *BatchReadBlobsRequest")
)

// ContentAddressableStorageServer interface:

func (s *grpcServer) FindMissingBlobs(ctx context.Context,
	req *pb.FindMissingBlobsRequest) (*pb.FindMissingBlobsResponse, error) {

	if req == nil {
		return nil, errNilFindMissingBlobsRequest
	}

	errorPrefix := "GRPC CAS HEAD"
	for _, digest := range req.BlobDigests {

		if digest == nil {
			return nil, errNilDigest
		}

		err := s.validateHash(digest.Hash, digest.SizeBytes, errorPrefix)
		if err != nil {
			return nil, err
		}
	}

	missingBlobs, err := s.cache.FindMissingCasBlobs(ctx, req.BlobDigests)
	if err != nil {
		return nil, err
	}

	return &pb.FindMissingBlobsResponse{MissingBlobDigests: missingBlobs}, nil
}

func (s *grpcServer) BatchUpdateBlobs(ctx context.Context,
	in *pb.BatchUpdateBlobsRequest) (*pb.BatchUpdateBlobsResponse, error) {

	if in == nil {
		return nil, errNilBatchUpdateBlobsRequest
	}

	resp := pb.BatchUpdateBlobsResponse{
		Responses: make([]*pb.BatchUpdateBlobsResponse_Response,
			0, len(in.Requests)),
	}

	errorPrefix := "GRPC CAS PUT"
	for _, req := range in.Requests {
		// TODO: consider fanning-out goroutines here.

		if req == nil {
			return nil, errNilBatchUpdateBlobsRequest_Request
		}

		if req.Digest == nil {
			return nil, errNilDigest
		}

		err := s.validateHash(req.Digest.Hash, req.Digest.SizeBytes, errorPrefix)
		if err != nil {
			return nil, err
		}

		rr := pb.BatchUpdateBlobsResponse_Response{
			Digest: &pb.Digest{
				Hash:      req.Digest.Hash,
				SizeBytes: req.Digest.SizeBytes,
			},
			Status: &status.Status{},
		}
		resp.Responses = append(resp.Responses, &rr)

		if req.Compressor != pb.Compressor_IDENTITY && req.Compressor != pb.Compressor_ZSTD {
			s.errorLogger.Printf("%s %s UNSUPPORTED COMPRESSOR: %s", errorPrefix, req.Digest.Hash, req.Compressor)
			rr.Status.Code = int32(gRPCErrCode(err, codes.InvalidArgument))
			continue
		}

		if req.Compressor == pb.Compressor_ZSTD {
			req.Data, err = decoder.DecodeAll(req.Data, nil)
			if err != nil {
				s.errorLogger.Printf("%s %s %s", errorPrefix, req.Digest.Hash, err)
				rr.Status.Code = int32(gRPCErrCode(err, codes.Internal))
				continue
			}
		}

		err = s.cache.Put(ctx, cache.CAS, req.Digest.Hash,
			int64(len(req.Data)), bytes.NewReader(req.Data))
		if err != nil && err != io.EOF {
			s.logErrorPrintf(err, "%s %s %s", errorPrefix, req.Digest.Hash, err)
			rr.Status.Code = int32(gRPCErrCode(err, codes.Internal))
			continue
		}

		s.accessLogger.Printf("GRPC CAS PUT %s OK", req.Digest.Hash)
	}

	return &resp, nil
}

// Return the data for a blob, or an error.  If the blob was not
// found, the returned error is errBlobNotFound. Only use this
// function when it's OK to buffer the entire blob in memory.
func (s *grpcServer) getBlobData(ctx context.Context, hash string, size int64) ([]byte, error) {
	if size < 0 {
		return []byte{}, errBadSize
	}

	if size == 0 {
		return []byte{}, nil
	}

	rdr, sizeBytes, err := s.cache.Get(ctx, cache.CAS, hash, size, 0)
	if err != nil {
		if rdr != nil {
			_ = rdr.Close()
		}
		return []byte{}, err
	}

	if rdr == nil {
		return []byte{}, errBlobNotFound
	}

	if sizeBytes != size {
		_ = rdr.Close()
		return []byte{}, errBadSize
	}

	data, err := io.ReadAll(rdr)
	if err != nil {
		_ = rdr.Close()
		return []byte{}, err
	}

	return data, rdr.Close()
}

func (s *grpcServer) getBlobResponse(ctx context.Context, digest *pb.Digest, allowZstd bool) *pb.BatchReadBlobsResponse_Response {
	r := pb.BatchReadBlobsResponse_Response{Digest: digest}

	var data []byte
	var err error

	if allowZstd {
		rc, foundSize, err := s.cache.GetZstd(ctx, digest.Hash, digest.SizeBytes, 0)
		if rc != nil {
			defer func() { _ = rc.Close() }()
		}

		if err != nil {
			s.errorLogger.Printf("GRPC CAS GET %s INTERNAL ERROR: %v", digest.Hash, err)
			// Using codes.NotFound as default, in order to keep historical behaviour.
			// That ensures that clients handle for example corrupted headers
			// as normal cache misses and allows clients to gracefully replace corrupted
			// entries on disk by new uploads.
			// The drawback is that it hides the real reason in e.g. prometheus metrics.
			r.Status = &status.Status{Code: int32(gRPCErrCode(err, codes.NotFound))}
			return &r
		}

		if rc == nil || foundSize != digest.SizeBytes {
			s.accessLogger.Printf("GRPC CAS GET %s NOT FOUND", digest.Hash)
			r.Status = &status.Status{Code: int32(code.Code_NOT_FOUND)}
			return &r
		}

		data, err := io.ReadAll(rc)
		if err != nil {
			s.errorLogger.Printf("GRPC CAS GET %s INTERNAL ERROR: %v", digest.Hash, err)
			r.Status = &status.Status{Code: int32(code.Code_INTERNAL)}
			return &r
		}

		r.Data = data
		r.Compressor = pb.Compressor_ZSTD

		return &r
	}

	data, err = s.getBlobData(ctx, digest.Hash, digest.SizeBytes)
	if err == errBlobNotFound {
		s.accessLogger.Printf("GRPC CAS GET %s NOT FOUND", digest.Hash)
		r.Status = &status.Status{Code: int32(code.Code_NOT_FOUND)}
		return &r
	}

	if err != nil {
		s.errorLogger.Printf("GRPC CAS GET %s INTERNAL ERROR: %v",
			digest.Hash, err)
		// TODO The case above with allowZstd have codes.NotFound as default
		//      for unknown erros, but this has codes.Internal. Is that difference
		//      intentional?
		r.Status = &status.Status{Code: int32(gRPCErrCode(err, codes.Internal))}
		return &r
	}

	r.Data = data
	r.Compressor = pb.Compressor_IDENTITY

	s.accessLogger.Printf("GRPC CAS GET %s OK", digest.Hash)
	r.Status = &status.Status{Code: int32(codes.OK)}
	return &r
}

func (s *grpcServer) BatchReadBlobs(ctx context.Context,
	in *pb.BatchReadBlobsRequest) (*pb.BatchReadBlobsResponse, error) {

	if in == nil {
		return nil, errNilBatchReadBlobsRequest
	}

	resp := pb.BatchReadBlobsResponse{
		Responses: make([]*pb.BatchReadBlobsResponse_Response,
			0, len(in.Digests)),
	}

	allowZstd := false
	for _, c := range in.AcceptableCompressors {
		if c == pb.Compressor_ZSTD {
			allowZstd = true
			break
		}
	}

	errorPrefix := "GRPC CAS GET"
	for _, digest := range in.Digests {
		// TODO: consider fanning-out goroutines here.

		if digest == nil {
			return nil, errNilDigest
		}

		err := s.validateHash(digest.Hash, digest.SizeBytes, errorPrefix)
		if err != nil {
			return nil, err
		}
		resp.Responses = append(resp.Responses, s.getBlobResponse(ctx, digest, allowZstd))
	}

	return &resp, nil
}

func (s *grpcServer) GetTree(in *pb.GetTreeRequest,
	stream pb.ContentAddressableStorage_GetTreeServer) error {

	resp := pb.GetTreeResponse{
		Directories: make([]*pb.Directory, 0),
	}
	errorPrefix := "GRPC CAS GETTREEREQUEST"

	if in == nil {
		return errNilGetTreeRequest
	}

	if in.RootDigest == nil {
		return errNilDigest
	}

	err := s.validateHash(in.RootDigest.Hash, in.RootDigest.SizeBytes, errorPrefix)
	if err != nil {
		return err
	}

	data, err := s.getBlobData(stream.Context(), in.RootDigest.Hash, in.RootDigest.SizeBytes)
	if err == errBlobNotFound {
		s.accessLogger.Printf("GRPC CAS GETTREEREQUEST %s NOT FOUND",
			in.RootDigest.Hash)
		return grpc_status.Error(codes.NotFound, "Item not found")
	}
	if err != nil {
		s.accessLogger.Printf("%s %s %s", errorPrefix, in.RootDigest.Hash, err)
		return grpc_status.Error(codes.Unknown, err.Error())
	}

	dir := pb.Directory{}
	err = proto.Unmarshal(data, &dir)
	if err != nil {
		s.errorLogger.Printf("%s %s %s", errorPrefix, in.RootDigest.Hash, err)
		return grpc_status.Error(codes.DataLoss, err.Error())
	}

	err = s.fillDirectories(stream.Context(), &resp, &dir, errorPrefix)
	if err != nil {
		return err
	}

	err = stream.Send(&resp)
	if err != nil {
		return err
	}
	// TODO: if resp is too large, split it up and call Send multiple times,
	// with resp.NextPageToken set for all but the last Send call?

	s.accessLogger.Printf("GRPC GETTREEREQUEST %s OK", in.RootDigest.Hash)
	return nil
}

// Attempt to populate `resp`. Return errors for invalid requests, but
// otherwise attempt to return as many blobs as possible.
func (s *grpcServer) fillDirectories(ctx context.Context, resp *pb.GetTreeResponse, dir *pb.Directory, errorPrefix string) error {

	// Add this dir.
	resp.Directories = append(resp.Directories, dir)

	// Recursively append all the child dirs.
	for _, dirNode := range dir.Directories {

		err := s.validateHash(dirNode.Digest.Hash, dirNode.Digest.SizeBytes, errorPrefix)
		if err != nil {
			return err
		}

		data, err := s.getBlobData(ctx, dirNode.Digest.Hash, dirNode.Digest.SizeBytes)
		if err == errBlobNotFound {
			s.accessLogger.Printf("GRPC GETTREEREQUEST BLOB %s NOT FOUND",
				dirNode.Digest.Hash)
			continue
		}
		if err != nil {
			s.accessLogger.Printf("GRPC GETTREEREQUEST BLOB %s ERR: %v", err)
			continue
		}

		dirMsg := pb.Directory{}
		err = proto.Unmarshal(data, &dirMsg)
		if err != nil {
			s.accessLogger.Printf("GRPC GETTREEREQUEST BAD BLOB: %v", err)
			continue
		}

		s.accessLogger.Printf("GRPC GETTREEREQUEST BLOB %s ADDED OK",
			dirNode.Digest.Hash)

		err = s.fillDirectories(ctx, resp, &dirMsg, errorPrefix)
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *grpcServer) SpliceBlob(ctx context.Context, req *pb.SpliceBlobRequest) (*pb.SpliceBlobResponse, error) {

	if req == nil {
		return nil, grpc_status.Errorf(codes.InvalidArgument,
			"SpliceBlob called with nil SpliceBlobRequest")
	}

	if req.DigestFunction != pb.DigestFunction_UNKNOWN && req.DigestFunction != pb.DigestFunction_SHA256 {
		digestName, ok := pb.DigestFunction_Value_name[int32(req.DigestFunction)]
		if ok {
			return nil, grpc_status.Errorf(codes.InvalidArgument,
				"SpliceBlob called with unsupported digest function: %s", digestName)
		}

		return nil, grpc_status.Errorf(codes.InvalidArgument,
			"SpliceBlob called with unrecognised digest function: %d", req.DigestFunction)
	}

	// From this point, we assume that the digest function is SHA256 and verify digests as necessary.

	// Check that req.ChunkDigests is OK.

	if len(req.ChunkDigests) == 0 {
		return nil, grpc_status.Errorf(codes.InvalidArgument,
			"SpliceBlob called with no SpliceBlobRequest.ChunkDigests")
	}

	chunkTotal := int64(0)
	for _, chunkDigest := range req.ChunkDigests {
		if chunkDigest == nil {
			return nil, grpc_status.Errorf(codes.InvalidArgument,
				"SpliceBlob called with a nil value in SpliceBlobRequest.ChunkDigests")
		}

		if chunkDigest.SizeBytes < 0 {
			return nil, grpc_status.Errorf(codes.InvalidArgument,
				"SpliceBlob called with a negative Digest in SpliceBlobRequest.ChunkDigests")
		}

		if chunkDigest.SizeBytes == 0 || chunkDigest.Hash == emptySha256 {
			return nil, grpc_status.Errorf(codes.InvalidArgument,
				"SpliceBlob called with an empty blob in SpliceBlobRequest.ChunkDigests")
		}

		if !validate.HashKeyRegex.MatchString(chunkDigest.Hash) {
			return nil, grpc_status.Errorf(codes.InvalidArgument,
				"SpliceBlob called with an invalid digest in SpliceBlobRequest.ChunkDigests: %s/%d",
				chunkDigest.Hash, chunkDigest.SizeBytes)
		}

		// chunkDigest.SizeBytes must be positive if we reached this point.
		// Add it to chunkTotal (which then must be positive, unless there
		// was an overflow).

		chunkTotal += chunkDigest.SizeBytes

		if chunkTotal <= 0 {
			return nil, grpc_status.Errorf(codes.InvalidArgument,
				"Overflow in SpliceBlobRequest.ChunkDigests, does not match SpliceBlobRequest.BlobDigest.SizeBytes")
		}
	}

	checkBlobDigestHashMatchesRegex := true
	if req.BlobDigest == nil {
		// We need to calculate the spliced blob's digest before we can call Put.
		// Since the blob might be large, let's try to avoid buffering the entire
		// thing in memory. We might get cache hits from the kernel's filesystem
		// cache when reading the chunks twice anyway when feeding the Put call.

		checkBlobDigestHashMatchesRegex = false // No need to check, if we hash ourselves

		hasher := sha256.New()

		for _, chunkDigest := range req.ChunkDigests {
			rc, _, err := s.cache.Get(ctx, cache.CAS, chunkDigest.Hash, chunkDigest.SizeBytes, 0)
			if err != nil {
				if rc != nil {
					_ = rc.Close()
				}

				return nil, grpc_status.Errorf(codes.Unknown,
					"SpliceBlob failed to get chunk %s/%d: %s",
					chunkDigest.Hash, chunkDigest.SizeBytes, err)
			}

			if rc == nil {
				return nil, grpc_status.Errorf(codes.NotFound,
					"SpliceBlob called with nonexistent blob: %s/%d",
					chunkDigest.Hash, chunkDigest.SizeBytes)
			}

			// We can assume that the size returned by s.cache.Get equals chunkDigest.SizeBytes,
			// because we checked that is was not -1 in the chunkTotal check performed earlier.

			copiedBytes, err := io.Copy(hasher, rc)
			if err != nil {
				_ = rc.Close()
				return nil, grpc_status.Errorf(codes.Unknown,
					"SpliceBlob failed to copy chunk %s/%d: %s",
					chunkDigest.Hash, chunkDigest.SizeBytes, err)
			}

			if copiedBytes != chunkDigest.SizeBytes {
				_ = rc.Close()
				return nil, grpc_status.Errorf(codes.Unknown,
					"SpliceBlob copied unpexpected number of bytes (%d) from chunk %s/%d",
					copiedBytes, chunkDigest.Hash, chunkDigest.SizeBytes)
			}

			_ = rc.Close()
		}

		req.BlobDigest = &pb.Digest{
			Hash:      hex.EncodeToString(hasher.Sum(nil)),
			SizeBytes: chunkTotal,
		}
	}

	// At this point, req.BlobDigest is non-nil.

	if s.maxCasBlobSizeBytes > 0 && req.BlobDigest.SizeBytes > s.maxCasBlobSizeBytes {
		return nil, grpc_status.Errorf(codes.InvalidArgument,
			"SpliceBlob called to create blob with size %d, which is greater than the max configured blob size %d",
			req.BlobDigest.SizeBytes, s.maxCasBlobSizeBytes)
	}

	if req.BlobDigest.SizeBytes == 0 || req.BlobDigest.Hash == emptySha256 {
		return nil, grpc_status.Errorf(codes.InvalidArgument,
			"SpliceBlob called to create the empty blob?")
	}

	if req.BlobDigest.SizeBytes < 0 {
		return nil, grpc_status.Errorf(codes.InvalidArgument,
			"SpliceBlob called with negative SpliceBlobRequest.BlobDigest.SizeBytes")
	}

	if checkBlobDigestHashMatchesRegex && !validate.HashKeyRegex.MatchString(req.BlobDigest.Hash) {
		return nil, grpc_status.Errorf(codes.InvalidArgument,
			"SpliceBlob called with invalid SpliceBlobRequest.BlobDigest.Hash: %s",
			req.BlobDigest.Hash)
	}

	if chunkTotal != req.BlobDigest.SizeBytes {
		return nil, grpc_status.Errorf(codes.InvalidArgument,
			"SpliceBlob called with SpliceBlobRequest.ChunkDigests sizes sum to %d, but SpliceBlobRequest.BlobDigest.SizeBytes was %d",
			chunkTotal, req.BlobDigest.SizeBytes)
	}

	alreadyHaveSplicedBlob, _ := s.cache.Contains(ctx, cache.CAS, req.BlobDigest.Hash, req.BlobDigest.SizeBytes)
	if alreadyHaveSplicedBlob {
		resp := pb.SpliceBlobResponse{
			BlobDigest: req.BlobDigest,
		}

		return &resp, nil
	}

	pr, pw := io.Pipe()
	writerResultChan := make(chan error, 1)

	go func() {
		defer func() { _ = pw.Close() }()

		for _, chunkDigest := range req.ChunkDigests {
			rc, _, err := s.cache.Get(ctx, cache.CAS, chunkDigest.Hash, chunkDigest.SizeBytes, 0)
			if err != nil {
				if rc != nil {
					_ = rc.Close()
				}
				writerResultChan <- grpc_status.Errorf(codes.Unknown,
					"SpliceBlob failed to get chunk %s/%d: %s",
					chunkDigest.Hash, chunkDigest.SizeBytes, err)
				return
			}

			if rc == nil {
				writerResultChan <- grpc_status.Errorf(codes.NotFound,
					"SpliceBlob called with nonexistent blob: %s/%d",
					chunkDigest.Hash, chunkDigest.SizeBytes)
				return
			}

			// We can assume that the size returned by s.cache.Get equals chunkDigest.SizeBytes,
			// because we checked that is was not -1 in the chunkTotal check performed earlier.

			copiedBytes, err := io.Copy(pw, rc)
			if err != nil {
				_ = rc.Close()
				writerResultChan <- grpc_status.Errorf(codes.Unknown,
					"SpliceBlob failed to copy chunk %s/%d: %s",
					chunkDigest.Hash, chunkDigest.SizeBytes, err)
				return
			}

			if copiedBytes != chunkDigest.SizeBytes {
				_ = rc.Close()
				writerResultChan <- grpc_status.Errorf(codes.Unknown,
					"SpliceBlob copied unpexpected number of bytes (%d) from chunk %s/%d",
					copiedBytes, chunkDigest.Hash, chunkDigest.SizeBytes)
				return
			}

			_ = rc.Close()
		}

		writerResultChan <- nil
	}()

	err := s.cache.Put(ctx, cache.CAS, req.BlobDigest.Hash, req.BlobDigest.SizeBytes, pr)
	if err != nil {

		select {
		case writerErr, ok := <-writerResultChan:
			if ok && writerErr != nil {
				// Return the more specific writerErr.
				return nil, writerErr
			}
		default:
		}

		return nil, grpc_status.Errorf(codes.Unknown,
			"Failed to splice blob %s/%d: %s",
			req.BlobDigest.Hash, req.BlobDigest.SizeBytes, err)
	}

	resp := pb.SpliceBlobResponse{
		BlobDigest: req.BlobDigest,
	}

	return &resp, nil
}

func (s *grpcServer) SplitBlob(ctx context.Context, req *pb.SplitBlobRequest) (*pb.SplitBlobResponse, error) {
	return nil, grpc_status.Errorf(codes.Unimplemented, "method SplitBlob not implemented")
}
