package disk

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"testing/iotest"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk/casblob"
	"github.com/buchgr/bazel-remote/v2/cache/disk/zstdimpl"
	testutils "github.com/buchgr/bazel-remote/v2/utils"
)

// blobProxy implements cache.Proxy and answers every CAS request with the same body, claiming the
// size that was asked for, so that the size checks cannot catch a wrong body.
type blobProxy struct {
	body []byte
	// If not nil, reading fails with this after the body.
	err error
}

func (p blobProxy) Put(ctx context.Context, kind cache.EntryKind, hash string, logicalSize int64, sizeOnDisk int64, rc io.ReadCloser) {
	_ = rc.Close()
}

func (p blobProxy) Get(ctx context.Context, kind cache.EntryKind, hash string, size int64) (io.ReadCloser, int64, error) {
	if kind != cache.CAS {
		return nil, -1, nil
	}

	var r io.Reader = bytes.NewReader(p.body)
	if p.err != nil {
		r = io.MultiReader(r, iotest.ErrReader(p.err))
	}
	return io.NopCloser(r), size, nil
}

func (p blobProxy) Contains(ctx context.Context, kind cache.EntryKind, hash string, size int64) (bool, int64) {
	return kind == cache.CAS, size
}

// Returns data in the format that a disk cache with the given storage mode stores, and hence a
// proxy backend returns.
func encodeCasBlob(t *testing.T, mode string, data []byte) []byte {
	if mode == "uncompressed" {
		return data
	}

	zi, err := zstdimpl.Get("go")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(t.TempDir(), "blob")
	if err != nil {
		t.Fatal(err)
	}
	// WriteAndClose verifies what it is given, so tell it the actual digest.
	_, err = casblob.WriteAndClose(zi, bytes.NewReader(data), f, casblob.Zstandard,
		hashStr(string(data)), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

// Reads and closes rdr, and checks that it returned want.
func expectBlob(t *testing.T, rdr io.ReadCloser, want []byte) {
	if rdr == nil {
		t.Fatal("expected the blob to exist")
	}
	got, err := io.ReadAll(rdr)
	_ = rdr.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("expected %d bytes of blob data, got %d bytes that differ", len(want), len(got))
	}
}

// A CAS blob fetched from a proxy backend must only be handed to a client and kept in the cache if
// its contents match the digest it was fetched under, whichever way the cache stores blobs on disk.
func TestProxyCasVerification(t *testing.T) {
	ctx := context.Background()

	// Several chunks in compressed storage mode.
	size := int64(2*1024*1024 + 1000)
	data, hash := testutils.RandomDataAndHash(size)
	otherData, _ := testutils.RandomDataAndHash(size)
	errProxy := errors.New("proxy read error")

	// Decoders differ in how strictly they treat broken input, so test each in zstd mode.
	type config struct{ mode, zstdImpl string }
	configs := []config{{"uncompressed", "go"}}
	for _, impl := range zstdimpl.GetImplementations() {
		configs = append(configs, config{"zstd", impl})
	}

	for _, cfg := range configs {
		t.Run(cfg.mode+"/"+cfg.zstdImpl, func(t *testing.T) {
			body := encodeCasBlob(t, cfg.mode, data)

			type testCase struct {
				name  string
				proxy blobProxy
				// Expected outcome: error, cache miss, or the data.
				wantErr  bool
				wantData bool
			}
			tests := []testCase{
				{"valid", blobProxy{body: body}, false, true},
				{"wrong digest", blobProxy{body: encodeCasBlob(t, cfg.mode, otherData)}, false, false},
				{"read error", blobProxy{body: body[:len(body)/2], err: errProxy}, true, false},
			}
			if cfg.mode == "zstd" {
				corrupt := bytes.Clone(body)
				corrupt[len(corrupt)-100] ^= 0xff

				zi, err := zstdimpl.Get("go")
				if err != nil {
					t.Fatal(err)
				}
				// As returned by a gRPC backend other than bazel-remote.
				plain := zi.EncodeAll(data, nil)

				tests = append(tests,
					testCase{"corrupt zstd data", blobProxy{body: corrupt}, false, false},
					testCase{"plain zstd stream", blobProxy{body: plain}, false, true})
			}

			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					cacheDir := tempDir(t)
					defer func() { _ = os.RemoveAll(cacheDir) }()

					cacheI, err := New(cacheDir, 4*size,
						WithStorageMode(cfg.mode),
						WithZstdImplementation(cfg.zstdImpl),
						WithProxyBackend(tt.proxy),
						WithAccessLogger(testutils.NewSilentLogger()))
					if err != nil {
						t.Fatal(err)
					}
					testCache := cacheI.(*diskCache)

					rdr, _, err := testCache.Get(ctx, cache.CAS, hash, size, 0)
					if tt.wantErr != (err != nil) {
						t.Fatalf("expected error: %v, got %v", tt.wantErr, err)
					}
					if !tt.wantData {
						if rdr != nil {
							_ = rdr.Close()
							t.Fatal("the cache returned a blob that does not match its digest")
						}
						if testCache.lru.Len() != 0 {
							t.Fatalf("expected the cache to be empty, found %d items", testCache.lru.Len())
						}
						return
					}

					expectBlob(t, rdr, data)
					if testCache.lru.Len() != 1 {
						t.Fatalf("expected one item in the cache, found %d items", testCache.lru.Len())
					}

					// Read from the middle of the second chunk of the now cached blob, which needs a
					// usable chunk table in compressed storage mode.
					offset := int64(1024*1024 + 1000)
					rdr, _, err = testCache.Get(ctx, cache.CAS, hash, size, offset)
					if err != nil {
						t.Fatal(err)
					}
					expectBlob(t, rdr, data[offset:])
				})
			}
		})
	}
}
