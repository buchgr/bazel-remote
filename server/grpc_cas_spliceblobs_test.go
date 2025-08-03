package server

import (
	"bytes"
	"context"
	"math"
	"os"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
)

// Create a test cache populated with "hello" and "world" blobs.
//
// Return a grpcTestFixtureWithTmpDirCache, digests of the two blobs in the cache,
// and for a "helloworld" blob (which is not yet present in the cache).
func spliceBlobTestSetup(t *testing.T) (fixture grpcTestFixtureWithTmpDirCache,
	helloDigest *pb.Digest, worldDigest *pb.Digest, helloworldDigest *pb.Digest) {

	fixture = grpcTestSetup(t)

	ctx := context.Background()

	// Upload "hello" and "world" blobs.

	helloDigest = &pb.Digest{
		Hash:      "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
		SizeBytes: 5,
	}

	worldDigest = &pb.Digest{
		Hash:      "486ea46224d1bb4fb680f34f7c9ad96a8f24ec88be73ea8e5a6c65260e9cb8a7",
		SizeBytes: 5,
	}

	helloworldDigest = &pb.Digest{
		Hash:      "936a185caaa266bb9cbe981e9e05cb78cd732b0b3280eb944412bb6f8f8f07af",
		SizeBytes: 10,
	}

	upReq := pb.BatchUpdateBlobsRequest{
		Requests: []*pb.BatchUpdateBlobsRequest_Request{
			{
				Digest: helloDigest,
				Data:   []byte("hello"),
			},
			{
				Digest: worldDigest,
				Data:   []byte("world"),
			},
		},
	}

	upResp, err := fixture.casClient.BatchUpdateBlobs(ctx, &upReq)
	if err != nil {
		t.Fatal(err)
	}

	if upResp.Responses == nil {
		t.Fatal("expected non-nil BatchUpdateBlobsResponse.Responses")
	}

	if len(upResp.Responses) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(upResp.Responses))
	}

	for _, r := range upResp.Responses {
		if r.Digest == nil {
			t.Fatal("got nil BatchUpdateBlobsResponse_Response.Digest")
		}
		if r.Status == nil {
			t.Fatal("got nil BatchUpdateBlobsResponse_Response.Status")
		}
		if r.Status.Code != int32(codes.OK) {
			t.Fatalf("failed to upload blob %s/%d: %d %s", r.Digest.Hash, r.Digest.SizeBytes, r.Status.GetCode(), r.Status.GetMessage())
		}
	}

	// Confirm that the "helloworld" blob does not exist in the cache yet.

	downloadReq := &pb.BatchReadBlobsRequest{
		Digests: []*pb.Digest{helloworldDigest},
	}

	downResp, err := fixture.casClient.BatchReadBlobs(ctx, downloadReq)
	if err != nil {
		t.Fatal(err)
	}

	if downResp == nil {
		t.Fatal("got nil BatchReadBlobsResponse")
	}

	if downResp.Responses == nil {
		t.Fatal("got nil BatchReadBlobsResponse.Responses")
	}

	if len(downResp.Responses) != 1 {
		t.Fatal("expected 1 response, got", len(downResp.Responses))
	}

	if downResp.Responses[0].Digest == nil {
		t.Fatal("got nil value in BatchReadBlobsResponse_Responses")
	}

	if downResp.Responses[0].Digest.Hash != helloworldDigest.Hash || downResp.Responses[0].Digest.SizeBytes != helloworldDigest.SizeBytes {
		t.Fatal("expected helloworldDigest in response")
	}

	if downResp.Responses[0].Status == nil {
		t.Fatal("expected non-nil BatchReadBlobsResponse_Response.Status")
	}

	if downResp.Responses[0].Status.Code != int32(codes.NotFound) {
		t.Fatal("expected \"helloworld\" blob not to exist in the cache yet")
	}

	return fixture, helloDigest, worldDigest, helloworldDigest
}

func TestSpliceBlobCapability(t *testing.T) {
	// The capabilities API should report that SpliceBlob is supported.

	fixture := grpcTestSetup(t)
	defer func() { _ = os.Remove(fixture.tempdir) }()

	resp, err := fixture.capabilitiesClient.GetCapabilities(context.Background(), &pb.GetCapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}

	if resp == nil {
		t.Fatal("expected non-nil *ServerCapabilities")
	}

	cacheCapabilities := resp.GetCacheCapabilities()
	if cacheCapabilities == nil {
		t.Fatal("expected non-nil *CacheCapabilities")
	}

	if !cacheCapabilities.BlobSpliceSupport {
		t.Fatal("expected CacheCapabilities.BlobSpliceSupport to be true")
	}
}

func TestSpliceBlobWithBlobDigest(t *testing.T) {
	testSpliceBlob(t, true)
}

func TestSpliceBlobWithoutBlobDigest(t *testing.T) {
	testSpliceBlob(t, false)
}

func testSpliceBlob(t *testing.T, withBlobDigest bool) {
	fixture, helloDigest, worldDigest, helloworldDigest := spliceBlobTestSetup(t)
	defer func() { _ = os.Remove(fixture.tempdir) }()

	ctx := context.Background()

	// Splice together the "hello" and "world" blobs to form "helloworld".

	var blobDigest *pb.Digest
	if withBlobDigest {
		blobDigest = helloworldDigest
	}

	spliceReq := pb.SpliceBlobRequest{
		BlobDigest: blobDigest,
		ChunkDigests: []*pb.Digest{
			helloDigest,
			worldDigest,
		},
	}
	spliceResp, err := fixture.casClient.SpliceBlob(ctx, &spliceReq)
	if err != nil {
		t.Fatal(err)
	}

	if spliceResp == nil {
		t.Fatal("got nil SpliceBlobResponse")
	}

	if spliceResp.BlobDigest == nil {
		t.Fatal("got nil SpliceBlobResponse.BlobDigest")
	}

	if spliceResp.BlobDigest.Hash != helloworldDigest.Hash || spliceResp.BlobDigest.SizeBytes != helloworldDigest.SizeBytes {
		t.Fatalf("SpliceBlob returned an unexpected digest: %s/%d", spliceResp.BlobDigest.Hash, spliceResp.BlobDigest.SizeBytes)
	}

	// Confirm that we can download the "helloworld" blob now.

	downloadReq := pb.BatchReadBlobsRequest{
		Digests: []*pb.Digest{helloworldDigest},
	}

	downResp, err := fixture.casClient.BatchReadBlobs(ctx, &downloadReq)
	if err != nil {
		t.Fatal(err)
	}

	if downResp == nil {
		t.Fatal("got nil BatchReadBlobsResponse")
	}

	if downResp.Responses == nil {
		t.Fatal("got nil BatchReadBlobsResponse.Responses")
	}

	if len(downResp.Responses) != 1 {
		t.Fatal("expected 1 response, got", len(downResp.Responses))
	}

	if downResp.Responses[0].Digest == nil {
		t.Fatal("got nil value in BatchReadBlobsResponse_Responses")
	}

	if downResp.Responses[0].Digest.Hash != helloworldDigest.Hash || downResp.Responses[0].Digest.SizeBytes != helloworldDigest.SizeBytes {
		t.Fatal("got unexpected response hash")
	}

	if downResp.Responses[0].Status == nil {
		t.Fatal("got nil BatchReadBlobsResponse_Response.Status")
	}

	if downResp.Responses[0].Status.Code != int32(codes.OK) {
		t.Fatalf("expected status OK, got: %d", downResp.Responses[0].Status.Code)
	}

	if !bytes.Equal(downResp.Responses[0].Data, []byte("helloworld")) {
		t.Fatal("expected to get \"helloworld\" bytes, got", downResp.Responses[0].Data)
	}
}

func TestSpliceBlobWithOverflow(t *testing.T) {
	fixture, _, _, helloworldDigest := spliceBlobTestSetup(t)
	defer func() { _ = os.Remove(fixture.tempdir) }()

	ctx := context.Background()

	largeDigest := pb.Digest{
		Hash:      "1111111111222222222233333333334444444444555555555566666666667777",
		SizeBytes: math.MaxInt64 - 2,
	}

	spliceReq := pb.SpliceBlobRequest{
		BlobDigest: helloworldDigest,
		ChunkDigests: []*pb.Digest{
			&largeDigest,
			&largeDigest,
		},
	}

	_, err := fixture.casClient.SpliceBlob(ctx, &spliceReq)
	if err == nil {
		t.Fatal("expected error due to overflow")
	}

	if status.Code(err) != codes.InvalidArgument {
		// In particular, we don't want to see NotFound here, because that means
		// we queried the index needlessly.
		t.Fatal("expected an InvalidArgument status code")
	}
}

func TestSpliceBlobWithEmptyChunk(t *testing.T) {
	// Empty chunks are useless, if we see one something is probably wrong.

	fixture, helloDigest, worldDigest, helloworldDigest := spliceBlobTestSetup(t)
	defer func() { _ = os.Remove(fixture.tempdir) }()

	ctx := context.Background()

	// Splice together the "hello" and "world" blobs to form "helloworld",
	// but with a useless empty chunk.

	emptyBlob := &pb.Digest{
		Hash:      emptySha256,
		SizeBytes: 0,
	}

	spliceReq := pb.SpliceBlobRequest{
		BlobDigest: helloworldDigest,
		ChunkDigests: []*pb.Digest{
			emptyBlob,
			helloDigest,
			worldDigest,
		},
	}
	spliceResp, err := fixture.casClient.SpliceBlob(ctx, &spliceReq)
	if err == nil {
		t.Error("expected an error due to the empty chunk")
	}

	if spliceResp != nil {
		t.Error("expected a nil SpliceBlobResponse")
	}

	if status.Code(err) != codes.InvalidArgument {
		t.Error("expected an InvalidArgument result, got", status.Code(err))
	}
}

func TestSpliceBlobWithMismatchedDigest(t *testing.T) {
	// SpliceBlob requests should fail if the actual digest does not match the result.

	fixture, helloDigest, worldDigest, helloworldDigest := spliceBlobTestSetup(t)
	defer func() { _ = os.Remove(fixture.tempdir) }()

	ctx := context.Background()

	// Splice together the "hello" and "world" blobs to form "helloworld",
	// but with a useless empty chunk.

	invalidHelloWorldDigest := &pb.Digest{
		Hash:      helloworldDigest.Hash,
		SizeBytes: helloworldDigest.SizeBytes + 1,
	}

	spliceReq := pb.SpliceBlobRequest{
		BlobDigest: invalidHelloWorldDigest,
		ChunkDigests: []*pb.Digest{
			helloDigest,
			worldDigest,
		},
	}

	_, err := fixture.casClient.SpliceBlob(ctx, &spliceReq)
	if err == nil {
		t.Fatal("expected an error when the BlobDigest does not match (SizeBytes too large)")
	}

	invalidHelloWorldDigest.SizeBytes = helloworldDigest.SizeBytes - 1

	_, err = fixture.casClient.SpliceBlob(ctx, &spliceReq)
	if err == nil {
		t.Fatal("expected an error when the BlobDigest does not match (SizeBytes too small)")
	}

	invalidHelloWorldDigest.SizeBytes = 0

	_, err = fixture.casClient.SpliceBlob(ctx, &spliceReq)
	if err == nil {
		t.Fatal("expected an error when the BlobDigest does not match (SizeBytes 0)")
	}

	invalidHelloWorldDigest.SizeBytes = -1

	_, err = fixture.casClient.SpliceBlob(ctx, &spliceReq)
	if err == nil {
		t.Fatal("expected an error when the BlobDigest does not match (SizeBytes negative)")
	}

	invalidHelloWorldDigest.SizeBytes = helloworldDigest.SizeBytes
	invalidHelloWorldDigest.Hash = helloworldDigest.Hash + "a"

	_, err = fixture.casClient.SpliceBlob(ctx, &spliceReq)
	if err == nil {
		t.Fatal("expected an error when the BlobDigest does not match (Hash too long)")
	}

	invalidHelloWorldDigest.Hash = helloworldDigest.Hash[1:]

	_, err = fixture.casClient.SpliceBlob(ctx, &spliceReq)
	if err == nil {
		t.Fatal("expected an error when the BlobDigest does not match (Hash too short)")
	}

	invalidHelloWorldDigest.Hash = strings.ToUpper(helloworldDigest.Hash)

	_, err = fixture.casClient.SpliceBlob(ctx, &spliceReq)
	if err == nil {
		t.Fatal("expected an error when the BlobDigest does not match (Hash wrong case)")
	}
}

func TestSpliceBlobWithMissingChunk(t *testing.T) {
	// If there's a missing chunk, SpliceBlob should fail and mention the missing chunk.

	fixture, helloDigest, worldDigest, _ := spliceBlobTestSetup(t)
	defer func() { _ = os.Remove(fixture.tempdir) }()

	ctx := context.Background()

	// Splice together the "hello" and "world" blobs to form "helloworld",
	// but with a useless empty chunk.

	// "helloworldmissing" blob
	helloworldmissingDigest := &pb.Digest{
		Hash:      "8a4ed91df71b00030d354dbf98a65135ed9a94939ad2ffd4b49a7cf14fc54ad2",
		SizeBytes: 17,
	}

	// "missing" blob
	missingDigest := &pb.Digest{
		Hash:      "ffa63583dfa6706b87d284b86b0d693a161e4840aad2c5cf6b5d27c3b9621f7d",
		SizeBytes: 7,
	}

	spliceReq := pb.SpliceBlobRequest{
		BlobDigest: helloworldmissingDigest,
		ChunkDigests: []*pb.Digest{
			helloDigest,
			worldDigest,
			missingDigest,
		},
	}

	_, err := fixture.casClient.SpliceBlob(ctx, &spliceReq)
	if err == nil {
		t.Fatal("expected NotFound error due to \"missing\" not existing in the cache")
	}

	if status.Code(err) != codes.NotFound {
		t.Fatal("expected \"missing\" blob not to exist in the cache yet, but got error", err)
	}
}

func TestUnsupportedSpliceBlobDigestsAreRejected(t *testing.T) {
	fixture, helloDigest, worldDigest, helloworldDigest := spliceBlobTestSetup(t)
	defer func() { _ = os.Remove(fixture.tempdir) }()

	spliceReq := pb.SpliceBlobRequest{
		BlobDigest: helloworldDigest,
		ChunkDigests: []*pb.Digest{
			helloDigest,
			worldDigest,
		},
		DigestFunction: pb.DigestFunction_MD5, // Unsupported
	}

	_, err := fixture.casClient.SpliceBlob(ctx, &spliceReq)
	if err == nil {
		t.Fatal("expected error when specifying an unsupported SpliceBlobRequest.DigestFunction")
	}

	if status.Code(err) != codes.InvalidArgument {
		t.Fatal("expected an InvalidArgument error, got:", err)
	}
}
