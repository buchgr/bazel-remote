package server

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	asset "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/asset/v1"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	testutils "github.com/buchgr/bazel-remote/v2/utils"
)

func TestAssetFetchBlob(t *testing.T) {
	t.Parallel()

	fixture := grpcTestSetup(t)
	defer func() { _ = os.Remove(fixture.tempdir) }()

	ts := newTestGetServer()

	hexSha256 := strings.TrimSuffix(ts.path, ".tar.gz")
	hashBytes, err := hex.DecodeString(hexSha256)
	if err != nil {
		t.Fatal(err)
	}

	req := asset.FetchBlobRequest{
		Uris: []string{
			ts.srv.URL + "/404.unrecognisedextension",
			ts.srv.URL + "/404.tar.gz",
			ts.srv.URL + "/" + ts.path, // This URL should work.
		},
		Qualifiers: []*asset.Qualifier{
			{
				Name: "checksum.sri",
				Value: "sha256-" +
					base64.StdEncoding.EncodeToString([]byte(hashBytes)),
			},
		},
	}

	resp, err := fixture.assetClient.FetchBlob(ctx, &req)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Status.GetCode() != int32(codes.OK) {
		t.Fatal("expected successful fetch")
	}
	if resp.BlobDigest == nil {
		t.Fatal("expected non-bil BlobDigest")
	}
	if resp.BlobDigest.Hash != hexSha256 {
		t.Fatal("mismatching BlobDigest hash returned")
	}
}

func TestAssetMismatchingSRIAlgorithm(t *testing.T) {
	t.Parallel()

	fixture := grpcTestSetup(t)
	defer os.Remove(fixture.tempdir)

	ts := newTestGetServer()

	req := asset.FetchBlobRequest{
		Uris: []string{
			ts.srv.URL + "/" + ts.path, // This URL should work.
		},
		Qualifiers: []*asset.Qualifier{
			{
				Name: "checksum.sri",
				// This is a mismatching algorithm
				// and also a mismatching hash.
				// This should cause an error.
				Value: "sha512-ieYjnbXfruIhY0rGSc4H5uYoEFP42Bj6jtnVK0dlzORoEOE0nJxDBRcjJdN9KHIQkB1y4UYdvHKe1u8/ELU+Ow==",
			},
		},
	}

	_, err := fixture.assetClient.FetchBlob(ctx, &req)
	if err == nil {
		t.Fatal("expected rpc error from fetch")
	}
}

func TestAssetUnsupportedQualifier(t *testing.T) {
	t.Parallel()

	fixture := grpcTestSetup(t)
	defer os.Remove(fixture.tempdir)

	ts := newTestGetServer()

	req := asset.FetchBlobRequest{
		Uris: []string{
			ts.srv.URL + "/" + ts.path, // This URL should work.
		},
		Qualifiers: []*asset.Qualifier{
			{
				Name:  "unknown-qualifier",
				Value: "some-value",
			},
		},
	}

	_, err := fixture.assetClient.FetchBlob(ctx, &req)
	if err == nil {
		t.Fatal(err, "expected rpc error from fetch")
	}
	gstatus, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected a status in rpc error")
	}
	if gstatus.Code() != codes.InvalidArgument {
		t.Fatalf("expected %v status code, got %v", codes.InvalidArgument, gstatus.Code())
	}
	if len(gstatus.Details()) != 1 {
		t.Fatal("expected one detail in rpc status")
	}
	expectedDetail := &errdetails.BadRequest{
		FieldViolations: []*errdetails.BadRequest_FieldViolation{
			{
				Field:       "qualifiers.name",
				Description: `"unknown-qualifier" not supported`,
			},
		},
	}
	protoDetail, ok := gstatus.Details()[0].(*errdetails.BadRequest)
	if !ok {
		t.Fatal("expected BadRequest detail in rpc status")
	}
	if !proto.Equal(protoDetail, expectedDetail) {
		t.Fatalf("expected %v BadRequest, got %v", expectedDetail, protoDetail)
	}
}

type testGetServer struct {
	srv *httptest.Server

	blob []byte
	path string
}

func (s *testGetServer) handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Unsupported method for this test",
			http.StatusMethodNotAllowed)
		return
	}

	if r.URL.Path != "/"+s.path {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)

	if r.Method == http.MethodHead {
		w.Header().Set("ContentLength", fmt.Sprintf("%d", len(s.blob)))
	}

	if r.Method == http.MethodGet {
		_, _ = w.Write(s.blob)
	}
}

func newTestGetServer() *testGetServer {
	blob, hash := testutils.RandomDataAndHash(256)

	ts := testGetServer{
		blob: blob,
		path: hash + ".tar.gz",
	}
	ts.srv = httptest.NewServer(http.HandlerFunc(ts.handler))

	return &ts
}
