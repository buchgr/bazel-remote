package server

import (
	"bytes"
	"context"
	"errors"
	"io/ioutil"

	"github.com/golang/protobuf/proto"
	"google.golang.org/genproto/googleapis/rpc/code"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	grpc_status "google.golang.org/grpc/status"

	pb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"

	"github.com/buchgr/bazel-remote/cache"
)

var (
	errBadSize      = errors.New("Unexpected size")
	errBlobNotFound = errors.New("Blob not found")
)

// ContentAddressableStorageServer interface:

func (s *grpcServer) FindMissingBlobs(ctx context.Context,
	req *pb.FindMissingBlobsRequest) (*pb.FindMissingBlobsResponse, error) {

	resp := pb.FindMissingBlobsResponse{}

	errorPrefix := "GRPC CAS GET"
	for _, digest := range req.BlobDigests {
		hash := digest.GetHash()
		err := s.validateHash(hash, errorPrefix)
		if err != nil {
			return nil, err
		}

		if !s.cache.Contains(cache.CAS, req.GetInstanceName(), hash) {
			s.accessLogger.Printf("GRPC CAS HEAD [%s] %s NOT FOUND", req.GetInstanceName(), hash)
			resp.MissingBlobDigests = append(resp.MissingBlobDigests, digest)
		} else {
			s.accessLogger.Printf("GRPC CAS HEAD [%s] %s OK", req.GetInstanceName(), hash)
		}
	}

	return &resp, nil
}

func (s *grpcServer) BatchUpdateBlobs(ctx context.Context,
	in *pb.BatchUpdateBlobsRequest) (*pb.BatchUpdateBlobsResponse, error) {

	resp := pb.BatchUpdateBlobsResponse{
		Responses: make([]*pb.BatchUpdateBlobsResponse_Response,
			0, len(in.Requests)),
	}

	errorPrefix := "GRPC CAS PUT"
	for _, req := range in.Requests {
		// TODO: consider fanning-out goroutines here.
		err := s.validateHash(req.Digest.Hash, errorPrefix)
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

		err = s.cache.Put(cache.CAS, in.GetInstanceName(), req.Digest.Hash,
			int64(len(req.Data)), bytes.NewReader(req.Data))
		if err != nil {
			s.errorLogger.Printf("%s [%s] %s %s", errorPrefix, in.GetInstanceName(), req.Digest.Hash, err)
			rr.Status.Code = int32(code.Code_UNKNOWN)
			continue
		}

		s.accessLogger.Printf("GRPC CAS PUT [%s] %s OK", in.GetInstanceName(), req.Digest.Hash)
	}

	return &resp, nil
}

// Return the data for a blob, or an error.  If the blob was not
// found, the returned error is errBlobNotFound. Only use this
// function when it's OK to buffer the entire blob in memory.
func (s *grpcServer) getBlobData(instanceName string, hash string, size int64) ([]byte, error) {
	rdr, sizeBytes, err := s.cache.Get(cache.CAS, instanceName, hash)
	if err != nil {
		rdr.Close()
		return []byte{}, err
	}

	if rdr == nil {
		return []byte{}, errBlobNotFound
	}

	if sizeBytes != size {
		rdr.Close()
		return []byte{}, errBadSize
	}

	data, err := ioutil.ReadAll(rdr)
	if err != nil {
		rdr.Close()
		return []byte{}, err
	}

	return data, rdr.Close()
}

func (s *grpcServer) getBlobResponse(instanceName string, digest *pb.Digest) *pb.BatchReadBlobsResponse_Response {
	r := pb.BatchReadBlobsResponse_Response{Digest: digest}

	data, err := s.getBlobData(instanceName, digest.Hash, digest.SizeBytes)
	if err == errBlobNotFound {
		s.accessLogger.Printf("GRPC CAS GET [%s] %s NOT FOUND", instanceName, digest.Hash)
		r.Status = &status.Status{Code: int32(code.Code_NOT_FOUND)}
		return &r
	}

	if err != nil {
		s.errorLogger.Printf("GRPC CAS GET [%s] %s INTERNAL ERROR: %v",
			instanceName, digest.Hash, err)
		r.Status = &status.Status{Code: int32(code.Code_INTERNAL)}
		return &r
	}

	r.Data = data

	s.accessLogger.Printf("GRPC CAS GET [%s] %s OK", instanceName, digest.Hash)
	return &r
}

func (s *grpcServer) BatchReadBlobs(ctx context.Context,
	in *pb.BatchReadBlobsRequest) (*pb.BatchReadBlobsResponse, error) {

	resp := pb.BatchReadBlobsResponse{
		Responses: make([]*pb.BatchReadBlobsResponse_Response,
			0, len(in.Digests)),
	}

	errorPrefix := "GRPC CAS GET"
	for _, digest := range in.Digests {
		// TODO: consider fanning-out goroutines here.
		err := s.validateHash(digest.Hash, errorPrefix)
		if err != nil {
			return nil, err
		}
		resp.Responses = append(resp.Responses, s.getBlobResponse(in.GetInstanceName(), digest))
	}

	return &resp, nil
}

func (s *grpcServer) GetTree(in *pb.GetTreeRequest,
	stream pb.ContentAddressableStorage_GetTreeServer) error {

	resp := pb.GetTreeResponse{
		Directories: make([]*pb.Directory, 0),
	}
	errorPrefix := "GRPC CAS GETTREEREQUEST"
	err := s.validateHash(in.RootDigest.Hash, errorPrefix)
	if err != nil {
		return err
	}

	data, err := s.getBlobData(in.GetInstanceName(), in.RootDigest.Hash, in.RootDigest.SizeBytes)
	if err == errBlobNotFound {
		s.accessLogger.Printf("GRPC CAS GETTREEREQUEST [%s] %s NOT FOUND", in.GetInstanceName(),
			in.RootDigest.Hash)
		return grpc_status.Error(codes.NotFound, "Item not found")
	}
	if err != nil {
		s.accessLogger.Printf("%s [%s] %s %s", errorPrefix, in.GetInstanceName(), in.RootDigest.Hash, err)
		return grpc_status.Error(codes.Unknown, err.Error())
	}

	dir := pb.Directory{}
	err = proto.Unmarshal(data, &dir)
	if err != nil {
		s.errorLogger.Printf("%s [%s] %s %s", errorPrefix, in.GetInstanceName(), in.RootDigest.Hash, err)
		return grpc_status.Error(codes.DataLoss, err.Error())
	}

	err = s.fillDirectories(in.GetInstanceName(), &resp, &dir, errorPrefix)
	if err != nil {
		return err
	}

	stream.Send(&resp)
	// TODO: if resp is too large, split it up and call Send multiple times,
	// with resp.NextPageToken set for all but the last Send call?

	s.accessLogger.Printf("GRPC GETTREEREQUEST [%s] %s OK", in.GetInstanceName(), in.RootDigest.Hash)
	return nil
}

// Attempt to populate `resp`. Return errors for invalid requests, but
// otherwise attempt to return as many blobs as possible.
func (s *grpcServer) fillDirectories(instanceName string, resp *pb.GetTreeResponse, dir *pb.Directory, errorPrefix string) error {

	// Add this dir.
	resp.Directories = append(resp.Directories, dir)

	// Recursively append all the child dirs.
	for _, dirNode := range dir.Directories {

		err := s.validateHash(dirNode.Digest.Hash, errorPrefix)
		if err != nil {
			return err
		}

		data, err := s.getBlobData(instanceName, dirNode.Digest.Hash, dirNode.Digest.SizeBytes)
		if err == errBlobNotFound {
			s.accessLogger.Printf("GRPC GETTREEREQUEST [%s] BLOB %s NOT FOUND", instanceName,
				dirNode.Digest.Hash)
			continue
		}
		if err != nil {
			s.accessLogger.Printf("GRPC GETTREEREQUEST [%s] BLOB %s ERR: %v", instanceName, err)
			continue
		}

		dirMsg := pb.Directory{}
		err = proto.Unmarshal(data, &dirMsg)
		if err != nil {
			s.accessLogger.Printf("GRPC GETTREEREQUEST [%s] BAD BLOB: %v", instanceName, err)
			continue
		}

		s.accessLogger.Printf("GRPC GETTREEREQUEST [%s] BLOB %s ADDED OK", instanceName,
			dirNode.Digest.Hash)

		err = s.fillDirectories(instanceName, resp, &dirMsg, errorPrefix)
		if err != nil {
			return err
		}
	}

	return nil
}
