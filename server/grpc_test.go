package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"net"
	"os"
	"testing"
	"time"

	asset "github.com/buchgr/bazel-remote/genproto/build/bazel/remote/asset/v1"
	pb "github.com/buchgr/bazel-remote/genproto/build/bazel/remote/execution/v2"
	"github.com/golang/protobuf/proto"
	"github.com/google/uuid"

	"google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/disk"
	"github.com/buchgr/bazel-remote/cache/disk/casblob"
	"github.com/buchgr/bazel-remote/utils"

	"github.com/klauspost/compress/zstd"
)

type badDigest struct {
	digest *pb.Digest
	reason string
}

const bufSize = 1024 * 1024

var (
	listener *bufconn.Listener

	acClient    pb.ActionCacheClient
	casClient   pb.ContentAddressableStorageClient
	bsClient    bytestream.ByteStreamClient
	assetClient asset.FetchClient
	ctx         = context.Background()
	diskCache   *disk.Cache

	badDigestTestCases = []badDigest{
		{digest: &pb.Digest{Hash: ""}, reason: "empty hash"},
		{digest: &pb.Digest{Hash: "a"}, reason: "too short"},
		{digest: &pb.Digest{Hash: "ab"}, reason: "too short"},
		{digest: &pb.Digest{Hash: "abc"}, reason: "too short"},
		{digest: &pb.Digest{Hash: "D87BB646700EF8FDD10F6C982A4401EBEFBEA4EF35D4D1516B01FDC25CCE56D4"}, reason: "uppercase hash"},
		{digest: &pb.Digest{Hash: "D87BB646700EF8FDD10F6C982A4401EBEFBEA4EF35D4D1516B01FDC25CCE56D41"}, reason: "too long"},
		{digest: &pb.Digest{Hash: "xyzbb646700ef8fdd10f6c982a4401ebefbea4ef35d4d1516b01fdc25cce56d4"}, reason: "non-hex characters"},
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

	// Add some overhead for likely CAS blob storage expansion.
	cacheSize := int64(10 * maxChunkSize * 2)

	diskCache, err = disk.New(dir, cacheSize, math.MaxInt64, "zstd", nil, testutils.NewSilentLogger())
	if err != nil {
		fmt.Println("Test setup failed")
		os.Exit(1)
	}

	accessLogger := testutils.NewSilentLogger()
	errorLogger := testutils.NewSilentLogger()

	listener = bufconn.Listen(bufSize)

	validateAC := true
	mangleACKeys := false
	enableRemoteAssetAPI := true
	allowUnauthenticatedReads := false

	go func() {
		err2 := serveGRPC(
			listener,
			[]grpc.ServerOption{},
			validateAC,
			mangleACKeys,
			enableRemoteAssetAPI,
			allowUnauthenticatedReads,
			diskCache, accessLogger, errorLogger)
		if err2 != nil {
			fmt.Println(err2)
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
	assetClient = asset.NewFetchClient(conn)

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
		r := pb.GetActionResultRequest{ActionDigest: tc.digest}
		_, err = acClient.GetActionResult(ctx, &r)
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
		r := pb.UpdateActionResultRequest{ActionDigest: tc.digest}
		_, err = acClient.UpdateActionResult(ctx, &r)
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
	if err != nil {
		t.Fatal(err)
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

func TestGrpcCasEmptySha256(t *testing.T) {

	// Check that we can "download" an empty blob, even if it hasn't
	// been uploaded.

	emptySum := sha256.Sum256([]byte{})
	emptyDigest := pb.Digest{
		Hash:      hex.EncodeToString(emptySum[:]),
		SizeBytes: 0,
	}

	downReq := pb.BatchReadBlobsRequest{
		Digests: []*pb.Digest{&emptyDigest},
	}

	downResp, err := casClient.BatchReadBlobs(ctx, &downReq)
	if err != nil {
		t.Fatal(err)
	}

	if len(downResp.GetResponses()) != 1 {
		t.Fatal("Expected 1 response, got", len(downResp.GetResponses()))
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

	treeWithEmptyFile := pb.Tree{
		Root: &pb.Directory{
			Files: []*pb.FileNode{
				{
					Name: "emptyfile",
					Digest: &pb.Digest{
						Hash: emptySha256,
					},
				},
			},
		},
		Children: []*pb.Directory{
			{
				Files: []*pb.FileNode{
					{
						Name: "emptyfile",
						Digest: &pb.Digest{
							Hash: emptySha256,
						},
					},
				},
			},
		},
	}

	treeWithEmptyFileData, err := proto.Marshal(&treeWithEmptyFile)
	if err != nil {
		t.Fatal(err)
	}
	treeHash := sha256.Sum256(treeWithEmptyFileData)

	treeWithEmptyFileDigest := pb.Digest{
		Hash:      hex.EncodeToString(treeHash[:]),
		SizeBytes: int64(len(treeWithEmptyFileData)),
	}

	// Note that we're uploading the tree data, but not the empty file blob.
	treeUpReq := pb.BatchUpdateBlobsRequest{
		InstanceName: "foo",
		Requests: []*pb.BatchUpdateBlobsRequest_Request{
			{
				Digest: &treeWithEmptyFileDigest,
				Data:   treeWithEmptyFileData,
			},
		},
	}

	_, err = casClient.BatchUpdateBlobs(ctx, &treeUpReq)
	if err != nil {
		t.Fatal(err)
	}

	ar := pb.ActionResult{
		OutputFiles: []*pb.OutputFile{
			{
				Path:     "foo/bar/grok",
				Digest:   &outputFileDigest,
				Contents: outputFile,
			},

			{
				Path: "foo/bar/empty",
				// Add the empty digest, as an alternative to an empty slice.
				Digest: &emptyFileDigest,
				// Note: don't "upload" the empty slice.
				//Contents: []byte{},
			},
		},
		OutputDirectories: []*pb.OutputDirectory{
			{
				Path:       "somedir",
				TreeDigest: &treeWithEmptyFileDigest,
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

	// Check that the blobs exist.

	missingReq := pb.FindMissingBlobsRequest{
		BlobDigests: []*pb.Digest{
			&outputFileDigest,
			&emptyFileDigest,
			&stdoutDigest,
		},
	}

	missingResp, err := casClient.FindMissingBlobs(ctx, &missingReq)
	if err != nil {
		t.Fatal(err)
	}

	if len(missingResp.MissingBlobDigests) != 0 {
		for _, d := range missingResp.MissingBlobDigests {
			t.Log("Blob missing from the CAS:", d.Hash, d.SizeBytes)
		}
		t.Fatal("Expected", len(missingReq.BlobDigests), "blobs, missing",
			len(missingResp.MissingBlobDigests))
	}

	// Double-check that we can actually download the blobs individually.

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

	for _, r := range downResp.GetResponses() {
		if r == nil {
			t.Fatal("nil response in BatchReadBlobsResponse")
		}

		if r.Status == nil {
			t.Fatal("nil status in BatchReadBlobsResponse_Response", r.Digest)
		}

		if r.Status.GetCode() != int32(codes.OK) {
			t.Fatal("missing blob:", r.Digest, "message:", r.Status.GetMessage())
		}
	}

	// Triple-check that we can get the inlined results.
	getAcReq := pb.GetActionResultRequest{
		ActionDigest:      &arDigest,
		InlineStdout:      true,
		InlineStderr:      true,
		InlineOutputFiles: []string{},
	}

	_, err = acClient.GetActionResult(ctx, &getAcReq)
	if err != nil {
		t.Fatal(err)
	}
}

func TestGrpcByteStreamDeadline(t *testing.T) {
	testCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testBlobSize := int64(16)
	testBlob, testBlobHash := testutils.RandomDataAndHash(testBlobSize)
	testBlobDigest := pb.Digest{
		Hash:      testBlobHash,
		SizeBytes: int64(len(testBlob)),
	}

	instance := "deadlineExpired"

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*10)
	defer cancel()

	bswc, err := bsClient.Write(ctx)
	if err != nil {
		t.Fatal(err)
	}

	resourceName := fmt.Sprintf(
		"%s/uploads/%s/blobs/%s/%d/deadline/metadata/here",
		instance,
		uuid.New().String(),
		testBlobDigest.Hash,
		len(testBlob),
	)

	for i := 0; i < len(testBlob); i++ {
		bswReq := bytestream.WriteRequest{
			ResourceName: resourceName,
			FinishWrite:  false,
			Data:         testBlob[i : i+1],
			WriteOffset:  int64(i),
		}

		err := bswc.Send(&bswReq)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Millisecond)
	}

	_, err = bswc.CloseAndRecv()

	statusError, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected rpc error code, got %v\n", err)
	}

	if code := statusError.Code(); code != codes.DeadlineExceeded {
		t.Fatalf("expected codes.DeadlineExceeded, got %s\n", code.String())
	}

	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond*500)
	defer cancel()

	bswc, err = bsClient.Write(ctx)
	if err != nil {
		t.Fatal(err)
	}

	bswReq := bytestream.WriteRequest{
		ResourceName: resourceName,
		FinishWrite:  false,
		Data:         testBlob,
	}
	err = bswc.Send(&bswReq)
	if err != nil {
		t.Fatalf("send error: %v\n", err)
	}

	_, err = bswc.CloseAndRecv()
	if err != nil {
		t.Fatal(err)
	}

	_, sz, err := diskCache.Get(testCtx, cache.CAS, testBlobHash, testBlobSize, 0)
	if err != nil {
		t.Fatalf("get error: %v\n", err)
	}

	if sz != int64(len(testBlob)) {
		t.Errorf("expected size: %d, got: %d\n", len(testBlob), sz)
	}
}

func TestGrpcByteStreamEmptySha256(t *testing.T) {
	// We should always be able to read the empty blob.

	resource := fmt.Sprintf("emptyRead/blobs/%s/0", emptySha256)
	bsrReq := bytestream.ReadRequest{ResourceName: resource}

	bsrc, err := bsClient.Read(ctx, &bsrReq)
	if err != nil {
		t.Fatal(err)
	}

	downloadedBlob := []byte{}
	for {
		bsrResp, err := bsrc.Recv()
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

		if len(downloadedBlob) > 0 {
			t.Fatalf("Downloaded too much data")
		}
	}

	// Also test that we can get the "compressed empty blob".
	// Clients shouldn't do this, but it should be possible.

	resource = fmt.Sprintf("emptyRead/compressed-blobs/zstd/%s/0", emptySha256)
	bsrReq = bytestream.ReadRequest{ResourceName: resource}

	bsrc, err = bsClient.Read(ctx, &bsrReq)
	if err != nil {
		t.Fatal(err)
	}

	downloadedBlob = []byte{}
	for {
		bsrResp, err := bsrc.Recv()
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

		if len(downloadedBlob) > len(emptyZstdBlob) {
			t.Fatalf("Downloaded too much data")
		}
	}

	if !bytes.Equal(downloadedBlob, emptyZstdBlob) {
		// There are many different valid empty zstd blob representations,
		// but we picked this one.
		t.Fatalf("Expected compressed empty blob to be available")
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

	// Read again, in zstd form this time.

	bsrReq = bytestream.ReadRequest{
		ResourceName: fmt.Sprintf("%s/compressed-blobs/zstd/%s/%d",
			instance, testBlobDigest.Hash, len(testBlob)),
	}

	bsrc, err = bsClient.Read(ctx, &bsrReq)
	if err != nil {
		t.Fatal(err)
	}

	var decmpBuf bytes.Buffer
	dr, dw := io.Pipe()
	dec, err := zstd.NewReader(dr, zstd.WithDecoderConcurrency(1))
	errs := make(chan error, 1)

	go func() {
		defer close(errs)

		for {
			bsrResp, err = bsrc.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				errs <- err
				return
			}
			if bsrResp == nil {
				errs <- errors.New("Expected non-nil response")
				return
			}

			dw.Write(bsrResp.Data)

			if len(downloadedBlob) > len(testBlob) {
				errs <- errors.New("Downloaded too much data")
				return
			}
		}

		dw.Close()
	}()

	_, err = io.Copy(&decmpBuf, dec)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(decmpBuf.Bytes(), testBlob) {
		t.Fatal("Error: bytestream compressed read failed (data doesn't match)")
	}

	// Invalid Read's.

	for _, tc := range badDigestTestCases {
		r := bytestream.ReadRequest{
			ResourceName: fmt.Sprintf("%s/blobs/%s/42",
				"instance", tc.digest.Hash),
		}
		rc, err := bsClient.Read(ctx, &r)
		if err != nil {
			t.Fatal(err)
		}
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
		if err != nil {
			t.Fatal(err)
		}
		_, err = wc.CloseAndRecv()
		checkBadDigestErr(t, err, tc)
	}

	err = <-errs
	if err != nil {
		t.Fatal(err)
	}
}

func TestGrpcByteStreamEmptyLastWrite(t *testing.T) {
	instance := "ignoredByteStreamInstance"
	testBlob, testBlobHash := testutils.RandomDataAndHash(7)
	req1 := bytestream.WriteRequest{
		ResourceName: fmt.Sprintf(
			"%s/uploads/%s/blobs/%s/%d",
			instance, uuid.New().String(), testBlobHash, len(testBlob)),
		Data: testBlob,
	}
	req2 := bytestream.WriteRequest{
		FinishWrite: true,
	}
	bswc, err := bsClient.Write(ctx)
	if err != nil {
		t.Fatal(err)
	}

	err = bswc.Send(&req1)
	if err != nil {
		t.Fatal(err)
	}
	err = bswc.Send(&req2)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := bswc.CloseAndRecv()
	if err != nil {
		t.Fatal(err)
	}

	if int(resp.CommittedSize) != len(testBlob) {
		t.Fatal("invalid size")
	}
}

func TestGrpcByteStreamZstdWrite(t *testing.T) {
	// Must be large enough to test multiple iterations of the
	// bytestream Read Recv loop.
	testBlobSize := int64(maxChunkSize * 3 / 2)
	testBlob, testBlobHash := testutils.RandomDataAndHash(testBlobSize)
	testBlobDigest := pb.Digest{
		Hash:      testBlobHash,
		SizeBytes: int64(len(testBlob)),
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	compressedBlob := enc.EncodeAll(testBlob, nil)
	enc.Close()

	bswc, err := bsClient.Write(ctx)
	if err != nil {
		t.Fatal(err)
	}

	instance := "ignoredInstance"

	cutoff := len(compressedBlob) / 2
	blobPart := compressedBlob[:cutoff]

	bswReq := bytestream.WriteRequest{
		ResourceName: fmt.Sprintf("%s/uploads/%s/compressed-blobs/zstd/%s/%d",
			instance, uuid.New().String(), testBlobDigest.Hash, len(testBlob)),
		FinishWrite: false,
		Data:        blobPart,
	}

	err = bswc.Send(&bswReq)
	if err != nil {
		t.Fatal(err)
	}

	blobPart = compressedBlob[cutoff:]
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
	if bswResp.CommittedSize != int64(len(compressedBlob)) {
		t.Fatalf("Error: expected to write: %d but committed: %d\n",
			len(testBlob), bswResp.CommittedSize)
	}

	// Read back.

	bsrReq := bytestream.ReadRequest{
		ResourceName: fmt.Sprintf("%s/blobs/%s/%d",
			instance, testBlobDigest.Hash, len(testBlob)),
	}

	bsrc, err := bsClient.Read(ctx, &bsrReq)
	if err != nil {
		t.Fatal(err)
	}

	downloadedBlob := make([]byte, 0, len(testBlob))

	for {
		bsrResp, err := bsrc.Recv()
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
}

func TestGrpcByteStreamInvalidReadLimit(t *testing.T) {
	testBlobSize := int64(maxChunkSize)
	testBlob, testBlobHash := testutils.RandomDataAndHash(testBlobSize)
	testBlobDigest := pb.Digest{
		Hash:      testBlobHash,
		SizeBytes: int64(len(testBlob)),
	}

	// Check that non-zero ReadLimit for compressed-blobs returns
	// InvalidArgument.
	bsrReq := bytestream.ReadRequest{
		ResourceName: fmt.Sprintf("ignoredinstance/compressed-blobs/zstd/%s/%d",
			testBlobDigest.Hash, len(testBlob)),
		ReadLimit: 1024,
	}

	bsrc, err := bsClient.Read(ctx, &bsrReq)
	if err != nil {
		t.Fatal(err)
	}

	_, err = bsrc.Recv()
	if err == nil || err == io.EOF {
		t.Fatal("Expected error due to non-zero ReadLimit for compressed-blobs read")
	}

	statusErr, ok := status.FromError(err)
	if !ok {
		t.Errorf("Expected a grpc status error, got something else: %v", err)
		return
	}
	if statusErr.Code() != codes.InvalidArgument {
		t.Fatal("Expected InvalidArgument response, got", err)
	}
}

func TestGrpcByteStreamSkippedWrite(t *testing.T) {

	// Must be large enough to test multiple iterations of the
	// bytestream Read Recv loop.
	testBlobSize := int64(maxChunkSize * 3 / 2)
	testBlob, testBlobHash := testutils.RandomDataAndHash(testBlobSize)
	testBlobDigest := pb.Digest{
		Hash:      testBlobHash,
		SizeBytes: int64(len(testBlob)),
	}

	bswc, err := bsClient.Write(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Write the blob, in two chunks.

	cutoff := 128
	firstBlobPart := testBlob[:cutoff]
	secondBlobPart := testBlob[cutoff:]

	bswReq := bytestream.WriteRequest{
		ResourceName: fmt.Sprintf("someInstance/uploads/%s/blobs/%s/%d",
			uuid.New().String(), testBlobDigest.Hash, len(testBlob)),
		FinishWrite: false,
		Data:        firstBlobPart,
	}

	err = bswc.Send(&bswReq)
	if err != nil {
		t.Fatal(err)
	}

	bswReq.FinishWrite = true
	bswReq.Data = secondBlobPart

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

	// Attempt to write the blob again with a new request.

	bswc, err = bsClient.Write(ctx)
	if err != nil {
		t.Fatal(err)
	}

	bswReq.FinishWrite = false
	bswReq.Data = firstBlobPart

	err = bswc.Send(&bswReq)
	if err != nil {
		t.Fatal(err)
	}

	// Expect success without writing the second blob.

	bswResp, err = bswc.CloseAndRecv()
	if err != nil {
		t.Fatal(err)
	}
	if bswResp.CommittedSize != int64(len(testBlob)) {
		t.Fatalf("Error: expected to write: %d but committed: %d\n",
			len(testBlob), bswResp.CommittedSize)
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
			{
				Digest: &testFile1Digest,
				Data:   testBlob1,
			},
			{
				Digest: &testFile2Digest,
				Data:   testBlob2,
			},
			{
				Digest: &testFile3Digest,
				Data:   testBlob3,
			},
			{
				Digest: &subDirDigest,
				Data:   subDirData,
			},
			{
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

func TestBadUpdateActionResultRequest(t *testing.T) {
	digest := pb.Digest{
		Hash:      "0123456789012345678901234567890123456789012345678901234567890123",
		SizeBytes: 1,
	}

	upACReq := pb.UpdateActionResultRequest{
		ActionDigest: &digest,
		ActionResult: &pb.ActionResult{
			OutputFiles: []*pb.OutputFile{
				{
					Path:     "foo/bar",
					Contents: []byte{0, 1, 2, 3, 4},
					// Note: nil digest.
				},
			},
		},
	}

	_, err := acClient.UpdateActionResult(ctx, &upACReq)
	if err != nil {
		t.Fatal(err)
	}
}

func TestParseReadResource(t *testing.T) {
	// Format: [{instance_name}]/blobs/{hash}/{size}

	s := &grpcServer{
		accessLogger: testutils.NewSilentLogger(),
		errorLogger:  testutils.NewSilentLogger(),
	}

	unusedLogPrefix := "foo"

	tcs := []struct {
		resourceName        string
		expectedHash        string
		expectedSize        int64
		expectedCompression casblob.CompressionType
		expectError         bool
	}{
		{
			// No instance specified.
			"blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Identity,
			false,
		},
		{
			// No instance specified.
			"compressed-blobs/zstd/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Zstandard,
			false,
		},
		{
			// Instance specified.
			"foo/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Identity,
			false,
		},
		{
			// Instance specified.
			"foo/compressed-blobs/zstd/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Zstandard,
			false,
		},
		{
			// Instance specified, containing '/'.
			"foo/bar/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Identity,
			false,
		},
		{
			// Instance specified, containing '/'.
			"foo/bar/compressed-blobs/zstd/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Zstandard,
			false,
		},
		{
			// Missing "/blobs/" or "/compressed-blobs/".
			resourceName: "foo/bar/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},

		// Instance names cannot contain the following path segments: blobs,
		// uploads, actions, actionResults, operations or `capabilities. We
		// only care about "blobs".
		{
			resourceName: "blobs/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "blobs/foo/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "foo/blobs/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},

		// Invalid hashes (we only support lowercase SHA256).
		{
			resourceName: "foo/blobs/blobs/01234567890123456789012345678901234567890123456789012345678901234/42",
			expectError:  true,
		},
		{
			resourceName: "foo/blobs/012345678901234567890123456789012345678901234567890123456789012/42",
			expectError:  true,
		},
		{
			resourceName: "foo/blobs/g123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "foo/blobs/A123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true, // Must be lowercase.
		},
		{
			resourceName: "foo/blobs//42",
			expectError:  true,
		},

		// Invalid sizes (must be valid non-negative int64).
		{
			resourceName: "foo/blobs/0123456789012345678901234567890123456789012345678901234567890123/-0",
			expectError:  true,
		},
		{
			// We use -1 as a placeholder for "size unknown" when validating AC digests, but it's invalid here.
			resourceName: "foo/blobs/0123456789012345678901234567890123456789012345678901234567890123/-1",
			expectError:  true,
		},
		{
			resourceName: "foo/blobs/0123456789012345678901234567890123456789012345678901234567890123/3.14",
			expectError:  true,
		},
		{
			// Size: max(int64) + 1
			resourceName: "foo/blobs/0123456789012345678901234567890123456789012345678901234567890123/9223372036854775808",
			expectError:  true,
		},

		// Trailing garbage.
		{
			resourceName: "blobs/0123456789012345678901234567890123456789012345678901234567890123/42abc",
			expectError:  true,
		},
		{
			resourceName: "blobs/0123456789012345678901234567890123456789012345678901234567890123/42/abc",
			expectError:  true,
		},

		// Misc.
		{
			resourceName: "foo/blobs/a",
			expectError:  true,
		},
		{
			resourceName: "foo/blobs//42",
			expectError:  true,
		},

		// Unsupported/unrecognised compression types.
		{
			resourceName: "pretenduuid/compressed-blobs/zstandard/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "pretenduuid/compressed-blobs/Zstd/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "pretenduuid/compressed-blobs/ZSTD/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "pretenduuid/compressed-blobs/identity/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "pretenduuid/compressed-blobs/Identity/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "pretenduuid/compressed-blobs/IDENTITY/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
	}

	for _, tc := range tcs {
		hash, size, cmp, err := s.parseReadResource(tc.resourceName, unusedLogPrefix)

		if tc.expectError {
			if err == nil {
				t.Fatalf("Expected an error for %q, got nil and hash: %q size: %d", tc.resourceName, hash, size)
			}

			continue
		}

		if !tc.expectError && (err != nil) {
			t.Fatalf("Expected an success for %q, got error %q", tc.resourceName, err)
		}

		if hash != tc.expectedHash {
			t.Fatalf("Expected hash: %q did not match actual hash: %q in %q", tc.expectedHash, hash, tc.resourceName)
		}

		if size != tc.expectedSize {
			t.Fatalf("Expected size: %d did not match actual size: %d in %q", tc.expectedSize, size, tc.resourceName)
		}

		if cmp != tc.expectedCompression {
			t.Fatalf("Expected compressor: %d did not match actual compressor: %d in %q", tc.expectedCompression, cmp, tc.resourceName)
		}
	}
}

func TestParseWriteResource(t *testing.T) {
	// Format: [{instance_name}/]uploads/{uuid}/blobs/{hash}/{size}[/{optionalmetadata}]
	// Or: [{instance_name}/]uploads/{uuid}/compressed-blobs/{compressor}/{uncompressed_hash}/{uncompressed_size}[{/optional_metadata}]

	// We ignore instance_name and metadata, and we don't verify that the
	// uuid is valid- it just needs to exist (or be empty) and not contain '/'.

	s := &grpcServer{
		accessLogger: testutils.NewSilentLogger(),
		errorLogger:  testutils.NewSilentLogger(),
	}

	tcs := []struct {
		resourceName        string
		expectedHash        string
		expectedSize        int64
		expectedCompression casblob.CompressionType
		expectError         bool
	}{
		{
			"foo/uploads/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Identity,
			false,
		},
		{
			"foo/uploads/pretenduuid/compressed-blobs/zstd/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Zstandard,
			false,
		},
		{
			"uploads/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Identity,
			false,
		},
		{
			"uploads/pretenduuid/compressed-blobs/zstd/0123456789012345678901234567890123456789012345678901234567890123/42",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Zstandard,
			false,
		},
		{
			// max(int64)
			"uploads/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/9223372036854775807",
			"0123456789012345678901234567890123456789012345678901234567890123",
			9223372036854775807,
			casblob.Identity,
			false,
		},
		{
			// max(int64)
			"uploads/pretenduuid/compressed-blobs/zstd/0123456789012345678901234567890123456789012345678901234567890123/9223372036854775807",
			"0123456789012345678901234567890123456789012345678901234567890123",
			9223372036854775807,
			casblob.Zstandard,
			false,
		},
		{
			"foo/uploads/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/42/some/meta/data",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Identity,
			false,
		},
		{
			"foo/uploads/pretenduuid/compressed-blobs/zstd/0123456789012345678901234567890123456789012345678901234567890123/42/some/meta/data",
			"0123456789012345678901234567890123456789012345678901234567890123",
			42,
			casblob.Zstandard,
			false,
		},

		// Missing "uploads"
		{
			resourceName: "/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},

		{
			// Missing uuid.
			resourceName: "uploads/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			// Multiple segments in place of uuid.
			resourceName: "uploads/uuid/with/segments/blobs/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},

		// Invalid hashes.
		{
			// Too long.
			resourceName: "uploads/pretenduuid/blobs/01234567890123456789012345678901234567890123456789012345678901234/42",
			expectError:  true,
		},
		{
			// Too short.
			resourceName: "uploads/pretenduuid/blobs/012345678901234567890123456789012345678901234567890123456789012/42",
			expectError:  true,
		},
		{
			// Not lowercase.
			resourceName: "uploads/pretenduuid/blobs/A123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/blobs//42", // Missing entirely.
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/blobs/42", // Missing entirely.
			expectError:  true,
		},

		// Invalid sizes (must be valid non-negative int64).
		{
			resourceName: "uploads/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/-0",
			expectError:  true,
		},
		{
			// We use -1 as a placeholder for "size unknown" when validating AC digests, but it's invalid here.
			resourceName: "uploads/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/-1",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/blobs/0123456789012345678901234567890123456789012345678901234567890123/2.71828",
			expectError:  true,
		},
		{
			// Size: max(int64) + 1
			resourceName: "foo/blobs/0123456789012345678901234567890123456789012345678901234567890123/9223372036854775808",
			expectError:  true,
		},

		// Unsupported/unrecognised compression types.
		{
			resourceName: "uploads/pretenduuid/compressed-blobs/zstandard/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/compressed-blobs/Zstd/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/compressed-blobs/ZSTD/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/compressed-blobs/identity/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/compressed-blobs/Identity/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
		{
			resourceName: "uploads/pretenduuid/compressed-blobs/IDENTITY/0123456789012345678901234567890123456789012345678901234567890123/42",
			expectError:  true,
		},
	}

	for _, tc := range tcs {
		hash, size, cmp, err := s.parseWriteResource(tc.resourceName)

		if tc.expectError {
			if err == nil {
				t.Fatalf("Expected an error for %q, got nil and hash: %q size: %d", tc.resourceName, hash, size)
			}

			continue
		}

		if !tc.expectError && (err != nil) {
			t.Fatalf("Expected an success for %q, got error %q", tc.resourceName, err)
		}

		if hash != tc.expectedHash {
			t.Fatalf("Expected hash: %q did not match actual hash: %q in %q", tc.expectedHash, hash, tc.resourceName)
		}

		if size != tc.expectedSize {
			t.Fatalf("Expected size: %d did not match actual size: %d in %q", tc.expectedSize, size, tc.resourceName)
		}

		if cmp != tc.expectedCompression {
			t.Fatalf("Expected compression: %d did not match actual compression: %d in %q", tc.expectedCompression, cmp, tc.resourceName)
		}
	}
}
