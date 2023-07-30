package grpcproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk"
	"github.com/buchgr/bazel-remote/v2/server"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
	testutils "github.com/buchgr/bazel-remote/v2/utils"
	bs "google.golang.org/genproto/googleapis/bytestream"
)

var logger = testutils.NewSilentLogger()

type testProxy struct {
	dir    string
	server *grpc.Server
	proxy  cache.Proxy
}

func (p *testProxy) Contains(kind cache.EntryKind, hash string) bool {
	src := filepath.Join(p.dir, kind.DirName(), hash)
	if _, err := os.Stat(src); err != nil {
		return false
	}
	return true
}

func newProxy(t *testing.T, dir string, storageMode string) *testProxy {
	listener := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	p := &testProxy{
		dir:    dir,
		server: srv,
	}
	pb.RegisterActionCacheServer(srv, p)
	pb.RegisterCapabilitiesServer(srv, p)
	pb.RegisterContentAddressableStorageServer(srv, p)
	bs.RegisterByteStreamServer(srv, p)

	go func() {
		_ = srv.Serve(listener)
	}()

	dialer := func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}
	cc, err := grpc.Dial(
		"bufconn",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatal(err)
	}
	clients := NewGrpcClients(cc)
	err = clients.CheckCapabilities(storageMode == "zstd")
	if err != nil {
		t.Fatal(err)
	}
	proxy := New(clients, storageMode, logger, logger, 100, 100)
	p.proxy = proxy

	return p
}

func (p *testProxy) GetCapabilities(ctx context.Context, req *pb.GetCapabilitiesRequest) (*pb.ServerCapabilities, error) {
	return &pb.ServerCapabilities{
		CacheCapabilities: &pb.CacheCapabilities{
			DigestFunctions:               []pb.DigestFunction_Value{pb.DigestFunction_SHA256},
			ActionCacheUpdateCapabilities: &pb.ActionCacheUpdateCapabilities{UpdateEnabled: true},
			SupportedCompressors: []pb.Compressor_Value{
				pb.Compressor_IDENTITY,
				pb.Compressor_ZSTD,
			},
		},
	}, nil
}

func (p *testProxy) UpdateActionResult(ctx context.Context, req *pb.UpdateActionResultRequest) (*pb.ActionResult, error) {
	data, err := proto.Marshal(req.ActionResult)
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(p.dir, cache.AC.DirName(), req.ActionDigest.Hash)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return nil, err
	}
	f, err := os.Create(dest)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	_, err = io.Copy(f, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return req.ActionResult, nil
}

func (p *testProxy) GetActionResult(ctx context.Context, req *pb.GetActionResultRequest) (*pb.ActionResult, error) {
	src := filepath.Join(p.dir, cache.AC.DirName(), req.ActionDigest.Hash)
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	result := &pb.ActionResult{}
	if proto.Unmarshal(data, result) != nil {
		return nil, err
	}
	return result, nil
}

func (p *testProxy) FindMissingBlobs(tx context.Context, req *pb.FindMissingBlobsRequest) (*pb.FindMissingBlobsResponse, error) {
	digests := []*pb.Digest{}
	for _, digest := range req.BlobDigests {
		src := filepath.Join(p.dir, cache.CAS.DirName(), digest.Hash)
		if _, err := os.Stat(src); err != nil {
			digests = append(digests, digest)
		}
	}
	result := pb.FindMissingBlobsResponse{
		MissingBlobDigests: digests,
	}
	return &result, nil
}

func (p *testProxy) Read(req *bs.ReadRequest, resp bs.ByteStream_ReadServer) error {
	parts := strings.Split(req.ResourceName, "/")
	hash := parts[len(parts)-2]
	f, err := os.Open(filepath.Join(p.dir, cache.CAS.DirName(), hash))
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	err = resp.Send(&bs.ReadResponse{Data: data[:len(data)/2]})
	if err != nil {
		return err
	}
	return resp.Send(&bs.ReadResponse{Data: data[len(data)/2:]})
}

func (p *testProxy) Write(srv bs.ByteStream_WriteServer) error {
	var f *os.File
	for {
		req, err := srv.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if f == nil {
			parts := strings.Split(req.ResourceName, "/")
			hash := parts[len(parts)-2]
			dest := filepath.Join(p.dir, cache.CAS.DirName(), hash)
			if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
				return err
			}
			f, err = os.Create(dest)
			if err != nil {
				return err
			}
			defer f.Close()
		}
		_, err = f.Write(req.Data)
		if err != nil {
			return err
		}
	}
}

func (p *testProxy) BatchReadBlobs(context.Context, *pb.BatchReadBlobsRequest) (*pb.BatchReadBlobsResponse, error) {
	return nil, nil
}

func (p *testProxy) BatchUpdateBlobs(context.Context, *pb.BatchUpdateBlobsRequest) (*pb.BatchUpdateBlobsResponse, error) {
	return nil, nil
}

func (p *testProxy) GetTree(*pb.GetTreeRequest, pb.ContentAddressableStorage_GetTreeServer) error {
	return nil
}

func (p *testProxy) QueryWriteStatus(context.Context, *bs.QueryWriteStatusRequest) (*bs.QueryWriteStatusResponse, error) {
	return nil, nil
}

type fixture struct {
	cc      *grpc.ClientConn
	server  *grpc.Server
	clients *GrpcClients
	cache   disk.Cache
}

func newFixture(t *testing.T, proxy cache.Proxy, storageMode string) *fixture {
	listener := bufconn.Listen(1024 * 1024)
	dialer := func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}

	cc, err := grpc.Dial(
		"bufconn",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
	)
	if err != nil {
		t.Fatal(err)
	}
	diskCache, err := disk.New(
		testutils.TempDir(t),
		8*1024*1024,
		disk.WithProxyBackend(proxy),
		disk.WithStorageMode(storageMode),
		disk.WithAccessLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	go func() {
		err := server.ServeGRPC(listener, grpcServer, false, false, true, diskCache, logger, logger)
		if err != nil {
			logger.Printf(err.Error())
		}
	}()

	clients := NewGrpcClients(cc)

	return &fixture{
		cache:   diskCache,
		cc:      cc,
		server:  grpcServer,
		clients: clients,
	}
}

func runTest(t *testing.T, storageMode string) {
	proxyFixture := newProxy(t, testutils.TempDir(t), storageMode)
	putFixture := newFixture(t, proxyFixture.proxy, storageMode)
	getFixture := newFixture(t, proxyFixture.proxy, storageMode)
	time.Sleep(time.Second)

	data, digest := testutils.RandomDataAndDigest(3 * 1024 * 1024)

	putCasReq := &bs.WriteRequest{
		ResourceName: fmt.Sprintf("uploads/%s/blobs/%s/%d", uuid.New().String(), digest.Hash, digest.SizeBytes),
		Data:         data,
	}
	putClient, err := putFixture.clients.bs.Write(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = putClient.Send(putCasReq)
	if err != nil {
		t.Fatal(err)
	}
	_, err = putClient.CloseAndRecv()
	if err != nil {
		t.Fatal(err)
	}
	ar := pb.ActionResult{
		OutputFiles: []*pb.OutputFile{{
			Path:   "foo/bar",
			Digest: &digest,
		}},
		ExitCode: int32(42),
		ExecutionMetadata: &pb.ExecutedActionMetadata{
			Worker: "Test",
		},
	}
	arData, err := proto.Marshal(&ar)
	if err != nil {
		t.Fatal(err)
	}
	arSum := sha256.Sum256(arData)
	arDigest := pb.Digest{
		Hash:      hex.EncodeToString(arSum[:]),
		SizeBytes: int64(len(arData)),
	}
	putAcReq := pb.UpdateActionResultRequest{
		ActionDigest: &arDigest,
		ActionResult: &ar,
	}
	_, err = putFixture.clients.ac.UpdateActionResult(context.Background(), &putAcReq)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)

	ok, size := putFixture.cache.Contains(context.Background(), cache.AC, arDigest.Hash, arDigest.SizeBytes)
	if !ok || size != arDigest.SizeBytes {
		t.Fatal("Cound not find action result in first server")
	}
	ok, size = putFixture.cache.Contains(context.Background(), cache.CAS, digest.Hash, digest.SizeBytes)
	if !ok || size != digest.SizeBytes {
		t.Fatal("Cound not find blob in first server")
	}
	if !proxyFixture.Contains(cache.AC, arDigest.Hash) {
		t.Fatal("Cound not find action result in proxy")
	}
	if !proxyFixture.Contains(cache.CAS, digest.Hash) {
		t.Fatal("Cound not find blob in proxy")
	}

	ok, size = getFixture.cache.Contains(context.Background(), cache.AC, arDigest.Hash, arDigest.SizeBytes)
	if !ok || size != arDigest.SizeBytes {
		t.Fatal("Second server could not find action result")
	}
	ok, size = getFixture.cache.Contains(context.Background(), cache.CAS, digest.Hash, digest.SizeBytes)
	if !ok || size != digest.SizeBytes {
		t.Fatal("Second server could not find blob")
	}

	fmReq := pb.FindMissingBlobsRequest{
		BlobDigests: []*pb.Digest{&digest},
	}
	fmRes, err := getFixture.clients.cas.FindMissingBlobs(context.Background(), &fmReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(fmRes.MissingBlobDigests) > 0 {
		t.Fatal("Second server did not find blob on proxy")
	}

	getAcReq := pb.GetActionResultRequest{
		ActionDigest: &arDigest,
	}
	getAcRes, err := getFixture.clients.ac.GetActionResult(context.Background(), &getAcReq)
	if err != nil {
		t.Fatal(err)
	}
	if err == nil && len(getAcRes.OutputFiles) < 1 {
		t.Fatal("Missing output files from action result")
	}
	if err == nil && getAcRes.OutputFiles[0].Digest.Hash != digest.Hash {
		t.Fatal("Unexpected digest in action result")
	}

	getCasRequest := &bs.ReadRequest{
		ResourceName: fmt.Sprintf("blobs/%s/%d", digest.Hash, digest.SizeBytes),
	}
	getClient, err := getFixture.clients.bs.Read(context.Background(), getCasRequest)
	if err != nil {
		t.Fatal(err)
	}
	received := []byte{}
	for {
		res, err := getClient.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		received = append(received, res.Data...)
	}

	if len(received) != len(data) {
		t.Fatal("Unexpected blob size")
	}
}

func TestEverything(t *testing.T) {
	runTest(t, "uncompressed")
}

func TestEverythingZstd(t *testing.T) {
	runTest(t, "zstd")
}
