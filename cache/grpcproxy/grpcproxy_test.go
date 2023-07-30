package grpcproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"testing"
	"time"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk"
	"github.com/buchgr/bazel-remote/v2/server"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
	testutils "github.com/buchgr/bazel-remote/v2/utils"
	bs "google.golang.org/genproto/googleapis/bytestream"
)

var logger = testutils.NewSilentLogger()

type fixture struct {
	port    int
	host    string
	cc      *grpc.ClientConn
	server  *grpc.Server
	clients *GrpcClients
	cache   disk.Cache
}

func newFixture(t *testing.T, proxy cache.Proxy) *fixture {
	port := rand.Intn(65353-1024) + 1024
	host := fmt.Sprintf("localhost:%d", port)
	cc, err := grpc.Dial(host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	diskCache, err := disk.New(
		testutils.TempDir(t),
		8*1024*1024,
		disk.WithProxyBackend(proxy),
		disk.WithStorageMode("uncompressed"),
		disk.WithAccessLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := grpc.NewServer()
	go func() {
		err := server.ListenAndServeGRPC(grpcServer, "tcp", host, false, false, true, diskCache, logger, logger)
		if err != nil {
			logger.Printf(err.Error())
		}
	}()

	clients := NewGrpcClients(cc)

	return &fixture{
		port:    port,
		host:    host,
		cache:   diskCache,
		cc:      cc,
		server:  grpcServer,
		clients: clients,
	}
}

func TestEverything(t *testing.T) {
	proxyFixture := newFixture(t, nil)
	proxy, err := New(proxyFixture.clients, logger, logger, 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	putFixture := newFixture(t, proxy)
	getFixture := newFixture(t, proxy)
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
	ok, size = proxyFixture.cache.Contains(context.Background(), cache.AC, arDigest.Hash, arDigest.SizeBytes)
	if !ok || size != arDigest.SizeBytes {
		t.Fatal("Cound not find action result in proxy")
	}
	ok, size = proxyFixture.cache.Contains(context.Background(), cache.CAS, digest.Hash, digest.SizeBytes)
	if !ok || size != digest.SizeBytes {
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
