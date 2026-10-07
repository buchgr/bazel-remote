package disk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk/casblob"
	"github.com/buchgr/bazel-remote/v2/cache/disk/zstdimpl"
	testutils "github.com/buchgr/bazel-remote/v2/utils"
)

// lyingProxy implements cache.Proxy and answers every CAS request with the same blob, whose
// contents never hash to the digest that was asked for.
type lyingProxy struct {
	// What the disk cache stores CAS blobs as, which is what a proxy backend hands back.
	storageMode casblob.CompressionType
}

const lie = "goodbye"

func hashOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func (p lyingProxy) Put(ctx context.Context, kind cache.EntryKind, hash string, logicalSize int64, sizeOnDisk int64, rc io.ReadCloser) {
	_ = rc.Close()
}

func (p lyingProxy) Get(ctx context.Context, kind cache.EntryKind, hash string, _ int64) (io.ReadCloser, int64, error) {
	if kind != cache.CAS {
		return nil, -1, nil
	}

	// The caller is told the size it asked for, so the existing size checks cannot catch this.
	if p.storageMode == casblob.Identity {
		return io.NopCloser(strings.NewReader(lie[:contentsLength])), contentsLength, nil
	}

	tmpfile, err := os.CreateTemp("", "lyingProxyGet")
	if err != nil {
		return nil, -1, err
	}
	name := tmpfile.Name()
	defer func() { _ = os.Remove(name) }()

	zi, err := zstdimpl.Get("go")
	if err != nil {
		return nil, -1, err
	}

	// WriteAndClose verifies what it is given, so it has to be told the digest of the lie.
	_, err = casblob.WriteAndClose(zi, strings.NewReader(lie[:contentsLength]), tmpfile,
		casblob.Zstandard, hashOf(lie[:contentsLength]), contentsLength)
	if err != nil {
		return nil, -1, err
	}

	readme, err := os.Open(name)
	if err != nil {
		return nil, -1, err
	}

	return readme, contentsLength, nil
}

func (p lyingProxy) Contains(ctx context.Context, kind cache.EntryKind, hash string, _ int64) (bool, int64) {
	if kind != cache.CAS {
		return false, -1
	}
	return true, contentsLength
}

// A CAS blob whose contents do not hash to the digest they were fetched under must not be handed to
// a client, nor left in the cache, whichever way the cache stores blobs on disk.
func TestProxyReturnsWrongCasBlob(t *testing.T) {
	for _, mode := range []string{"uncompressed", "zstd"} {
		t.Run(mode, func(t *testing.T) {
			compression := casblob.Identity
			if mode == "zstd" {
				compression = casblob.Zstandard
			}

			cacheDir := tempDir(t)
			defer func() { _ = os.RemoveAll(cacheDir) }()

			cacheI, err := New(cacheDir, BlockSize,
				WithStorageMode(mode),
				WithProxyBackend(lyingProxy{storageMode: compression}),
				WithAccessLogger(testutils.NewSilentLogger()))
			if err != nil {
				t.Fatal(err)
			}
			testCache := cacheI.(*diskCache)

			rdr, _, err := testCache.Get(context.Background(), cache.CAS, contentsHash, contentsLength, 0)
			if err != nil {
				t.Fatalf("expected a cache miss, got an error: %v", err)
			}
			if rdr != nil {
				data, _ := io.ReadAll(rdr)
				_ = rdr.Close()
				t.Fatalf("the cache returned a blob that does not match its digest: %q", data)
			}

			if testCache.lru.Len() != 0 {
				t.Fatalf("expected the cache to be empty, found %d items", testCache.lru.Len())
			}
		})
	}
}
