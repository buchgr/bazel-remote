// This is a client which is used by .bazelci/tls-tests.sh and
// ~/.bazelci/basic-auth-tests.sh to verify read/write access
// with and without authentication.

package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"time"

	pb "github.com/buchgr/bazel-remote/genproto/build/bazel/remote/execution/v2"
	"github.com/google/uuid"

	"google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

var emptyDigest = pb.Digest{
	Hash:      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	SizeBytes: 0,
}

func main() {
	// Always required:
	serverAddr := flag.String("server-addr", "",
		"gRPC server address in the form hostname:port")

	readsShouldWork := flag.Bool("reads-should-work", false,
		"whether the server should allow read access in this test")

	// Required for mTLS:
	clientCertFile := flag.String("client-cert-file", "",
		"TLS client certificate file (for mTLS)")
	clientKeyFile := flag.String("client-key-file", "",
		"TLS client key file (for mTLS)")

	// Required when the server uses TLS:
	caCertFile := flag.String("ca-cert-file", "",
		"TLS CA certificate file which signed the server's cert")

	basicAuthUser := flag.String("basic-auth-user", "",
		"Username for basic authentication")

	basicAuthPass := flag.String("basic-auth-pass", "",
		"Password for basic authentication")

	showHelp := flag.Bool("help", false, "Show help")

	flag.Parse()

	if *showHelp {
		flag.PrintDefaults()
		os.Exit(1)
	}

	if *serverAddr == "" {
		fmt.Println("Error: -server-addr must be specified")
		os.Exit(1)
	}

	writesShouldWork := false

	basicAuthFlagCount := 0
	if *basicAuthUser != "" {
		basicAuthFlagCount++
	}
	if *basicAuthPass != "" {
		basicAuthFlagCount++
	}
	if basicAuthFlagCount != 0 {
		if basicAuthFlagCount != 2 {
			fmt.Println("Error: if one of -basic-auth-user or -basic-auth-path are specified, then both must be")
			os.Exit(1)
		}

		*readsShouldWork = true
		writesShouldWork = true
	}

	if *clientCertFile != "" {
		*readsShouldWork = true
		writesShouldWork = true
	}

	if *clientCertFile != "" && basicAuthFlagCount != 0 {
		fmt.Println("Error: only one of basic authentication and mTLS can be used")
		os.Exit(1)
	}

	err := run(*serverAddr, *readsShouldWork, writesShouldWork, *clientCertFile, *clientKeyFile, *caCertFile, *basicAuthUser, *basicAuthPass)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func dial(serverAddr string, caCertFile string, clientCertFile string, clientKeyFile string, basicAuthUser string, basicAuthPass string) (*grpc.ClientConn, error, context.Context, context.CancelFunc) {

	dialOpts := []grpc.DialOption{grpc.WithBlock()}

	if basicAuthUser != "" {
		authority := fmt.Sprintf("%s:%s@%s", basicAuthUser, basicAuthPass, serverAddr)
		dialOpts = append(dialOpts, grpc.WithAuthority(authority))
	}

	if caCertFile == "" {
		creds := insecure.NewCredentials()
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(creds))
	} else {
		fmt.Println("reading", caCertFile)

		caCertData, err := ioutil.ReadFile(caCertFile)
		if err != nil {
			return nil, fmt.Errorf("Failed to read CA cert file %q: %w",
				caCertFile, err), nil, nil
		}

		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCertData) {
			return nil, fmt.Errorf("Failed to create CA certificate pool"), nil, nil
		}

		tlsCfg := &tls.Config{RootCAs: pool}

		if clientCertFile != "" {
			clientCert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
			if err != nil {
				return nil, fmt.Errorf("Failed to read client cert file %q (key file %q): %w",
					clientCertFile, clientKeyFile, err), nil, nil
			}

			tlsCfg.Certificates = []tls.Certificate{clientCert}
		}

		creds := credentials.NewTLS(tlsCfg)

		dialOpts = append(dialOpts, grpc.WithTransportCredentials(creds))
	}

	fmt.Println("Dialing...", serverAddr)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	conn, err := grpc.DialContext(ctx, serverAddr, dialOpts...)
	return conn, err, ctx, cancel
}

func run(serverAddr string, readsShouldWork bool, writesShouldWork bool, clientCertFile string, clientKeyFile string, caCertFile string, basicAuthUser string, basicAuthPass string) error {

	conn, err, ctx, cancel := dial(serverAddr, caCertFile, clientCertFile, clientKeyFile, basicAuthUser, basicAuthPass)
	if conn != nil {
		defer conn.Close()
	}
	defer cancel()

	select {
	case <-ctx.Done():
		if !readsShouldWork && !writesShouldWork {
			fmt.Println("Gave up dialing, as expected:", ctx.Err())
			return nil
		}

		return fmt.Errorf("Failed to connect to %q: %w", serverAddr, ctx.Err())
	default:
	}

	if err != nil || conn == nil {
		return fmt.Errorf("Failed to connect %q: %w", serverAddr, err)
	}

	fmt.Println("Connected.")

	err = checkGetCapabilities(conn, readsShouldWork)
	if err != nil {
		return err
	}

	err = checkCacheReadOps(conn, readsShouldWork)
	if err != nil {
		return err
	}

	err = checkCacheWriteOps(conn, writesShouldWork)
	if err != nil {
		return err
	}

	return nil
}

func checkGetCapabilities(conn *grpc.ClientConn, shouldWork bool) error {
	capClient := pb.NewCapabilitiesClient(conn)

	_, err := capClient.GetCapabilities(context.Background(),
		&pb.GetCapabilitiesRequest{})

	if !shouldWork {
		if err != nil {
			fmt.Println("GetCapabilities failed, as expected")
			return nil
		}
		return fmt.Errorf("Got capabilities when we expected it to fail")
	}

	if err != nil {
		return fmt.Errorf("Failed to get capabilities: %w", err)
	}

	fmt.Println("GetCapabilities succeeded, as expected")
	return nil
}

func checkCacheReadOps(conn *grpc.ClientConn, shouldWork bool) error {

	casClient := pb.NewContentAddressableStorageClient(conn)

	var err error

	err = checkBatchReadBlobs(casClient, shouldWork)
	if err != nil {
		return err
	}

	err = checkFindMissingBlobs(casClient, shouldWork)
	if err != nil {
		return err
	}

	acClient := pb.NewActionCacheClient(conn)

	err = checkGetActionResult(acClient, shouldWork)
	if err != nil {
		return err
	}

	bsClient := bytestream.NewByteStreamClient(conn)

	err = checkBytestreamRead(bsClient, shouldWork)
	if err != nil {
		return err
	}

	return nil
}

func checkBatchReadBlobs(casClient pb.ContentAddressableStorageClient, shouldWork bool) error {
	bg := context.Background()
	brResp, err := casClient.BatchReadBlobs(bg, &pb.BatchReadBlobsRequest{
		Digests: []*pb.Digest{&emptyDigest},
	})

	if !shouldWork {
		if err != nil {
			fmt.Println("BatchReadBlobs failed, as expected")
			return nil
		}
		return fmt.Errorf("BatchReadBlobs succeeded but was expected to fail")
	}

	if err != nil {
		return fmt.Errorf("BatchReadBlobs failed: %w", err)
	}

	if len(brResp.Responses) != 1 {
		return fmt.Errorf("Error: expected 1 digest in the response, found: %d",
			len(brResp.Responses))
	}

	if brResp.Responses[0] == nil {
		return fmt.Errorf("Error: found nil reponse")
	}

	if brResp.Responses[0].Status.Code != int32(codes.OK) {
		return fmt.Errorf("Error: unexpected reponse: %s",
			brResp.Responses[0].Status.GetMessage())
	}

	if brResp.Responses[0].Digest == nil {
		return fmt.Errorf("Error: found nil digest")
	}

	if brResp.Responses[0].Digest.Hash != emptyDigest.Hash || brResp.Responses[0].Digest.SizeBytes != 0 {
		return fmt.Errorf("Error: found different digest")
	}

	if len(brResp.Responses[0].Data) != 0 {
		return fmt.Errorf("Error: found non-empty Data")
	}

	fmt.Println("BatchReadBlobs succeeded, as expected")
	return nil
}

func checkFindMissingBlobs(casClient pb.ContentAddressableStorageClient, shouldWork bool) error {
	req := pb.FindMissingBlobsRequest{
		BlobDigests: []*pb.Digest{&emptyDigest},
	}

	resp, err := casClient.FindMissingBlobs(context.Background(), &req)
	if !shouldWork {
		if err != nil {
			fmt.Println("FindMissingBlobsRequest failed, as expected")
			return nil
		}

		return fmt.Errorf("Expected FindMissingBlobs to fail, but it succeeded")
	}

	if err != nil {
		return fmt.Errorf("FindMissingBlobs failed: %w", err)
	}

	if len(resp.MissingBlobDigests) != 0 {
		// The empty blob should always be available.
		return fmt.Errorf("Expected no missing blobs from FindMissingBlobs call")
	}

	fmt.Println("FindMissingBlobsRequest succeeded, as expected")
	return nil
}

func checkGetActionResult(acClient pb.ActionCacheClient, shouldWork bool) error {

	// We don't expect the cache to contain this entry.
	req := pb.GetActionResultRequest{
		ActionDigest: &emptyDigest,
	}

	ar, err := acClient.GetActionResult(context.Background(), &req)

	if !shouldWork {
		if ar != nil {
			return fmt.Errorf("Expected GetActionResult to fail, but it returned a non-nil ActionResult")
		}

		if err == nil {
			return fmt.Errorf("Expected GetActionResult to fail and return a non-nil error, but the error was nil")
		}

		if status.Code(err) != codes.Unauthenticated {
			return fmt.Errorf("Expected GetActionResult to fail and return Unauthenticated, but got: %s", err.Error())
		}

		fmt.Println("GetActionResult failed, as expected")
		return nil
	}

	if err == nil {
		return fmt.Errorf("Expected a GetActionResult to return NotFound, but it succeeded somehow")
	}

	if status.Code(err) != codes.NotFound {
		return fmt.Errorf("Expected a GetActionResult to return NotFound, but got: %s", err.Error())
	}

	fmt.Println("GetActionResult succeeded, as expected")
	return nil
}

func checkBytestreamRead(bsClient bytestream.ByteStreamClient, shouldWork bool) error {

	resource := fmt.Sprintf("emptyRead/blobs/%s/0", emptyDigest.Hash)
	req := bytestream.ReadRequest{ResourceName: resource}

	bsrc, err := bsClient.Read(context.Background(), &req)

	if !shouldWork {
		if err != nil {
			fmt.Println("BytestreamRead failed, as expected")
			return nil
		}

		_, err := bsrc.Recv() // We seem to fail here. Not sure why not above.
		if err != nil {
			fmt.Println("BytestreamRead failed, as expected")
			return nil
		}

		return fmt.Errorf("Expected bytestream Read to fail, but it succeeded")
	}

	if err != nil {
		return fmt.Errorf("Expected bytestream Read to succeed, got %w", err)
	}

	var downloadedBlob []byte
	for {
		bsrResp, err := bsrc.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("Expected success, got %w", err)
		}
		if bsrResp == nil {
			return fmt.Errorf("Expected non-nil response")
		}

		downloadedBlob = append(downloadedBlob, bsrResp.Data...)

		if len(downloadedBlob) > 0 {
			return fmt.Errorf("Downloaded too much data, expected an empty blob")
		}
	}

	fmt.Println("BytestreamRead succeeded, as expected")
	return nil
}

func checkCacheWriteOps(conn *grpc.ClientConn, shouldWork bool) error {
	var err error

	casClient := pb.NewContentAddressableStorageClient(conn)

	err = checkBatchUpdateBlobs(casClient, shouldWork)
	if err != nil {
		return err
	}

	acClient := pb.NewActionCacheClient(conn)

	err = checkUpdateActionResult(acClient, shouldWork)
	if err != nil {
		return err
	}

	bsClient := bytestream.NewByteStreamClient(conn)

	err = checkBytestreamWrite(bsClient, shouldWork)
	if err != nil {
		return err
	}

	return nil
}

func checkUpdateActionResult(acClient pb.ActionCacheClient, shouldWork bool) error {
	// This is the most important request to get right.

	req := pb.UpdateActionResultRequest{
		ActionDigest: &pb.Digest{
			Hash:      "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
			SizeBytes: 5,
		},
		ActionResult: &pb.ActionResult{
			ExitCode: int32(42),
		},
	}

	_, err := acClient.UpdateActionResult(context.Background(), &req)

	if !shouldWork {
		if err != nil {
			fmt.Println("UpdateActionResult failed, as expected")
			return nil
		}

		return fmt.Errorf("Expected UpdateActionResult to fail, but it succeeded")
	}

	if err != nil {
		return fmt.Errorf("Expected UpdateActionResult to succeed, got: %w", err)
	}

	fmt.Println("UpdateActionResult succeeded, as expected")
	return nil
}

func checkBatchUpdateBlobs(casClient pb.ContentAddressableStorageClient, shouldWork bool) error {

	ur := pb.BatchUpdateBlobsRequest_Request{
		Digest: &pb.Digest{
			Hash:      "1d996e033d612d9af2b44b70061ee0e868bfd14c2dd90b129e1edeb7953e7985",
			SizeBytes: 10,
		},
		Data: []byte("hellothere"),
	}
	req := pb.BatchUpdateBlobsRequest{Requests: []*pb.BatchUpdateBlobsRequest_Request{&ur}}

	resp, err := casClient.BatchUpdateBlobs(context.Background(), &req)

	if !shouldWork {
		if err != nil {
			fmt.Println("BatchUpdateBlobs failed, as expected")
			return nil
		}

		return fmt.Errorf("Expected BatchUpdateBlobs to fail, but it succeeded")
	}

	if err != nil {
		return fmt.Errorf("Expected BatchUpdateBlobs to succeed, got %w", err)
	}

	rs := resp.GetResponses()
	if len(rs) != 1 {
		return fmt.Errorf("Expected BatchUpdateBlobs to have 1 reponse, found %d", len(rs))
	}

	if rs[0].Digest.Hash != ur.Digest.Hash {
		return fmt.Errorf("Unexpected digest in reponse")
	}

	if rs[0].Digest.SizeBytes != ur.Digest.SizeBytes {
		return fmt.Errorf("Unexpected digest in reponse")
	}

	if rs[0].Status != nil && rs[0].Status.Code != int32(codes.OK) {
		return fmt.Errorf("Unexpected unsuccessful response")
	}

	fmt.Println("BatchUpdateBlobs succeeded, as expected")
	return nil
}

func checkBytestreamWrite(bsClient bytestream.ByteStreamClient, shouldWork bool) error {
	bswc, err := bsClient.Write(context.Background())
	if err != nil {
		return fmt.Errorf("BytestreamWrite failed to create ByteStreamClient, got: %w", err)
	}

	now := time.Now().UnixNano()
	nowBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(nowBytes, uint64(now))
	blob := append([]byte("checkBytestreamWrite.testData"), nowBytes...)
	hash := sha256.Sum256(blob)
	blobDigest := pb.Digest{
		Hash:      hex.EncodeToString(hash[:]),
		SizeBytes: int64(len(blob)),
	}

	resource := fmt.Sprintf("uploads/%s/blobs/%s/%d",
		uuid.New().String(), blobDigest.Hash, blobDigest.SizeBytes)

	req := bytestream.WriteRequest{
		ResourceName: resource,
		FinishWrite:  true,
		Data:         blob,
	}

	err = bswc.Send(&req)
	if err != nil && err != io.EOF {
		return fmt.Errorf("BytestreamWrite failed to send data: %w", err)
	}

	resp, err := bswc.CloseAndRecv()
	if !shouldWork {
		if err != nil {
			statusErr, ok := status.FromError(err)
			if ok {
				if statusErr.Code() == codes.Unauthenticated {
					fmt.Println("BytestreamWrite failed, as expected")
					return nil
				}

				return fmt.Errorf("BytestreamWrite got unexpected unauthenticated error: %w", err)
			}

			return fmt.Errorf("BytestreamWrite got unexpected unauthenticated error: %w", err)
		}

		return fmt.Errorf("BytestreamWrite CloseAndRecv succeeded, but expected it to fail")
	}

	if err != nil {
		return fmt.Errorf("BytestreamWrite failed to CloseAndRecv: %w", err)
	}

	if resp.CommittedSize != int64(len(blob)) {
		return fmt.Errorf("BytestreamWrite expected to write %d bytes, but committed %d",
			len(blob), resp.CommittedSize)
	}

	fmt.Println("BytestreamWrite succeeded, as expected")
	return nil
}
