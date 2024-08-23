package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk/casblob"

	"github.com/buchgr/bazel-remote/v2/utils/zstdpool"
	syncpool "github.com/mostynb/zstdpool-syncpool"
)

const (
	// The maximum chunk size to write back to the client in Send calls.
	// Inspired by Goma's FileBlob.FILE_CHUNK maxium size.
	maxChunkSize = 2 * 1024 * 1024 // 2M
)

var decoderPool = zstdpool.GetDecoderPool()

// ByteStreamServer interface:

var emptyZstdBlob = []byte{40, 181, 47, 253, 32, 0, 1, 0, 0}

var (
	errNilReadRequest = status.Error(codes.InvalidArgument,
		"expected a non-nil *bytestream.ReadRequest")
	errNilQueryWriteStatusRequest = status.Error(codes.InvalidArgument,
		"expected a non-nil *bytestream.QueryWriteStatusRequest")
)

func (s *grpcServer) Read(req *bytestream.ReadRequest,
	resp bytestream.ByteStream_ReadServer) error {

	if req == nil {
		return errNilReadRequest
	}

	errorPrefix := "GRPC BYTESTREAM READ"

	var cmp casblob.CompressionType
	hash, size, cmp, err := s.parseReadResource(req.ResourceName, errorPrefix)
	if err != nil {
		return err
	}

	if size == 0 {
		if cmp == casblob.Identity {
			s.accessLogger.Printf("GRPC BYTESTREAM READ COMPLETED %s", req.ResourceName)
			return nil
		}

		// The client asked for a zstd-compressed empty blob. Weird.
		err := resp.Send(&bytestream.ReadResponse{Data: emptyZstdBlob})
		if err != nil {
			msg := fmt.Sprintf("GRPC BYTESTREAM READ FAILED TO SEND RESPONSE: %s %v", hash, err)
			s.accessLogger.Printf(msg)
			return status.Error(codes.Unknown, msg)
		}
		s.accessLogger.Printf("GRPC BYTESTREAM READ COMPLETED %s", req.ResourceName)
		return nil
	}

	if req.ReadOffset < 0 {
		s.accessLogger.Printf("GRPC BYTESTREAM READ OFFSET INVALID: %s %d",
			req.ReadOffset)
		return status.Error(codes.InvalidArgument,
			"Negative ReadOffset is invalid")
	}

	if cmp != casblob.Identity && req.ReadLimit != 0 {
		s.accessLogger.Printf("GRPC BYTESTREAM READ LIMIT INVALID: %s %d",
			req.ReadLimit)
		return status.Error(codes.InvalidArgument,
			"ReadLimit must be 0 for compressed-blobs")
	}

	if req.ReadLimit < 0 {
		s.accessLogger.Printf("GRPC BYTESTREAM READ LIMIT OUT OF RANGE: %s %d",
			req.ReadLimit)
		return status.Error(codes.OutOfRange,
			"Negative ReadLimit is out of range")
	}

	limitedSend := (req.ReadLimit != 0) && cmp == casblob.Identity
	sendLimitRemaining := req.ReadLimit

	if req.ReadOffset > size {
		msg := fmt.Sprintf("ReadOffset %d larger than expected data size %d resource: %s",
			req.ReadOffset, size, req.ResourceName)
		s.accessLogger.Printf("GRPC BYTESTREAM READ FAILED %s: %s", hash, msg)
		return status.Error(codes.OutOfRange, msg)
	}

	var rc io.ReadCloser
	var foundSize int64

	if cmp == casblob.Zstandard {
		rc, foundSize, err = s.cache.GetZstd(resp.Context(), hash, size, req.ReadOffset)
	} else {
		rc, foundSize, err = s.cache.Get(resp.Context(), cache.CAS, hash, size, req.ReadOffset)
	}

	if rc != nil {
		defer rc.Close()
	}

	if err != nil {
		msg := fmt.Sprintf("GRPC BYTESTREAM READ FAILED: %s %v", hash, err)
		s.accessLogger.Printf(msg)
		code := gRPCErrCode(err, codes.Internal)
		return status.Error(code, msg)
	}
	if rc == nil {
		msg := fmt.Sprintf("GRPC BYTESTREAM READ BLOB NOT FOUND: %s", hash)
		s.accessLogger.Printf(msg)
		return status.Error(codes.NotFound, msg)
	}

	if foundSize != size {
		// This should have been caught above.
		msg := fmt.Sprintf("GRPC BYTESTREAM READ BLOB SIZE MISMATCH: %s (EXPECTED %d, FOUND %d)",
			hash, size, foundSize)
		s.accessLogger.Printf(msg)
		return status.Error(codes.Internal, msg)
	}

	bufSize := size
	if bufSize > maxChunkSize {
		bufSize = maxChunkSize
	}

	buf := make([]byte, bufSize)

	var chunkResp bytestream.ReadResponse
	for {
		n, err := rc.Read(buf)

		if n > 0 {
			if limitedSend {
				if (sendLimitRemaining - int64(n)) < 0 {
					msg := fmt.Sprintf("GRPC BYTESTREAM READ FAILED: %s READ LIMIT EXCEEDED", hash)
					s.accessLogger.Printf(msg)
					return status.Error(codes.OutOfRange, msg)
				}
				sendLimitRemaining -= int64(n)
			}

			chunkResp.Data = buf[:n]
			sendErr := resp.Send(&chunkResp)
			if sendErr != nil {
				msg := fmt.Sprintf("GRPC BYTESTREAM READ FAILED TO SEND RESPONSE: %s %v", hash, sendErr)
				s.accessLogger.Printf(msg)
				return status.Error(codes.Unknown, msg)
			}
		}

		if err == io.EOF {
			s.accessLogger.Printf("GRPC BYTESTREAM READ COMPLETED %s",
				req.ResourceName)
			return nil
		}

		if err != nil {
			msg := fmt.Sprintf("GRPC BYTESTREAM READ FAILED: %s %v", hash, err)
			s.accessLogger.Printf(msg)
			return status.Error(codes.Unknown, msg)
		}
	}
}

// Parse a ReadRequest.ResourceName, return the validated hash, size, compression type and an error.
func (s *grpcServer) parseReadResource(name string, errorPrefix string) (string, int64, casblob.CompressionType, error) {

	// The resource name should be of the format:
	// [{instance_name}]/blobs/{hash}/{size}
	// Or:
	// [{instance_name}]/compressed-blobs/{compressor}/{uncompressed_hash}/{uncompressed_size}

	// Instance_name is ignored in this bytestream implementation, so don't
	// bother returning it. It is not allowed to contain "blobs" as a distinct
	// path segment.

	fields := strings.Split(name, "/")
	var rem []string
	foundBlobs := false
	foundCompressedBlobs := false
	for i := range fields {
		if fields[i] == "blobs" {
			rem = fields[i+1:]
			foundBlobs = true
			break
		}

		if fields[i] == "compressed-blobs" {
			rem = fields[i+1:]
			foundCompressedBlobs = true
			break
		}
	}

	if foundBlobs {
		if len(rem) != 2 {
			msg := fmt.Sprintf("Unable to parse resource name: %s", name)
			s.accessLogger.Printf("%s: %s", errorPrefix, msg)
			return "", 0, casblob.Identity,
				status.Error(codes.InvalidArgument, msg)
		}

		hash := rem[0]

		size, err := strconv.ParseInt(rem[1], 10, 64)
		if err != nil {
			msg := fmt.Sprintf("Invalid size: %s from %q", rem[1], name)
			s.accessLogger.Printf("%s: %s", errorPrefix, msg)
			return "", 0, casblob.Identity,
				status.Error(codes.InvalidArgument, msg)
		}
		if size < 0 {
			msg := fmt.Sprintf("Invalid size (must be non-negative): %d from %q", size, name)
			s.accessLogger.Printf("%s: %s", errorPrefix, msg)
			return "", 0, casblob.Identity,
				status.Error(codes.InvalidArgument, msg)
		}

		err = s.validateHash(hash, size, errorPrefix)
		if err != nil {
			return "", 0, casblob.Identity, err
		}

		return hash, size, casblob.Identity, nil
	}

	if !foundCompressedBlobs || len(rem) != 3 {
		msg := fmt.Sprintf("Unable to parse resource name: %s", name)
		s.accessLogger.Printf("%s: %s", errorPrefix, msg)
		return "", 0, casblob.Identity,
			status.Error(codes.InvalidArgument, msg)
	}

	if rem[0] != "zstd" {
		msg := fmt.Sprintf("Unable to parse compressor in resource name: %s", name)
		s.accessLogger.Printf("%s: %s", errorPrefix, msg)
		return "", 0, casblob.Identity,
			status.Error(codes.InvalidArgument, msg)
	}

	hash := rem[1]
	sizeStr := rem[2]

	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		msg := fmt.Sprintf("Invalid size: %s from %q", sizeStr, name)
		s.accessLogger.Printf("%s: %s", errorPrefix, msg)
		return "", 0, casblob.Zstandard,
			status.Error(codes.InvalidArgument, msg)
	}
	if size < 0 {
		msg := fmt.Sprintf("Invalid size (must be non-negative): %d from %q", size, name)
		s.accessLogger.Printf("%s: %s", errorPrefix, msg)
		return "", 0, casblob.Zstandard,
			status.Error(codes.InvalidArgument, msg)
	}

	err = s.validateHash(hash, size, errorPrefix)
	if err != nil {
		return "", 0, casblob.Zstandard, err
	}

	return hash, size, casblob.Zstandard, nil
}

// Parse a WriteRequest.ResourceName, return the validated hash, size,
// compression type and an optional error.
func (s *grpcServer) parseWriteResource(r string) (string, int64, casblob.CompressionType, error) {

	// req.ResourceName is of the form:
	// [{instance_name}/]uploads/{uuid}/blobs/{hash}/{size}[/{optionalmetadata}]
	// Or, for compressed blobs:
	// [{instance_name}/]uploads/{uuid}/compressed-blobs/{compressor}/{uncompressed_hash}/{uncompressed_size}[{/optional_metadata}]

	fields := strings.Split(r, "/")
	var rem []string
	for i := range fields {
		if fields[i] == "uploads" {
			rem = fields[i+1:]
			break
		}
	}

	if len(rem) < 4 {
		return "", 0, casblob.Identity,
			status.Errorf(codes.InvalidArgument, "Unable to parse resource name: %s", r)
	}

	// rem[0] should hold the uuid, which we don't use- ignore it.

	if rem[1] == "blobs" {
		hash := rem[2]
		size, err := strconv.ParseInt(rem[3], 10, 64)
		if err != nil {
			return "", 0, casblob.Identity,
				status.Errorf(codes.InvalidArgument, "Unable to parse size: %s from %q", rem[3], r)
		}

		if size < 0 {
			return "", 0, casblob.Identity,
				status.Errorf(codes.InvalidArgument, "Invalid size (must be non-negative): %d from %q", size, r)
		}

		err = s.validateHash(hash, size, "GRPC BYTESTREAM READ FAILED")
		if err != nil {
			return "", 0, casblob.Identity, err
		}

		return hash, size, casblob.Identity, nil
	}

	if rem[1] != "compressed-blobs" || len(rem) < 5 || rem[2] != "zstd" {
		return "", 0, casblob.Zstandard,
			status.Errorf(codes.InvalidArgument, "Unable to parse resource name: %s", r)
	}

	sizeStr := rem[4]

	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return "", 0, casblob.Zstandard,
			status.Errorf(codes.InvalidArgument, "Unable to parse size: %s from %q", sizeStr, r)
	}

	if size < 0 {
		return "", 0, casblob.Zstandard,
			status.Errorf(codes.InvalidArgument, "Invalid size (must be non-negative): %d from %q", size, r)
	}

	hash := rem[3]
	err = s.validateHash(hash, size, "GRPC BYTESTREAM READ FAILED")
	if err != nil {
		return "", 0, casblob.Zstandard, err
	}

	return hash, size, casblob.Zstandard, nil
}

var errWriteOffset error = errors.New("bytestream writes from non-zero offsets are unsupported")
var errDecoderPoolFail error = errors.New("failed to get DecoderWrapper from pool")

func (s *grpcServer) Write(srv bytestream.ByteStream_WriteServer) error {

	var resp bytestream.WriteResponse
	pr, pw := io.Pipe()

	putResult := make(chan error, 1)
	recvResult := make(chan error, 1)
	resourceNameChan := make(chan string, 1)

	cmp := casblob.Identity
	var dec *syncpool.DecoderWrapper
	defer func() {
		if dec != nil {
			dec.Close()
		}
	}()

	go func() {
		firstIteration := true
		var resourceName string
		var size int64

		for {
			req, err := srv.Recv()
			if err == io.EOF {
				if cmp == casblob.Identity && resp.CommittedSize != size {
					msg := fmt.Sprintf("Unexpected amount of data read: %d expected: %d",
						resp.CommittedSize, size)
					recvResult <- status.Error(codes.Unknown, msg)
					return
				}

				recvResult <- io.EOF
				return
			}
			if err != nil {
				recvResult <- status.Error(codes.Internal, err.Error())
				return
			}

			if firstIteration {
				resourceName = req.ResourceName
				if resourceName == "" {
					msg := "Empty resource name"
					s.accessLogger.Printf("GRPC BYTESTREAM WRITE FAILED: %s", msg)
					recvResult <- status.Error(codes.InvalidArgument, msg)
					return
				}
				resourceNameChan <- resourceName
				close(resourceNameChan)

				var hash string
				hash, size, cmp, err = s.parseWriteResource(resourceName)
				if err != nil {
					s.accessLogger.Printf("GRPC BYTESTREAM WRITE FAILED: %s", err)
					recvResult <- err
					return
				}

				exists, _ := s.cache.Contains(srv.Context(), cache.CAS, hash, size)
				if exists {
					// Blob already exists, return without writing anything.
					if cmp == casblob.Identity {
						resp.CommittedSize = size
					} else {
						resp.CommittedSize = -1
					}
					putResult <- io.EOF
					return
				}

				resp.CommittedSize = req.WriteOffset
				if req.WriteOffset != 0 {
					err = errWriteOffset
					s.accessLogger.Printf("GRPC BYTESTREAM WRITE FAILED: %s", err)
					recvResult <- err
					return
				}

				var rc io.ReadCloser = pr
				if cmp == casblob.Zstandard {
					var ok bool
					dec, ok = decoderPool.Get().(*syncpool.DecoderWrapper)
					if !ok {
						s.accessLogger.Printf("GRPC BYTESTREAM WRITE FAILED: %s", errDecoderPoolFail)
						recvResult <- errDecoderPoolFail
						return
					}
					err = dec.Reset(pr)
					if err != nil {
						s.accessLogger.Printf("GRPC BYTESTREAM WRITE FAILED: %s", err)
						recvResult <- err
						return
					}
					rc = dec.IOReadCloser()
				}

				go func() {
					err := s.cache.Put(srv.Context(), cache.CAS, hash, size, rc)
					putResult <- err
				}()

				firstIteration = false
			} else {
				if req.ResourceName != "" && resourceName != req.ResourceName {
					msg := fmt.Sprintf("Resource name changed in a single Write %v -> %v",
						resourceName, req.ResourceName)
					recvResult <- status.Error(codes.InvalidArgument, msg)
					return
				}
			}

			n, err := pw.Write(req.Data)
			if err != nil {
				recvResult <- status.Error(codes.Internal, err.Error())
				return
			}
			resp.CommittedSize += int64(n)

			if cmp == casblob.Identity && resp.CommittedSize > size {
				msg := fmt.Sprintf("Client sent more than %d data! %d", size, resp.CommittedSize)
				recvResult <- status.Error(codes.OutOfRange, msg)
				return
			}

			// Possibly redundant check, since we explicitly check for
			// EOF at the start of each loop.
			if req.FinishWrite {
				if cmp == casblob.Identity && resp.CommittedSize != size {
					msg := fmt.Sprintf("Unexpected amount of data read: %d expected: %d",
						resp.CommittedSize, size)
					recvResult <- status.Error(codes.Unknown, msg)
					return
				}

				recvResult <- io.EOF
				return
			}
		}
	}()

	resourceName := "unknown-resource"

	select {
	case err, ok := <-recvResult:
		if !ok {
			select {
			case resourceName = <-resourceNameChan:
			default:
			}

			msg := fmt.Sprintf("GRPC BYTESTREAM WRITE FAILED: %s Receive loop closed unexpectedly.", resourceName)
			s.accessLogger.Printf(msg)
			return status.Error(codes.Internal, msg)
		}
		if err == io.EOF {
			pw.Close()
			break
		}
		if err != nil {
			select {
			case resourceName = <-resourceNameChan:
			default:
			}

			_ = pw.CloseWithError(err)
			s.accessLogger.Printf("GRPC BYTESTREAM WRITE FAILED: %s %s",
				resourceName, err.Error())
			return err
		}

	case err := <-putResult:
		select {
		case resourceName = <-resourceNameChan:
		default:
		}

		if err == io.EOF {
			s.accessLogger.Printf("GRPC BYTESTREAM SKIPPED WRITE: %s", resourceName)

			err = srv.SendAndClose(&resp)
			if err != nil {
				msg := fmt.Sprintf("GRPC BYTESTREAM SKIPPED WRITE FAILED: %s %v", resourceName, err)
				s.accessLogger.Printf(msg)
				return status.Error(codes.Internal, msg)
			}
			return nil
		}
		if err == nil {
			// Unexpected early return. Should not happen.
			msg := fmt.Sprintf("GRPC BYTESTREAM WRITE INTERNAL ERROR %s", resourceName)
			s.accessLogger.Printf(msg)
			return status.Error(codes.Internal, msg)
		}

		msg := fmt.Sprintf("GRPC BYTESTREAM WRITE CACHE ERROR: %s %v", resourceName, err)
		s.accessLogger.Printf(msg)
		return status.Error(codes.Internal, msg)
	}

	select {
	case resourceName = <-resourceNameChan:
	default:
	}

	err := <-putResult
	if err == io.EOF {
		s.accessLogger.Printf("GRPC BYTESTREAM SKIPPED WRITE: %s", resourceName)

		err = srv.SendAndClose(&resp)
		if err != nil {
			msg := fmt.Sprintf("GRPC BYTESTREAM SKIPPED WRITE FAILED: %s %v", resourceName, err)
			s.accessLogger.Printf(msg)
			return status.Error(codes.Internal, msg)
		}
		return nil
	}
	if err != nil {
		msg := fmt.Sprintf("GRPC BYTESTREAM WRITE FAILED: %s Cache Put failed: %v", resourceName, err)
		s.accessLogger.Printf(msg)
		code := gRPCErrCode(err, codes.Internal)
		return status.Error(code, msg)
	}

	err = srv.SendAndClose(&resp)
	if err != nil {
		msg := fmt.Sprintf("GRPC BYTESTREAM WRITE FAILED: %s %v", resourceName, err)
		s.accessLogger.Printf(msg)
		return status.Error(codes.Unknown, msg)
	}

	s.accessLogger.Printf("GRPC BYTESTREAM WRITE COMPLETED: %s", resourceName)
	return nil
}

func (s *grpcServer) QueryWriteStatus(ctx context.Context, req *bytestream.QueryWriteStatusRequest) (*bytestream.QueryWriteStatusResponse, error) {

	if req == nil {
		return nil, errNilQueryWriteStatusRequest
	}

	hash, size, _, err := s.parseWriteResource(req.ResourceName)
	if err != nil {
		return nil, err
	}

	// We don't support partial writes, so the status will either be fully written
	// and complete, or 0 written and incomplete.

	exists, _ := s.cache.Contains(ctx, cache.CAS, hash, size)

	if !exists {
		return &bytestream.QueryWriteStatusResponse{CommittedSize: 0, Complete: false}, nil
	}

	return &bytestream.QueryWriteStatusResponse{CommittedSize: size, Complete: true}, nil
}
