package grpcproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk"
	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
	"github.com/buchgr/bazel-remote/v2/server"
	"github.com/klauspost/compress/zstd"
	bs "google.golang.org/genproto/googleapis/bytestream"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func readStatusConn(t *testing.T, srv *grpc.Server, listener *bufconn.Listener) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///read-status", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); srv.Stop(); listener.Close() })
	return conn
}

func readStatusCache(t *testing.T, proxy cache.Proxy, mode string) (*grpc.ClientConn, disk.Cache, string) {
	t.Helper()
	dir := t.TempDir()
	opts := []disk.Option{disk.WithStorageMode(mode), disk.WithAccessLogger(logger)}
	if proxy != nil {
		opts = append(opts, disk.WithProxyBackend(proxy))
	}
	c, err := disk.New(dir, 16*1024*1024, opts...)
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	go func() { _ = server.ServeGRPC(listener, srv, true, false, false, 8*1024*1024, c, logger, logger) }()
	return readStatusConn(t, srv, listener), c, dir
}

// Exercise both response compressors through both CAS read APIs.
func checkCASReads(t *testing.T, conn *grpc.ClientConn, digest *pb.Digest, want codes.Code, data []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprintf("zstd=%v", compressed), func(t *testing.T) {
			req := &pb.BatchReadBlobsRequest{Digests: []*pb.Digest{digest}}
			prefix := "blobs/"
			if compressed {
				req.AcceptableCompressors = []pb.Compressor_Value{pb.Compressor_ZSTD}
				prefix = "compressed-blobs/zstd/"
			}
			batch, err := pb.NewContentAddressableStorageClient(conn).BatchReadBlobs(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if len(batch.Responses) != 1 {
				t.Fatalf("responses: %d", len(batch.Responses))
			}
			got := batch.Responses[0]
			if codes.Code(got.Status.GetCode()) != want {
				t.Errorf("batch: got %s, want %s", codes.Code(got.Status.GetCode()), want)
			}
			var streamData []byte
			stream, err := bs.NewByteStreamClient(conn).Read(ctx, &bs.ReadRequest{ResourceName: fmt.Sprintf("%s%s/%d", prefix, digest.Hash, digest.SizeBytes)})
			if err == nil {
				for {
					var msg *bs.ReadResponse
					msg, err = stream.Recv()
					if err != nil {
						break
					}
					streamData = append(streamData, msg.Data...)
				}
			}
			if want == codes.OK && err == io.EOF {
				err = nil
			}
			if status.Code(err) != want {
				t.Errorf("stream: got %v, want %s", err, want)
			}
			if want != codes.OK {
				if len(got.Data) != 0 || len(streamData) != 0 {
					t.Error("failed read returned payload")
				}
				return
			}
			for _, payload := range [][]byte{got.Data, streamData} {
				if compressed {
					dec, err := zstd.NewReader(nil)
					if err != nil {
						t.Fatal(err)
					}
					payload, err = dec.DecodeAll(payload, nil)
					dec.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				if !bytes.Equal(payload, data) {
					t.Error("incorrect blob data")
				}
			}
		})
	}
}

type failingReadServer struct {
	bs.UnimplementedByteStreamServer
	code    codes.Code
	partial []byte
}

func (s *failingReadServer) Read(_ *bs.ReadRequest, stream bs.ByteStream_ReadServer) error {
	if s.partial != nil {
		if err := stream.Send(&bs.ReadResponse{Data: s.partial}); err != nil {
			return err
		}
	}
	return status.Error(s.code, "injected upstream failure")
}

func TestProxyCASReadStatus(t *testing.T) {
	data := []byte("synthetic CAS read status regression blob")
	digest := &pb.Digest{Hash: fmt.Sprintf("%x", sha256.Sum256(data)), SizeBytes: int64(len(data))}
	for _, mode := range []string{"uncompressed", "zstd"} {
		t.Run(mode, func(t *testing.T) {
			upstream, c, _ := readStatusCache(t, nil, mode)
			proxy := New(NewGrpcClients(upstream), mode, logger, logger, 0, 0)
			downstream, _, _ := readStatusCache(t, proxy, mode)
			for _, endpoint := range []struct {
				name string
				conn *grpc.ClientConn
			}{{"direct", upstream}, {"proxy", downstream}} {
				t.Run(endpoint.name+"/missing", func(t *testing.T) { checkCASReads(t, endpoint.conn, digest, codes.NotFound, nil) })
			}
			if err := c.Put(context.Background(), cache.CAS, digest.Hash, digest.SizeBytes, bytes.NewReader(data)); err != nil {
				t.Fatal(err)
			}
			for _, endpoint := range []struct {
				name string
				conn *grpc.ClientConn
			}{{"direct", upstream}, {"proxy", downstream}} {
				t.Run(endpoint.name+"/present", func(t *testing.T) { checkCASReads(t, endpoint.conn, digest, codes.OK, data) })
			}
		})
	}
}

func TestProxyCASReadFailures(t *testing.T) {
	data := []byte("synthetic incomplete CAS blob")
	digest := &pb.Digest{Hash: fmt.Sprintf("%x", sha256.Sum256(data)), SizeBytes: int64(len(data))}
	for _, mode := range []string{"uncompressed", "zstd"} {
		for _, partial := range []bool{false, true} {
			for _, code := range []codes.Code{codes.NotFound, codes.Unavailable, codes.DeadlineExceeded, codes.PermissionDenied, codes.Unauthenticated, codes.Canceled, codes.ResourceExhausted, codes.Unknown} {
				t.Run(fmt.Sprintf("%s/partial=%v/%s", mode, partial, code), func(t *testing.T) {
					srv := grpc.NewServer()
					listener := bufconn.Listen(1024 * 1024)
					upstream := &failingReadServer{code: code}
					if partial {
						upstream.partial = data[:8]
						if mode == "zstd" {
							upstream.partial = []byte{0x28, 0xb5, 0x2f, 0xfd, 0x20, 0x30}
						}
					}
					bs.RegisterByteStreamServer(srv, upstream)
					go func() { _ = srv.Serve(listener) }()
					conn := readStatusConn(t, srv, listener)
					proxy := New(NewGrpcClients(conn), mode, logger, logger, 0, 0)
					downstream, c, dir := readStatusCache(t, proxy, mode)
					want := code
					if partial && code == codes.NotFound {
						want = codes.Internal
					}
					checkCASReads(t, downstream, digest, want, nil)
					total, reserved, items, _ := c.Stats()
					if total != 0 || reserved != 0 || items != 0 {
						t.Errorf("failed reads left cache state: total=%d reserved=%d items=%d", total, reserved, items)
					}
					err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
						if err != nil {
							return err
						}
						if !entry.IsDir() {
							t.Errorf("failed read left file %s", path)
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

// The reader must retain buffered bytes when the following receive fails and
// must return a valid byte count, including on EOF with a nil message.
type statusTestStream struct {
	data []byte
	err  error
}

func (s *statusTestStream) Recv() (*bs.ReadResponse, error) {
	if s.data != nil {
		data := s.data
		s.data = nil
		return &bs.ReadResponse{Data: data}, nil
	}
	return nil, s.err
}
func (s *statusTestStream) CloseSend() error { return nil }
func TestStreamReadCloserTerminalStatus(t *testing.T) {
	for _, terminal := range []error{io.EOF, status.Error(codes.Unavailable, "failed"), status.Error(codes.NotFound, "missing")} {
		t.Run(terminal.Error(), func(t *testing.T) {
			r := &StreamReadCloser[*bs.ReadResponse]{Stream: &statusTestStream{data: []byte("abc"), err: terminal}}
			p := make([]byte, 1)
			if n, err := r.Read(p); n != 1 || err != nil || string(p) != "a" {
				t.Fatalf("first read: %d %v %q", n, err, p)
			}
			p = make([]byte, 8)
			n, err := r.Read(p)
			if n != 2 || string(p[:n]) != "bc" {
				t.Fatalf("terminal read: %d %v %q", n, err, p)
			}
			if terminal == io.EOF {
				if err != io.EOF {
					t.Fatalf("got %v, want EOF", err)
				}
			} else {
				want := status.Code(terminal)
				if want == codes.NotFound {
					want = codes.Internal
				}
				if status.Code(err) != want {
					t.Fatalf("got %v, want %s", err, want)
				}
			}
		})
	}
	r := &StreamReadCloser[*bs.ReadResponse]{Stream: &statusTestStream{err: status.Error(codes.NotFound, "missing")}}
	if n, err := r.Read(make([]byte, 8)); n != 0 || status.Code(err) != codes.NotFound {
		t.Fatalf("empty miss: %d %v", n, err)
	}
}
