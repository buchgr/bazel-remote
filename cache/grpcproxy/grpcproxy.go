package grpcproxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/utils/backendproxy"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	asset "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/asset/v1"
	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
	bs "google.golang.org/genproto/googleapis/bytestream"
)

const (
	// The maximum chunk size to write back to the client in Send calls.
	// Inspired by Goma's FileBlob.FILE_CHUNK maxium size.
	maxChunkSize = 2 * 1024 * 1024 // 2M
)

type GrpcClients struct {
	asset asset.FetchClient
	bs    bs.ByteStreamClient
	ac    pb.ActionCacheClient
	cas   pb.ContentAddressableStorageClient
}

func NewGrpcClients(cc *grpc.ClientConn) (*GrpcClients, error) {
	resp, err := pb.NewCapabilitiesClient(cc).GetCapabilities(context.Background(), &pb.GetCapabilitiesRequest{})
	if err != nil {
		return nil, err
	}
	if !resp.CacheCapabilities.ActionCacheUpdateCapabilities.UpdateEnabled {
		return nil, errors.New("Proxy backend does not allow action cache updates")
	}
	supportsSha256 := func(r *pb.ServerCapabilities) bool {
		for _, df := range r.CacheCapabilities.DigestFunctions {
			if df == pb.DigestFunction_SHA256 {
				return true
			}
		}
		return false
	}
	if !supportsSha256(resp) {
		return nil, errors.New("Proxy backend does not support sha256")
	}

	return &GrpcClients{
		asset: asset.NewFetchClient(cc),
		bs:    bs.NewByteStreamClient(cc),
		ac:    pb.NewActionCacheClient(cc),
		cas:   pb.NewContentAddressableStorageClient(cc),
	}, nil
}

type remoteGrpcProxyCache struct {
	clients      *GrpcClients
	uploadQueue  chan<- backendproxy.UploadReq
	accessLogger cache.Logger
	errorLogger  cache.Logger
}

func New(clients *GrpcClients,
	accessLogger cache.Logger, errorLogger cache.Logger,
	numUploaders, maxQueuedUploads int) cache.Proxy {

	proxy := &remoteGrpcProxyCache{
		clients:      clients,
		accessLogger: accessLogger,
		errorLogger:  errorLogger,
	}

	proxy.uploadQueue = backendproxy.StartUploaders(proxy, numUploaders, maxQueuedUploads)

	return proxy
}

// Helper function for logging responses
func logResponse(logger cache.Logger, method string, msg string, resource string) {
	logger.Printf("GRPC PROXY %s %s: %s", method, msg, resource)
}

func (r *remoteGrpcProxyCache) UploadFile(item backendproxy.UploadReq) {
	defer item.Rc.Close()

	switch item.Kind {
	case cache.RAW:
		// RAW cache entries are a special case of AC, used when --disable_http_ac_validation
		// is enabled. We can treat them as AC in this scope
		fallthrough
	case cache.AC:
		data := make([]byte, item.SizeOnDisk)
		read := int64(0)
		for {
			n, err := item.Rc.Read(data[read:])
			if n > 0 {
				read += int64(n)
			}
			if err == io.EOF || read == item.SizeOnDisk {
				break
			}
		}
		if read != item.SizeOnDisk {
			logResponse(r.errorLogger, "AC Upload", "Unexpected short read", item.Hash)
			return
		}
		ar := &pb.ActionResult{}
		err := proto.Unmarshal(data, ar)
		if err != nil {
			logResponse(r.errorLogger, "AC Upload", err.Error(), item.Hash)
			return
		}
		digest := &pb.Digest{
			Hash:      item.Hash,
			SizeBytes: item.LogicalSize,
		}

		req := &pb.UpdateActionResultRequest{
			ActionDigest: digest,
			ActionResult: ar,
		}
		_, err = r.clients.ac.UpdateActionResult(context.Background(), req)
		if err != nil {
			logResponse(r.errorLogger, "AC Upload", err.Error(), item.Hash)
		}
		return
	case cache.CAS:
		stream, err := r.clients.bs.Write(context.Background())
		if err != nil {
			logResponse(r.errorLogger, "Write", err.Error(), item.Hash)
			return
		}

		bufSize := item.SizeOnDisk
		if bufSize > maxChunkSize {
			bufSize = maxChunkSize
		}
		buf := make([]byte, bufSize)

		resourceName := fmt.Sprintf("uploads/%s/blobs/%s/%d", uuid.New().String(), item.Hash, item.LogicalSize)
		firstIteration := true
		for {
			n, err := item.Rc.Read(buf)
			if err != nil && err != io.EOF {
				logResponse(r.errorLogger, "Write", err.Error(), resourceName)
				err := stream.CloseSend()
				if err != nil {
					logResponse(r.errorLogger, "Write", err.Error(), resourceName)
				}
				return
			}
			if n > 0 {
				rn := ""
				if firstIteration {
					firstIteration = false
					rn = resourceName
				}
				req := &bs.WriteRequest{
					ResourceName: rn,
					Data:         buf[:n],
				}
				err := stream.Send(req)
				if err != nil {
					logResponse(r.errorLogger, "Write", err.Error(), resourceName)
					return
				}
			} else {
				_, err = stream.CloseAndRecv()
				if err != nil {
					logResponse(r.errorLogger, "Write", err.Error(), resourceName)
					return
				}
				logResponse(r.accessLogger, "Write", "Success", resourceName)
				return
			}
		}
	default:
		logResponse(r.errorLogger, "Write", "Unexpected kind", item.Kind.String())
		return
	}
}

func (r *remoteGrpcProxyCache) Put(ctx context.Context, kind cache.EntryKind, hash string, logicalSize int64, sizeOnDisk int64, rc io.ReadCloser) {
	if r.uploadQueue == nil {
		rc.Close()
		return
	}

	item := backendproxy.UploadReq{
		Hash:        hash,
		LogicalSize: logicalSize,
		SizeOnDisk:  sizeOnDisk,
		Kind:        kind,
		Rc:          rc,
	}

	select {
	case r.uploadQueue <- item:
	default:
		r.errorLogger.Printf("too many uploads queued")
		rc.Close()
	}
}

func (r *remoteGrpcProxyCache) Get(ctx context.Context, kind cache.EntryKind, hash string) (io.ReadCloser, int64, error) {
	switch kind {
	case cache.RAW:
		// RAW cache entries are a special case of AC, used when --disable_http_ac_validation
		// is enabled. We can treat them as AC in this scope
		fallthrough
	case cache.AC:
		digest := pb.Digest{
			Hash:      hash,
			SizeBytes: -1,
		}

		req := &pb.GetActionResultRequest{ActionDigest: &digest}

		res, err := r.clients.ac.GetActionResult(ctx, req)
		status, ok := status.FromError(err)
		if ok && status.Code() == codes.NotFound {
			return nil, -1, nil
		}

		if err != nil {
			logResponse(r.errorLogger, "GetActionResult", err.Error(), digest.Hash)
			return nil, -1, err
		}
		data, err := proto.Marshal(res)
		if err != nil {
			logResponse(r.errorLogger, "GetActionResult", err.Error(), digest.Hash)
			return nil, -1, err
		}

		logResponse(r.accessLogger, "GetActionResult", "Success", digest.Hash)
		return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil

	case cache.CAS:
		// We don't know the size, so send a FetchBlob request first to get the digest
		// TODO: consider passign the expected blob size to the proxy?
		decoded, err := hex.DecodeString(hash)
		if err != nil {
			return nil, -1, err
		}
		q := asset.Qualifier{
			Name:  "checksum.sri",
			Value: fmt.Sprintf("sha256-%s", base64.StdEncoding.EncodeToString(decoded)),
		}
		freq := asset.FetchBlobRequest{
			Uris:       []string{},
			Qualifiers: []*asset.Qualifier{&q},
		}

		res, err := r.clients.asset.FetchBlob(ctx, &freq)
		if err != nil {
			logResponse(r.errorLogger, "FetchBlob", err.Error(), hash)
			return nil, -1, err
		}

		if res.Status.GetCode() == int32(codes.NotFound) {
			logResponse(r.accessLogger, "FetchBlob", res.Status.Message, hash)
			return nil, -1, nil
		}
		if res.Status.GetCode() != int32(codes.OK) {
			logResponse(r.errorLogger, "FetchBlob", res.Status.Message, hash)
			return nil, -1, errors.New(res.Status.Message)
		}

		req := bs.ReadRequest{
			ResourceName: fmt.Sprintf("blobs/%s/%d", res.BlobDigest.Hash, res.BlobDigest.SizeBytes),
		}
		stream, err := r.clients.bs.Read(ctx, &req)
		if err != nil {
			logResponse(r.errorLogger, "Read", err.Error(), hash)
			return nil, -1, err
		}
		rc := StreamReadCloser[*bs.ReadResponse]{Stream: stream}
		return &rc, res.BlobDigest.SizeBytes, nil
	default:
		return nil, -1, fmt.Errorf("Unexpected kind %s", kind)
	}
}

func (r *remoteGrpcProxyCache) Contains(ctx context.Context, kind cache.EntryKind, hash string) (bool, int64) {
	switch kind {
	case cache.RAW:
		// RAW cache entries are a special case of AC, used when --disable_http_ac_validation
		// is enabled. We can treat them as AC in this scope
		fallthrough
	case cache.AC:
		// There's not "contains" method for the action cache so the best we can do
		// is to get the object and discard the result
		// We don't expect this to ever be called anyways since it is not part of the grpc protocol
		rc, size, err := r.Get(ctx, kind, hash)
		rc.Close()
		if err != nil || size < 0 {
			return false, -1
		}
		return true, size
	case cache.CAS:
		decoded, err := hex.DecodeString(hash)
		if err != nil {
			logResponse(r.errorLogger, "Contains", err.Error(), hash)
			return false, -1
		}
		q := asset.Qualifier{
			Name:  "checksum.sri",
			Value: fmt.Sprintf("sha256-%s", base64.StdEncoding.EncodeToString(decoded)),
		}
		freq := asset.FetchBlobRequest{
			Uris:       []string{},
			Qualifiers: []*asset.Qualifier{&q},
		}

		res, err := r.clients.asset.FetchBlob(ctx, &freq)
		if err != nil {
			logResponse(r.errorLogger, "Contains", err.Error(), hash)
			return false, -1
		}

		if res.Status.GetCode() == int32(codes.NotFound) {
			logResponse(r.accessLogger, "Contains", "Not Found", hash)
			return false, -1
		}
		if res.Status.GetCode() != int32(codes.OK) {
			logResponse(r.errorLogger, "Contains", res.Status.Message, hash)
			return false, -1
		}

		logResponse(r.accessLogger, "Contains", "Success", hash)
		return true, res.BlobDigest.SizeBytes
	default:
		logResponse(r.errorLogger, "Contains", "Unexpected kind", kind.String())
		return false, -1
	}
}
