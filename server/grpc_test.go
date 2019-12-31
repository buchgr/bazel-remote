package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"testing"
	"time"

	pb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/golang/protobuf/proto"
	"github.com/google/uuid"

	"google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/buchgr/bazel-remote/cache/disk"
	"github.com/buchgr/bazel-remote/utils"
)

type badDigest struct {
	digest pb.Digest
	reason string
}

const bufSize = 1024 * 1024

var (
	listener *bufconn.Listener

	acClient  pb.ActionCacheClient
	casClient pb.ContentAddressableStorageClient
	bsClient  bytestream.ByteStreamClient
	ctx       = context.Background()

	badDigestTestCases = []badDigest{
		{digest: pb.Digest{Hash: ""}, reason: "empty hash"},
		{digest: pb.Digest{Hash: "a"}, reason: "too short"},
		{digest: pb.Digest{Hash: "ab"}, reason: "too short"},
		{digest: pb.Digest{Hash: "abc"}, reason: "too short"},
		{digest: pb.Digest{Hash: "D87BB646700EF8FDD10F6C982A4401EBEFBEA4EF35D4D1516B01FDC25CCE56D4"}, reason: "uppercase hash"},
		{digest: pb.Digest{Hash: "D87BB646700EF8FDD10F6C982A4401EBEFBEA4EF35D4D1516B01FDC25CCE56D41"}, reason: "too long"},
		{digest: pb.Digest{Hash: "xyzbb646700ef8fdd10f6c982a4401ebefbea4ef35d4d1516b01fdc25cce56d4"}, reason: "non-hex characters"},
	}
)

func bufDialer(string, time.Duration) (net.Conn, error) {
	return listener.Dial()
}

func TestMain(m *testing.M) {
	dir, err := ioutil.TempDir("", "bazel-remote-grpc-tests")
	if err != nil {
		fmt.Println("Test setup failed")
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	diskCache := disk.New(dir, int64(10*maxChunkSize))

	accessLogger := testutils.NewSilentLogger()
	errorLogger := testutils.NewSilentLogger()

	listener = bufconn.Listen(bufSize)

	go func() {
		err := ServeGRPC(
			listener,
			[]grpc.ServerOption{},
			diskCache, accessLogger, errorLogger)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	}()

	conn, err := grpc.Dial("bufnet",
		grpc.WithInsecure(), grpc.WithDialer(bufDialer))
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	casClient = pb.NewContentAddressableStorageClient(conn)
	acClient = pb.NewActionCacheClient(conn)
	bsClient = bytestream.NewByteStreamClient(conn)

	os.Exit(m.Run())
}

func checkBadDigestErr(t *testing.T, err error, bd badDigest) {
	if err == nil {
		t.Errorf("Expected an error, %s \"%s\"", bd.reason, bd.digest.Hash)
		return
	}
	statusErr, ok := status.FromError(err)
	if !ok {
		t.Errorf("Expected a grpc status error, %s got: %v", bd.reason, err)
		return
	}
	if statusErr.Code() != codes.InvalidArgument {
		t.Errorf("Expected a grpc status error with code InvalidArgument, %s, got: %d %s",
			bd.reason, statusErr.Code(), statusErr.Message())
		return
	}
}

func TestGrpcAc(t *testing.T) {
	ar := pb.ActionResult{
		StdoutRaw: []byte("pretend action stdout"),
		StderrRaw: []byte("pretend action stderr"),
		ExitCode:  int32(42),
	}

	data, err := proto.Marshal(&ar)
	if err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	digest := pb.Digest{
		Hash:      hash,
		SizeBytes: int64(len(data)),
	}

	// GetActionResultRequest, expect cache miss.

	getReq := pb.GetActionResultRequest{
		ActionDigest:      &digest,
		InlineStdout:      true,
		InlineStderr:      true,
		InlineOutputFiles: []string{},
	}

	gacrResp, err := acClient.GetActionResult(ctx, &getReq)
	if err == nil {
		t.Fatal("Expected NotFound")
	}

	s, ok := status.FromError(err)
	if !ok || s.Code() != codes.NotFound {
		t.Fatal(err)
	}

	// Invalid GetActionResultRequest's.

	for _, tc := range badDigestTestCases {
		r := pb.GetActionResultRequest{ActionDigest: &tc.digest}
		_, err := acClient.GetActionResult(ctx, &r)
		checkBadDigestErr(t, err, tc)
	}

	// UpdateActionResultRequest.

	upACReq := pb.UpdateActionResultRequest{
		ActionDigest: &digest,
		ActionResult: &ar,
	}

	upACResp, err := acClient.UpdateActionResult(ctx, &upACReq)
	if err != nil {
		t.Fatal(err)
	}

	// We expect the returned metadata to have changed.
	if upACResp.ExecutionMetadata == nil {
		t.Fatal("Error: expected ExecutionMetadata to be non-nil")
	}
	if upACResp.ExecutionMetadata.Worker != "bufconn" {
		t.Fatal("Error: expected ExecutionMetadata.Worker to be set")
	}
	// Remove the metadata so we can compare with the request.
	upACResp.ExecutionMetadata = nil

	if !proto.Equal(&ar, upACResp) {
		t.Fatal("Error: uploaded and returned ActionResult differ")
	}

	// Invalid UpdateActionResultRequest's.

	for _, tc := range badDigestTestCases {
		r := pb.UpdateActionResultRequest{ActionDigest: &tc.digest}
		_, err := acClient.UpdateActionResult(ctx, &r)
		checkBadDigestErr(t, err, tc)
	}

	zeroActionResult := pb.ActionResult{}
	zeroData, err := proto.Marshal(&zeroActionResult)
	if err != nil {
		t.Fatal(err)
	}
	if len(zeroData) != 0 {
		t.Fatal("expected a zero-sized test blob")
	}
	_, zeroHash := testutils.RandomDataAndHash(0)
	zeroDigest := pb.Digest{
		Hash:      zeroHash,
		SizeBytes: 0,
	}
	zeroReq := pb.UpdateActionResultRequest{
		ActionDigest: &zeroDigest,
		ActionResult: &zeroActionResult,
	}
	zeroResp, err := acClient.UpdateActionResult(ctx, &zeroReq)
	if proto.Equal(&zeroReq, zeroResp) {
		t.Fatal("expected non-zero ActionResult to be returned")
	}

	// We expect the returned metadata to have changed.
	if zeroResp.ExecutionMetadata == nil {
		t.Fatal("Error: expected ExecutionMetadata to be non-nil")
	}
	if zeroResp.ExecutionMetadata.Worker != "bufconn" {
		t.Fatal("Error: expected ExecutionMetadata.Worker to be set")
	}
	// Remove the metadata so we can compare with the request.
	zeroResp.ExecutionMetadata = nil

	if !proto.Equal(zeroReq.ActionResult, zeroResp) {
		t.Fatal("expected returned ActionResult to only differ by ExecutionMetadata")
	}

	// GetActionResultRequest again, expect cache hit.

	gacrResp, err = acClient.GetActionResult(ctx, &getReq)
	if err != nil {
		t.Fatal(err)
	}

	// We expect the returned metadata to have changed.
	if gacrResp.ExecutionMetadata == nil {
		t.Fatal("Error: expected ExecutionMetadata to be non-nil")
	}
	if gacrResp.ExecutionMetadata.Worker != "bufconn" {
		t.Fatal("Error: expected ExecutionMetadata.Worker to be set")
	}
	// Remove the metadata so we can compare with the request.
	gacrResp.ExecutionMetadata = nil

	if !proto.Equal(&ar, gacrResp) {
		t.Fatal("Error: uploaded and returned ActionResult differ")
	}
}

func TestGrpcAcRequestInlinedBlobs(t *testing.T) {

	// Upload an ActionResult with some inlined blobs.

	testBlobSize := int64(128)

	outputFile, outputFileHash := testutils.RandomDataAndHash(testBlobSize)
	outputFileDigest := pb.Digest{
		Hash:      outputFileHash,
		SizeBytes: testBlobSize,
	}

	_, emptyFileHash := testutils.RandomDataAndHash(int64(0))
	emptyFileDigest := pb.Digest{
		Hash:      emptyFileHash,
		SizeBytes: 0,
	}

	stdoutRaw, stdoutHash := testutils.RandomDataAndHash(testBlobSize)
	stdoutDigest := pb.Digest{
		Hash:      stdoutHash,
		SizeBytes: int64(len(stdoutRaw)),
	}

	stderrRaw, stderrHash := testutils.RandomDataAndHash(testBlobSize)
	stderrDigest := pb.Digest{
		Hash:      stderrHash,
		SizeBytes: int64(len(stderrRaw)),
	}

	ar := pb.ActionResult{
		OutputFiles: []*pb.OutputFile{
			&pb.OutputFile{
				Path:     "foo/bar/grok",
				Digest:   &outputFileDigest,
				Contents: outputFile,
			},

			&pb.OutputFile{
				Path: "foo/bar/empty",
				// Add the empty digest, as an alternative to an empty slice.
				Digest: &emptyFileDigest,
				// Note: don't "upload" the empty slice.
				//Contents: []byte{},
			},
		},
		StdoutRaw:    stdoutRaw,
		StdoutDigest: &stdoutDigest,
		StderrRaw:    stderrRaw,
		StderrDigest: &stderrDigest,
		ExitCode:     int32(42),
	}

	arData, err := proto.Marshal(&ar)
	if err != nil {
		t.Fatal(err)
	}

	arSum := sha256.Sum256(arData)
	arHash := hex.EncodeToString(arSum[:])
	arDigest := pb.Digest{
		Hash:      arHash,
		SizeBytes: int64(len(arData)),
	}

	_, err = acClient.UpdateActionResult(ctx, &pb.UpdateActionResultRequest{
		ActionDigest: &arDigest,
		ActionResult: &ar,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Check that we can download the blobs individually.

	downReq := pb.BatchReadBlobsRequest{
		Digests: []*pb.Digest{
			&outputFileDigest,
			&emptyFileDigest,
			&stdoutDigest,
			&stderrDigest,
		},
	}

	downResp, err := casClient.BatchReadBlobs(ctx, &downReq)
	if err != nil {
		t.Fatal(err)
	}

	if len(downResp.GetResponses()) != len(downReq.Digests) {
		t.Fatal("Expected", len(downReq.Digests), "responses, got",
			len(downResp.GetResponses()))
	}
}

func TestGrpcByteStream(t *testing.T) {

	// Must be large enough to test multiple iterations of the
	// bytestream Read Recv loop.
	testBlobSize := int64(maxChunkSize * 3 / 2)
	testBlob, testBlobHash := testutils.RandomDataAndHash(testBlobSize)
	testBlobDigest := pb.Digest{
		Hash:      testBlobHash,
		SizeBytes: int64(len(testBlob)),
	}

	// Read, expect cache miss.

	instance := "ignoredByteStreamInstance"
	resourceName := fmt.Sprintf("%s/blobs/%s/%d",
		instance, testBlobDigest.Hash, len(testBlob))
	bsrReq := bytestream.ReadRequest{
		ResourceName: resourceName,
	}

	bsrc, err := bsClient.Read(ctx, &bsrReq)
	if err != nil {
		t.Fatal(err)
	}

	bsrResp, err := bsrc.Recv()
	if err == nil {
		t.Fatal("Expected NotFound")
	}

	s, ok := status.FromError(err)
	if !ok || s.Code() != codes.NotFound {
		t.Fatal(err)
	}

	// Write the blob, in two chunks.

	bswc, err := bsClient.Write(ctx)
	if err != nil {
		t.Fatal(err)
	}

	instance = "secondIgnoredInstance"

	cutoff := 128
	blobPart := testBlob[:cutoff]

	bswReq := bytestream.WriteRequest{
		ResourceName: fmt.Sprintf("%s/uploads/%s/blobs/%s/%d/ignored/metadata/here",
			instance, uuid.New().String(), testBlobDigest.Hash, len(testBlob)),
		FinishWrite: false,
		Data:        blobPart,
	}

	err = bswc.Send(&bswReq)
	if err != nil {
		t.Fatal(err)
	}

	blobPart = testBlob[cutoff:]
	bswReq.FinishWrite = true
	bswReq.Data = blobPart

	err = bswc.Send(&bswReq)
	if err != nil {
		t.Fatal(err)
	}

	bswResp, err := bswc.CloseAndRecv()
	if err != nil {
		t.Fatal(err)
	}
	if bswResp.CommittedSize != int64(len(testBlob)) {
		t.Fatalf("Error: expected to write: %d but committed: %d\n",
			len(testBlob), bswResp.CommittedSize)
	}

	// Read again, expect cache hit.

	instance = "thirdIgnoredInstance"

	bsrReq = bytestream.ReadRequest{
		ResourceName: fmt.Sprintf("%s/blobs/%s/%d",
			instance, testBlobDigest.Hash, len(testBlob)),
	}

	bsrc, err = bsClient.Read(ctx, &bsrReq)
	if err != nil {
		t.Fatal(err)
	}

	downloadedBlob := make([]byte, 0, len(testBlob))

	for {
		bsrResp, err = bsrc.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if bsrResp == nil {
			t.Fatalf("Expected non-nil response")
		}

		downloadedBlob = append(downloadedBlob, bsrResp.Data...)

		if len(downloadedBlob) > len(testBlob) {
			t.Fatalf("Downloaded too much data")
		}
	}

	if !bytes.Equal(downloadedBlob, testBlob) {
		t.Fatal("Error: bytestream read failed (data doesn't match)")
	}

	// Invalid Read's.

	for _, tc := range badDigestTestCases {
		r := bytestream.ReadRequest{
			ResourceName: fmt.Sprintf("%s/blobs/%s/42",
				"instance", tc.digest.Hash),
		}
		rc, err := bsClient.Read(ctx, &r)
		_, err = rc.Recv()
		checkBadDigestErr(t, err, tc)
	}

	// Invalid Write's.
	for _, tc := range badDigestTestCases {
		wc, err := bsClient.Write(ctx)
		if err != nil {
			t.Fatal(err)
		}
		r := bytestream.WriteRequest{
			ResourceName: fmt.Sprintf("%s/uploads/%s/blobs/%s/%d/ignored/metadata/here",
				instance, uuid.New().String(), tc.digest.Hash, tc.digest.SizeBytes),
			FinishWrite: false,
			Data:        blobPart,
		}
		err = wc.Send(&r)
		_, err = wc.CloseAndRecv()
		checkBadDigestErr(t, err, tc)
	}

}

func TestGrpcCasBasics(t *testing.T) {

	testBlob, testBlobHash := testutils.RandomDataAndHash(256)
	testBlobDigest := pb.Digest{
		Hash:      testBlobHash,
		SizeBytes: int64(len(testBlob)),
	}

	missingReq := pb.FindMissingBlobsRequest{
		BlobDigests: []*pb.Digest{&testBlobDigest},
	}

	// FindMissingBlobs, expect cache miss.

	missingResp, err := casClient.FindMissingBlobs(ctx, &missingReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(missingResp.MissingBlobDigests) != 1 {
		t.Fatal("Expected 1 missing blob, found",
			len(missingResp.MissingBlobDigests))
	}

	// BatchUpdateBlobs.

	upReq := pb.BatchUpdateBlobsRequest{}
	r := pb.BatchUpdateBlobsRequest_Request{
		Digest: &testBlobDigest,
		Data:   testBlob,
	}
	upReq.Requests = append(upReq.Requests, &r)
	upResp, err := casClient.BatchUpdateBlobs(ctx, &upReq)
	if err != nil {
		t.Fatal(err)
	}

	if len(upResp.GetResponses()) != 1 {
		t.Fatal("Expected 1 response, found",
			len(upResp.GetResponses()))
	}
	if upResp.Responses[0].Digest.Hash != testBlobDigest.Hash ||
		upResp.Responses[0].Digest.SizeBytes != testBlobDigest.SizeBytes {
		t.Fatal("Blobs did not match")
	}

	// FindMissingBlobsRequest again, expect cache hit.

	missingResp, err = casClient.FindMissingBlobs(ctx, &missingReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(missingResp.MissingBlobDigests) != 0 {
		t.Fatal("Expected 0 missing blob, found",
			len(missingResp.MissingBlobDigests))
	}

	// BatchReadBlobsRequest, expect cache hit.

	downReq := pb.BatchReadBlobsRequest{
		Digests: []*pb.Digest{&testBlobDigest},
	}
	downResp, err := casClient.BatchReadBlobs(ctx, &downReq)
	if err != nil {
		t.Fatal(err)
	}

	if len(downResp.GetResponses()) != 1 {
		t.Fatal("Expected 1 response, got", len(downResp.GetResponses()))
	}

	if downResp.Responses[0].Digest.Hash != testBlobDigest.Hash ||
		downResp.Responses[0].Digest.SizeBytes != testBlobDigest.SizeBytes {
		t.Fatalf("Error: expected response for hash %s %d got: %s %d",
			testBlobDigest.Hash, testBlobDigest.SizeBytes,
			downResp.Responses[0].Digest.Hash, downResp.Responses[0].Digest.SizeBytes)
	}
}

func TestGrpcCasTreeRequest(t *testing.T) {

	// Create a test tree, which does not yet exist in the CAS.

	testBlob1, testBlob1Hash := testutils.RandomDataAndHash(64)
	testFile1Digest := pb.Digest{
		Hash:      testBlob1Hash,
		SizeBytes: int64(len(testBlob1)),
	}

	testFile1 := pb.FileNode{
		Name:   "testFile1",
		Digest: &testFile1Digest,
	}

	testBlob2, testBlob2Hash := testutils.RandomDataAndHash(128)
	testFile2Digest := pb.Digest{
		Hash:      testBlob2Hash,
		SizeBytes: int64(len(testBlob2)),
	}

	testFile2 := pb.FileNode{
		Name:   "testFile2",
		Digest: &testFile2Digest,
	}

	testBlob3, testBlob3Hash := testutils.RandomDataAndHash(512)
	testFile3Digest := pb.Digest{
		Hash:      testBlob3Hash,
		SizeBytes: int64(len(testBlob3)),
	}

	testFile3 := pb.FileNode{
		Name:   "testFile3",
		Digest: &testFile3Digest,
	}

	subDir := pb.Directory{
		Files: []*pb.FileNode{
			&testFile2,
			&testFile3,
		},
	}

	subDirData, err := proto.Marshal(&subDir)
	if err != nil {
		t.Fatal(err)
	}
	subDirDataHash := sha256.Sum256(subDirData)
	subDirDataHashStr := hex.EncodeToString(subDirDataHash[:])
	subDirDigest := pb.Digest{
		Hash:      subDirDataHashStr,
		SizeBytes: int64(len(subDirData)),
	}

	subDirNode := pb.DirectoryNode{
		Name:   "subdir",
		Digest: &subDirDigest,
	}

	testTree := pb.Directory{
		Files:       []*pb.FileNode{&testFile1},
		Directories: []*pb.DirectoryNode{&subDirNode},
	}

	treeData, err := proto.Marshal(&testTree)
	if err != nil {
		t.Fatal(err)
	}
	treeHash := sha256.Sum256(treeData)
	treeHashStr := hex.EncodeToString(treeHash[:])
	treeDigest := pb.Digest{
		Hash:      treeHashStr,
		SizeBytes: int64(len(treeData)),
	}

	////////////////////////////////////////////////////////////////////////////

	// GetTreeRequest, expect cache miss.

	req := pb.GetTreeRequest{RootDigest: &treeDigest}

	resp, err := casClient.GetTree(ctx, &req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = resp.Recv()
	if err == nil {
		t.Fatal("Expected NotFound")
	}

	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Fatal(err)
	}

	////////////////////////////////////////////////////////////////////////////

	// Upload all the blobs...

	upReq := pb.BatchUpdateBlobsRequest{
		InstanceName: "foo",
		Requests: []*pb.BatchUpdateBlobsRequest_Request{
			&pb.BatchUpdateBlobsRequest_Request{
				Digest: &testFile1Digest,
				Data:   testBlob1,
			},
			&pb.BatchUpdateBlobsRequest_Request{
				Digest: &testFile2Digest,
				Data:   testBlob2,
			},
			&pb.BatchUpdateBlobsRequest_Request{
				Digest: &testFile3Digest,
				Data:   testBlob3,
			},
			&pb.BatchUpdateBlobsRequest_Request{
				Digest: &subDirDigest,
				Data:   subDirData,
			},
			&pb.BatchUpdateBlobsRequest_Request{
				Digest: &treeDigest,
				Data:   treeData,
			},
		},
	}

	_, err = casClient.BatchUpdateBlobs(ctx, &upReq)
	if err != nil {
		t.Fatal(err)
	}

	////////////////////////////////////////////////////////////////////////////

	// Re-do the GetTreeRequest, expect cache hit and all the data
	// returned in a single Recv.

	resp, err = casClient.GetTree(ctx, &req)
	if err != nil {
		t.Fatal(err)
	}

	tResp, err := resp.Recv()
	if err == io.EOF {
		t.Fatal("Unexpected EOF")
	}
	if err != nil {
		t.Fatal(err)
	}

	if len(tResp.Directories) != 2 {
		// Unnamed top dir and "subdir".
		t.Fatal("Expected two directories")
	}

	if tResp.NextPageToken != "" {
		t.Fatal("Expected only a single response")
	}

	_, err = resp.Recv()
	if err != io.EOF {
		t.Fatal("Expected EOF")
	}

	// The traversal order is not specified, but there are only two
	// directories and therefore only two possible ways to match them.
	//
	// Note: proto.Equal is like reflect.DeepEqual except that it
	// ignores XXX_* fields in generated protobuf structs.
	// https://groups.google.com/forum/#!topic/protobuf/N-elvFu4dFM

	if proto.Equal(tResp.Directories[0], &testTree) {
		if !proto.Equal(tResp.Directories[1], &subDir) {
			t.Fatal("\"subdir\" doesn't match")
		}
	} else if proto.Equal(tResp.Directories[0], &subDir) {
		if !proto.Equal(tResp.Directories[1], &testTree) {
			t.Fatal("Unnamed parent dir doesn't match")
		}
	} else {
		t.Fatal("Neither directory matches")
	}
}
