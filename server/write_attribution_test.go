package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	auth "github.com/abbot/go-http-auth"
	"github.com/buchgr/bazel-remote/v2/cache/disk"
	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
	"google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

type attributionLog struct {
	sync.Mutex
	bytes.Buffer
}

func (l *attributionLog) Write(p []byte) (int, error) {
	l.Lock()
	defer l.Unlock()
	return l.Buffer.Write(p)
}

func (l *attributionLog) contents() string {
	l.Lock()
	defer l.Unlock()
	return l.Buffer.String()
}

func TestAuthenticatedWriteAttribution(t *testing.T) {
	var output attributionLog
	logger := log.New(&output, "", 0)
	cache, err := disk.New(t.TempDir(), 1000000, disk.WithAccessLogger(logger))
	if err != nil {
		t.Fatal(err)
	}
	secrets := func(user, realm string) string {
		if user == "writer" {
			return "{SHA}W6ph5Mm5Pz8GgiULbPgzG37mj9g=" // password
		}
		return ""
	}
	basic := NewGrpcBasicAuth(secrets, true)
	gs := grpc.NewServer(grpc.UnaryInterceptor(basic.UnaryServerInterceptor), grpc.StreamInterceptor(basic.StreamServerInterceptor))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer gs.Stop()
	defer listener.Close()
	go func() { _ = ServeGRPC(listener, gs, false, false, false, 1000000, cache, logger, logger) }()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("writer:password"))))
	data := []byte("batch upload")
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	digest := &pb.Digest{Hash: hash, SizeBytes: int64(len(data))}
	cas := pb.NewContentAddressableStorageClient(conn)
	resp, err := cas.BatchUpdateBlobs(ctx, &pb.BatchUpdateBlobsRequest{Requests: []*pb.BatchUpdateBlobsRequest_Request{{Digest: digest, Data: data}}})
	if err != nil || len(resp.GetResponses()) != 1 || resp.Responses[0].Status.Code != 0 {
		t.Fatalf("batch upload: %v %v", resp, err)
	}
	_, err = pb.NewActionCacheClient(conn).UpdateActionResult(ctx, &pb.UpdateActionResultRequest{ActionDigest: digest, ActionResult: &pb.ActionResult{ExitCode: 42}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := bytestream.NewByteStreamClient(conn).Write(ctx)
	if err != nil {
		t.Fatal(err)
	}
	streamData := []byte("stream upload")
	streamHash := fmt.Sprintf("%x", sha256.Sum256(streamData))
	resource := fmt.Sprintf("uploads/test/blobs/%s/%d", streamHash, len(streamData))
	if err := stream.Send(&bytestream.WriteRequest{ResourceName: resource, Data: streamData, FinishWrite: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		t.Fatal(err)
	}
	hc := NewHTTPCache(cache, logger, logger, false, false, false, false, "", "", 1000000)
	ba := &auth.BasicAuth{Realm: "test", Secrets: secrets}
	hs := httptest.NewServer(ba.Wrap(func(w http.ResponseWriter, r *auth.AuthenticatedRequest) {
		hc.CacheHandler(w, r.WithContext(WithAuthenticatedUser(r.Context(), r.Username)))
	}))
	defer hs.Close()
	request, err := http.NewRequest(http.MethodPut, hs.URL+"/cas/"+hash, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("writer", "password")
	httpResp, err := hs.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, httpResp.Body)
	_ = httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP write status: %d", httpResp.StatusCode)
	}
	// A rejected write must not produce a successful attributed entry.
	badCtx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("imposter:password"))))
	if _, err := cas.BatchUpdateBlobs(badCtx, &pb.BatchUpdateBlobsRequest{Requests: []*pb.BatchUpdateBlobsRequest_Request{{Digest: digest, Data: data}}}); err == nil {
		t.Fatal("unauthenticated write succeeded")
	}
	logs := output.contents()
	for _, prefix := range []string{"GRPC CAS PUT " + hash + " OK", "GRPC AC PUT " + hash + " OK", "GRPC BYTESTREAM WRITE COMPLETED: " + resource, "/cas/" + hash} {
		found := false
		for _, line := range strings.Split(logs, "\n") {
			if strings.Contains(line, prefix+" user=writer peer=127.0.0.1:") {
				found = true
			}
		}
		if !found {
			t.Errorf("missing attributed success for %q in:\n%s", prefix, logs)
		}
	}
	if strings.Contains(logs, "user=imposter") || strings.Contains(logs, "password") {
		t.Fatalf("unverified identity or password logged:\n%s", logs)
	}
}

func TestAuthenticatedUserEscaping(t *testing.T) {
	if got := authenticatedUser(WithAuthenticatedUser(context.Background(), "writer\nuser=other")); got != "writer%0Auser%3Dother" {
		t.Fatalf("unsafe log identity: %q", got)
	}
	if got := authenticatedUser(context.Background()); got != "-" {
		t.Fatalf("anonymous identity: %q", got)
	}
}
