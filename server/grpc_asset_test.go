package server

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	asset "github.com/bazelbuild/remote-apis/build/bazel/remote/asset/v1"
	//pb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"

	"google.golang.org/grpc/codes"

	testutils "github.com/buchgr/bazel-remote/utils"
)

// These tests rely on TestMain in grpc_test.go.

func TestAssetFetchBlob(t *testing.T) {

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

	resp, err := assetClient.FetchBlob(ctx, &req)
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
		w.Write(s.blob)
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
