package disk

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchgr/bazel-remote/v2/cache"
	"github.com/buchgr/bazel-remote/v2/cache/disk/casblob"
	"github.com/buchgr/bazel-remote/v2/cache/disk/zstdimpl"
	"github.com/buchgr/bazel-remote/v2/cache/hashing"
	"github.com/buchgr/bazel-remote/v2/cache/httpproxy"
	testutils "github.com/buchgr/bazel-remote/v2/utils"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
	"google.golang.org/protobuf/proto"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func tempDir(t *testing.T) string {
	dir, err := os.MkdirTemp("", "bazel-remote")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

const KEY = "a-key"
const contents = "hello"
const contentsLength = int64(len(contents))

var contentsHash = hashing.DefaultHasher.Hash([]byte(contents))

func TestCacheBasics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)

	itemSize := int64(256)

	// Add some overhead for likely CAS blob storage expansion.
	cacheSize := int64(itemSize*2 + BlockSize)

	testCacheI, err := New(cacheDir, cacheSize, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	testCache := testCacheI.(*diskCache)

	if testCache.lru.Len() != 0 {
		t.Fatalf("Expected to start with an empty disk cache, found %d items",
			testCache.lru.Len())
	}

	data, hash := testutils.RandomDataAndHash(itemSize, hashing.DefaultHasher)

	// Non-existing item.
	rdr, _, err := testCache.Get(ctx, cache.CAS, hashing.DefaultHasher, hash, itemSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rdr != nil {
		t.Fatal("expected the item not to exist")
	}

	// Add an item.
	err = testCache.Put(ctx, cache.CAS, hashing.DefaultHasher, hash, itemSize,
		io.NopCloser(bytes.NewReader(data)))
	if err != nil {
		t.Fatal(err)
	}

	// Get the item back.
	rdr, sizeBytes, err := testCache.Get(ctx, cache.CAS, hashing.DefaultHasher, hash, itemSize, 0)
	if err != nil {
		t.Fatal(err)
	}

	err = expectContentEquals(rdr, sizeBytes, data)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCachePutWrongSize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache, err := New(cacheDir, BlockSize, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	content := "hello"
	hash := hashStr(content)

	for _, kind := range []cache.EntryKind{cache.AC, cache.CAS, cache.RAW} {
		err = testCache.Put(ctx, kind, hashing.DefaultHasher, hash, int64(len(content)), strings.NewReader(content))
		if err != nil {
			t.Fatal("Expected success", err)
		}

		err = testCache.Put(ctx, kind, hashing.DefaultHasher, hash, int64(len(content))+1, strings.NewReader(content))
		if err == nil {
			t.Error("Expected error due to size being different")
		}

		err = testCache.Put(ctx, kind, hashing.DefaultHasher, hash, int64(len(content))-1, strings.NewReader(content))
		if err == nil {
			t.Error("Expected error due to size being different")
		}
		err = testCache.Put(ctx, kind, hashing.DefaultHasher, hashStr(content[:len(content)-1]), int64(len(content))-1, strings.NewReader(content))
		if err == nil {
			t.Error("Expected error due to size being different")
		}
	}
}

func TestCacheGetContainsWrongSize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache, err := New(cacheDir, BlockSize, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	var rdr io.ReadCloser

	err = testCache.Put(ctx, cache.CAS, hashing.DefaultHasher, contentsHash, contentsLength, strings.NewReader(contents))
	if err != nil {
		t.Fatal("Expected success", err)
	}

	found, _ = testCache.Contains(ctx, cache.CAS, hashing.DefaultHasher, contentsHash, contentsLength+1)
	if found {
		t.Error("Expected not found, due to size being different")
	}

	rdr, _, _ = testCache.Get(ctx, cache.CAS, hashing.DefaultHasher, contentsHash, contentsLength+1, 0)
	if rdr != nil {
		t.Error("Expected not found, due to size being different")
	}

	found, _ = testCache.Contains(ctx, cache.CAS, hashing.DefaultHasher, contentsHash, -1)
	if !found {
		t.Error("Expected found, when unknown size")
	}

	rdr, _, _ = testCache.Get(ctx, cache.CAS, hashing.DefaultHasher, contentsHash, -1, 0)
	if rdr == nil {
		t.Error("Expected found, when unknown size")
	}
}

func TestCacheGetContainsWrongSizeWithProxy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCacheI, err := New(cacheDir, BlockSize, WithProxyBackend(new(proxyStub)), WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	testCache := testCacheI.(*diskCache)

	var found bool
	var rdr io.ReadCloser

	// The proxyStub contains the digest {contentsHash, contentsLength}.

	if testCache.lru.Len() != 0 {
		t.Fatalf("Expected to start with an empty disk cache, found %d items",
			testCache.lru.Len())
	}

	found, _ = testCache.Contains(ctx, cache.CAS, hashing.DefaultHasher, contentsHash, contentsLength+1)
	if found {
		t.Fatal("Expected not found, due to size being different")
	}

	rdr, _, _ = testCache.Get(ctx, cache.CAS, hashing.DefaultHasher, contentsHash, contentsLength+1, 0)
	if rdr != nil {
		t.Fatal("Expected not found, due to size being different")
	}

	if testCache.lru.Len() != 0 {
		t.Fatalf("Expected cache to be empty at this point, found %d items",
			testCache.lru.Len())
	}

	found, _ = testCache.Contains(ctx, cache.CAS, hashing.DefaultHasher, contentsHash, -1)
	if !found {
		t.Fatal("Expected found, when unknown size")
	}

	rdr, _, _ = testCache.Get(ctx, cache.CAS, hashing.DefaultHasher, contentsHash, -1, 0)
	if rdr == nil {
		t.Fatal("Expected found, when unknown size")
	}

	if testCache.lru.Len() != 1 {
		t.Fatalf("Expected one item to be in the cache at this point, found %d items",
			testCache.lru.Len())
	}
}

// proxyStub implements the cache.Proxy interface for a single blob with
// digest {contentsHash, contentsLength}.
type proxyStub struct{}

func (d proxyStub) Put(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, logicalSize int64, sizeOnDisk int64, rc io.ReadCloser) {
	// Not implemented.
}

func (d proxyStub) Get(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, _ int64) (io.ReadCloser, int64, error) {
	if hash != contentsHash || kind != cache.CAS {
		return nil, -1, nil
	}

	tmpfile, err := os.CreateTemp("", "proxyStubGet")
	if err != nil {
		return nil, -1, err
	}
	tfn := tmpfile.Name()
	defer os.Remove(tfn)

	var zi zstdimpl.ZstdImpl
	zi, err = zstdimpl.Get("go")
	if err != nil {
		return nil, -1, err
	}

	_, err = casblob.WriteAndClose(
		zi,
		io.NopCloser(
			strings.NewReader(contents)), tmpfile, casblob.Zstandard,
		hasher, hash, contentsLength)
	if err != nil {
		return nil, -1, err
	}

	readme, err := os.Open(tfn)
	if err != nil {
		return nil, -1, err
	}

	return readme, contentsLength, nil
}

func (d proxyStub) Contains(ctx context.Context, kind cache.EntryKind, hasher hashing.Hasher, hash string, _ int64) (bool, int64) {
	if hash != contentsHash || kind != cache.CAS {
		return false, -1
	}

	return true, contentsLength
}

func expectContentEquals(rdr io.ReadCloser, sizeBytes int64, expectedContent []byte) error {
	if rdr == nil {
		return fmt.Errorf("expected the item to exist")
	}
	data, err := io.ReadAll(rdr)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, expectedContent) {
		return fmt.Errorf("expected response '%s', but received '%s'",
			expectedContent, data)
	}
	if int64(len(data)) != sizeBytes {
		return fmt.Errorf("Expected sizeBytes to be '%d' but was '%d'",
			sizeBytes, len(data))
	}

	return nil
}

func putGetCompare(ctx context.Context, kind cache.EntryKind, hash string, content string, testCache *diskCache) error {
	return putGetCompareBytes(ctx, kind, hash, []byte(content), testCache)
}

func putGetCompareBytes(ctx context.Context, kind cache.EntryKind, hash string, data []byte, testCache *diskCache) error {

	r := bytes.NewReader(data)

	err := testCache.Put(ctx, kind, hashing.DefaultHasher, hash, int64(len(data)), r)
	if err != nil {
		return err
	}

	rdr, sizeBytes, err := testCache.Get(ctx, kind, hashing.DefaultHasher, hash, int64(len(data)), 0)
	if err != nil {
		return err
	}

	// Get the item back
	return expectContentEquals(rdr, sizeBytes, data)
}

func hashStr(content string) string {
	return hashing.DefaultHasher.Hash([]byte(content))
}

// Make sure that we can overwrite items if we upload the same key again.
func TestOverwrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)

	testCacheI, err := New(cacheDir, BlockSize, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	testCache := testCacheI.(*diskCache)

	err = putGetCompare(ctx, cache.CAS, hashStr("hello"), "hello", testCache)
	if err != nil {
		t.Fatal(err)
	}
	err = putGetCompare(ctx, cache.CAS, hashStr("hello"), "hello", testCache)
	if err != nil {
		t.Fatal(err)
	}

	err = putGetCompare(ctx, cache.AC, hashStr("world"), "world1", testCache)
	if err != nil {
		t.Fatal(err)
	}
	err = putGetCompare(ctx, cache.AC, hashStr("world"), "world2", testCache)
	if err != nil {
		t.Fatal(err)
	}

	err = putGetCompare(ctx, cache.RAW, hashStr("world"), "world3", testCache)
	if err != nil {
		t.Fatal(err)
	}
	err = putGetCompare(ctx, cache.RAW, hashStr("world"), "world4", testCache)
	if err != nil {
		t.Fatal(err)
	}
}

func ensureDirExists(path string, t *testing.T) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		err = os.MkdirAll(path, os.ModePerm)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCacheExistingFiles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)

	items := []struct {
		df       pb.DigestFunction_Value
		contents string
		hash     string
		key      string
		file     string
	}{
		{
			pb.DigestFunction_SHA256,
			"hej",
			"9c478bf63e9500cb5db1e85ece82f18c8eb9e52e2f9135acd7f10972c8d563ba",
			"cas/9c478bf63e9500cb5db1e85ece82f18c8eb9e52e2f9135acd7f10972c8d563ba",
			"cas.v2/9c/9c478bf63e9500cb5db1e85ece82f18c8eb9e52e2f9135acd7f10972c8d563ba-3-123456789",
		},
		{
			pb.DigestFunction_SHA256,
			"världen",
			"d497feaa39156f4ae61317db9d2adc3a8f2ff1437fd48ccb56f814f0b7ac5fe1",
			"cas/d497feaa39156f4ae61317db9d2adc3a8f2ff1437fd48ccb56f814f0b7ac5fe1",
			"cas.v2/d4/d497feaa39156f4ae61317db9d2adc3a8f2ff1437fd48ccb56f814f0b7ac5fe1-8-123456789",
		},
		{
			pb.DigestFunction_SHA256,
			"foo",
			"733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
			"ac/733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
			"ac.v2/73/733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54-123456789",
		},
		{
			pb.DigestFunction_SHA256,
			"bar",
			"733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
			"raw/733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
			"raw.v2/73/733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54-123456789",
		},
	}

	var err error
	for _, it := range items {
		hasher, err := hashing.Get(it.df)
		if err != nil {
			t.Fatal(err)
		}

		fp := path.Join(cacheDir, it.file)
		ensureDirExists(path.Dir(fp), t)

		if strings.HasPrefix(it.file, "cas.v2/") {
			r := bytes.NewReader([]byte(it.contents))
			var f *os.File
			f, err = os.Create(fp)
			if err == nil {
				var zi zstdimpl.ZstdImpl
				zi, err = zstdimpl.Get("go")
				if err != nil {
					t.Fatal(err)
				}
				_, err = casblob.WriteAndClose(zi, r, f, casblob.Zstandard,
					hasher, it.hash, int64(len(it.contents)))
			}
		} else {
			err = os.WriteFile(fp, []byte(it.contents), os.ModePerm)
		}
		if err != nil {
			t.Fatal(err)
		}

		// Wait a bit to account for atime granularity
		time.Sleep(10 * time.Millisecond)
	}

	// Add some overhead for likely CAS blob storage expansion.
	const cacheSize = BlockSize * 10

	testCacheI, err := New(cacheDir, cacheSize, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	testCache := testCacheI.(*diskCache)

	evicted := []Key{}
	origOnEvict := testCache.lru.onEvict
	testCache.lru.onEvict = func(key Key, value lruItem) {
		evicted = append(evicted, key.(string))
		origOnEvict(key, value)
	}

	if testCache.lru.Len() != 4 {
		t.Fatal("expected four items in the cache, found", testCache.lru.Len())
	}

	// Adding new blobs should eventually evict the oldest (items[0]).
	for i := 0; i < 100; i++ {
		data, hash := testutils.RandomDataAndHash(32, hashing.DefaultHasher)

		if items[0].hash == hash {
			// Add any item but this one, to ensure it will be evicted first.
			continue
		}

		err = testCache.Put(ctx, cache.CAS, hashing.DefaultHasher, hash, int64(len(data)),
			bytes.NewReader(data))
		if err != nil {
			t.Fatal("failed to Put CAS blob", hash, err)
		}

		if len(evicted) == 0 {
			// Nothing evicted yet.
			continue
		}

		if evicted[0] != items[0].key {
			t.Fatalf("Expected first evicted item to be %s, was %s",
				items[0].key, evicted[0])
		}

		break // First item evicted as expected.
	}

	found, _ := testCache.Contains(ctx, cache.CAS, hashing.DefaultHasher, items[0].hash, contentsLength)
	if found {
		t.Fatalf("%s should have been evicted", items[0].file)
	}
}

// Make sure that the cache returns http.StatusInsufficientStorage when trying to upload an item
// that's bigger than the maximum size.
func TestCacheBlobTooLarge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCacheI, err := New(cacheDir, BlockSize, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	testCache := testCacheI.(*diskCache)

	for k := range []cache.EntryKind{cache.AC, cache.RAW} {
		kind := cache.EntryKind(k)
		err := testCache.Put(ctx, kind, hashing.DefaultHasher, hashStr("foo"), 10000, strings.NewReader(contents))
		if err == nil {
			t.Fatal("Expected an error")
		}

		if cerr, ok := err.(*cache.Error); ok {
			if cerr.Code != http.StatusInsufficientStorage {
				t.Fatalf("Expected error code %d but received %d", http.StatusInsufficientStorage, cerr.Code)
			}
		} else {
			t.Fatal("Expected error to be of type Error")
		}
	}
}

// Make sure that Cache rejects an upload whose hashsum doesn't match
func TestCacheCorruptedCASBlob(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCacheI, err := New(cacheDir, BlockSize, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	testCache := testCacheI.(*diskCache)

	err = testCache.Put(ctx, cache.CAS, hashing.DefaultHasher, hashStr("foo"), int64(len(contents)),
		strings.NewReader(contents))
	if err == nil {
		t.Fatal("expected hash mismatch error")
	}

	// We expect the upload to succeed without validation:
	err = testCache.Put(ctx, cache.RAW, hashing.DefaultHasher, hashStr("foo"), int64(len(contents)),
		strings.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
}

// Create a random file of a certain size in the given directory, and
// return its hash.
func createRandomFile(dir string, size int64) (string, error) {
	data, hash := testutils.RandomDataAndHash(size, hashing.LegacyHasher)
	err := os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		return "", err
	}
	filepath := dir + "/" + hash

	return hash, os.WriteFile(filepath, data, os.ModePerm)
}

func createRandomV1CASFile(dir string, size int64) (string, error) {
	data, hash := testutils.RandomDataAndHash(size, hashing.LegacyHasher)
	err := os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		return "", err
	}
	filePath := dir + "/" + hash

	err = os.WriteFile(filePath, data, os.ModePerm)
	if err != nil {
		return "", err
	}

	return hash, nil
}

func TestMigrateFromOldDirectoryStructure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	acHash, err := createRandomFile(cacheDir+"/ac", 512)
	if err != nil {
		t.Fatal(err)
	}

	casHash1, err := createRandomV1CASFile(cacheDir+"/cas", 1024)
	if err != nil {
		t.Fatal(err)
	}

	casHash2, err := createRandomV1CASFile(cacheDir+"/cas", 1024)
	if err != nil {
		t.Fatal(err)
	}

	// Add some overhead for likely CAS blob storage expansion.
	const cacheSize = 2560*2 + BlockSize*2

	testCacheI, err := New(cacheDir, cacheSize, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	testCache := testCacheI.(*diskCache)

	_, _, numItems, _ := testCache.Stats()
	if numItems != 3 {
		t.Fatalf("Expected test cache size 3 but was %d", numItems)
	}

	var found bool
	found, _ = testCache.Contains(ctx, cache.AC, hashing.LegacyHasher, acHash, 512)
	if !found {
		t.Fatalf("Expected cache to contain AC entry '%s'", acHash)
	}

	found, _ = testCache.Contains(ctx, cache.CAS, hashing.LegacyHasher, casHash1, 1024)
	if !found {
		t.Fatalf("Expected cache to contain CAS entry '%s'", casHash1)
	}

	found, _ = testCache.Contains(ctx, cache.CAS, hashing.LegacyHasher, casHash2, 1024)
	if !found {
		t.Fatalf("Expected cache to contain CAS entry '%s'", casHash2)
	}
}

func TestLoadExistingEntries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test that loading existing items works
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	numBlobs := int64(8)
	blobSize := int64(1024)

	var err error

	// V0 AC entry.
	acData, acHash := testutils.RandomDataAndHash(blobSize, hashing.LegacyHasher)
	err = os.MkdirAll(path.Join(cacheDir, "ac"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(path.Join(cacheDir, "ac", acHash), acData, 0644)
	if err != nil {
		t.Fatal(err)
	}

	// V0 CAS entry.
	casData, casHash := testutils.RandomDataAndHash(blobSize, hashing.LegacyHasher)
	err = os.MkdirAll(path.Join(cacheDir, "cas"), 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(path.Join(cacheDir, "cas", casHash), casData, 0644)
	if err != nil {
		t.Fatal(err)
	}

	// V1 CAS entry.
	casV1Data, casV1Hash := testutils.RandomDataAndHash(blobSize, hashing.LegacyHasher)
	err = os.MkdirAll(path.Join(cacheDir, "cas", casV1Hash[:2]), 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(path.Join(cacheDir, "cas", casV1Hash[:2], casV1Hash), casV1Data, 0644)
	if err != nil {
		t.Fatal(err)
	}

	// V1 AC entry.
	acV1Data, acV1Hash := testutils.RandomDataAndHash(blobSize, hashing.LegacyHasher)
	err = os.MkdirAll(path.Join(cacheDir, "ac", acV1Hash[:2]), 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(path.Join(cacheDir, "ac", acV1Hash[:2], acV1Hash), acV1Data, 0644)
	if err != nil {
		t.Fatal(err)
	}

	// V1 RAW entry.
	rawV1Data, rawV1Hash := testutils.RandomDataAndHash(blobSize, hashing.LegacyHasher)
	err = os.MkdirAll(path.Join(cacheDir, "raw", rawV1Hash[:2]), 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(path.Join(cacheDir, "raw", rawV1Hash[:2], rawV1Hash), rawV1Data, 0644)
	if err != nil {
		t.Fatal(err)
	}

	// V2 CAS entry.
	casV2Data, casV2Hash := testutils.RandomDataAndHash(blobSize, hashing.DefaultHasher)
	err = os.MkdirAll(path.Join(cacheDir, "cas.v2", hashing.DefaultHasher.Dir(), casV2Hash[:2]), 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(path.Join(cacheDir, "cas.v2", hashing.DefaultHasher.Dir(), casV2Hash[:2], casV2Hash+"-271174706"), casV2Data, 0644)
	if err != nil {
		t.Fatal(err)
	}

	// V2 AC entry.
	acV2Data, acV2Hash := testutils.RandomDataAndHash(blobSize, hashing.DefaultHasher)
	err = os.MkdirAll(path.Join(cacheDir, "ac.v2", hashing.DefaultHasher.Dir(), acV2Hash[:2]), 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(path.Join(cacheDir, "ac.v2", hashing.DefaultHasher.Dir(), acV2Hash[:2], acV2Hash+"-271174706"), acV2Data, 0644)
	if err != nil {
		t.Fatal(err)
	}

	// V2 RAW entry.
	rawV2Data, rawV2Hash := testutils.RandomDataAndHash(blobSize, hashing.DefaultHasher)
	err = os.MkdirAll(path.Join(cacheDir, "raw.v2", hashing.DefaultHasher.Dir(), rawV2Hash[:2]), 0755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(path.Join(cacheDir, "raw.v2", hashing.DefaultHasher.Dir(), rawV2Hash[:2], rawV2Hash+"-271174706"), rawV2Data, 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Create some .DS_Store files which should be ignored or deleted.
	err = os.WriteFile(path.Join(cacheDir, "raw.v2", hashing.DefaultHasher.Dir(), rawV2Hash[:2], ".DS_Store"), []byte{1, 2, 3}, 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(path.Join(cacheDir, "raw.v2", hashing.DefaultHasher.Dir(), ".DS_Store"), []byte{}, 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(path.Join(cacheDir, "raw.v2", ".DS_Store"), []byte{4, 5, 6}, 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(path.Join(cacheDir, ".DS_Store"), []byte{4, 5, 6}, 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Add some overhead for likely CAS blob storage expansion.
	cacheSize := int64((blobSize + BlockSize) * numBlobs * 2)

	testCacheI, err := New(cacheDir, cacheSize, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	testCache := testCacheI.(*diskCache)

	_, _, numItems, _ := testCache.Stats()
	if int64(numItems) != numBlobs {
		t.Fatalf("Expected test cache size %d but was %d",
			numBlobs, numItems)
	}

	var found bool

	found, _ = testCache.Contains(ctx, cache.AC, hashing.LegacyHasher, acHash, blobSize)
	if !found {
		t.Fatalf("Expected cache to contain AC entry '%s'", acHash)
	}

	found, _ = testCache.Contains(ctx, cache.CAS, hashing.LegacyHasher, casHash, blobSize)
	if !found {
		t.Fatalf("Expected cache to contain CAS entry '%s'", casHash)
	}

	found, _ = testCache.Contains(ctx, cache.CAS, hashing.LegacyHasher, casV1Hash, blobSize)
	if !found {
		t.Fatalf("Expected cache to contain CAS V1 entry '%s'", casV1Hash)
	}

	found, _ = testCache.Contains(ctx, cache.RAW, hashing.LegacyHasher, rawV1Hash, blobSize)
	if !found {
		t.Fatalf("Expected cache to contain RAW entry '%s'", rawV1Hash)
	}

	found, _ = testCache.Contains(ctx, cache.AC, hashing.DefaultHasher, acV2Hash, blobSize)
	if !found {
		t.Fatalf("Expected cache to contain AC entry '%s'", acV2Hash)
	}

	found, _ = testCache.Contains(ctx, cache.CAS, hashing.DefaultHasher, casV2Hash, blobSize)
	if !found {
		t.Fatalf("Expected cache to contain CAS entry '%s'", casV2Hash)
	}

	found, _ = testCache.Contains(ctx, cache.RAW, hashing.DefaultHasher, rawV2Hash, blobSize)
	if !found {
		t.Fatalf("Expected cache to contain RAW entry '%s'", rawV2Hash)
	}
}

func TestDistinctKeyspaces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	blobSize := 1024

	// Add some overhead for likely CAS blob storage expansion.
	cacheSize := int64((blobSize+BlockSize)*3) * 2

	testCacheI, err := New(cacheDir, cacheSize, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	testCache := testCacheI.(*diskCache)

	blob, casHash := testutils.RandomDataAndHash(1024, hashing.DefaultHasher)

	// Add the same blob with the same key, to each of the three
	// keyspaces, and verify that we have exactly three items in
	// the cache.

	err = putGetCompareBytes(ctx, cache.CAS, casHash, blob, testCache)
	if err != nil {
		t.Fatal(err)
	}

	err = putGetCompareBytes(ctx, cache.AC, casHash, blob, testCache)
	if err != nil {
		t.Fatal(err)
	}

	err = putGetCompareBytes(ctx, cache.RAW, casHash, blob, testCache)
	if err != nil {
		t.Fatal(err)
	}

	_, _, numItems, _ := testCache.Stats()
	if numItems != 3 {
		t.Fatalf("Expected test cache size 3 but was %d",
			numItems)
	}
}

// Code copied from cache/http/http_test.go
type testServer struct {
	srv *httptest.Server

	mu  sync.Mutex
	ac  map[string][]byte
	cas map[string][]byte
}

func (s *testServer) handler(w http.ResponseWriter, r *http.Request) {

	fields := strings.Split(r.URL.Path, "/")

	kindMap := s.ac
	if fields[1] == "ac" {
		kindMap = s.ac
	} else if fields[1] == "cas" {
		kindMap = s.cas
	}
	hash := fields[2]

	s.mu.Lock()
	defer s.mu.Unlock()

	switch method := r.Method; method {
	case http.MethodGet:
		data, ok := kindMap[hash]
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
		return

	case http.MethodPut:
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusInternalServerError)
			return
		}
		kindMap[hash] = data
		return

	case http.MethodHead:
		_, ok := kindMap[hash]
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		return
	}
}

func (s *testServer) numItems() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ac) + len(s.cas)
}

func newTestServer(t *testing.T) *testServer {
	ts := testServer{
		ac:  make(map[string][]byte),
		cas: make(map[string][]byte),
	}
	ts.srv = httptest.NewServer(http.HandlerFunc(ts.handler))

	return &ts
}

func TestHttpProxyBackend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	backend := newTestServer(t)
	url, err := url.Parse(backend.srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	accessLogger := testutils.NewSilentLogger()
	errorLogger := testutils.NewSilentLogger()

	proxy, err := httpproxy.New(url, "zstd", &http.Client{}, accessLogger, errorLogger, 100, 1000000)
	if err != nil {
		t.Fatal(err)
	}

	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	// Add some overhead for likely CAS blob storage expansion.
	cacheSize := int64(1024*10) * 2

	testCacheI, err := New(cacheDir, cacheSize, WithProxyBackend(proxy), WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	testCache := testCacheI.(*diskCache)

	blobSize := int64(1024)
	blob, casHash := testutils.RandomDataAndHash(blobSize, hashing.DefaultHasher)

	// Non-existing item
	r, _, err := testCache.Get(ctx, cache.CAS, hashing.DefaultHasher, casHash, blobSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r != nil {
		t.Fatal("Expected nil reader")
	}

	if backend.numItems() != 0 {
		t.Fatal("Expected empty backend")
	}

	err = testCache.Put(ctx, cache.CAS, hashing.DefaultHasher, casHash, int64(len(blob)),
		bytes.NewReader(blob))
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(time.Second) // Proxying to the backend is async.

	if backend.numItems() != 1 {
		// If this fails, check the time.Sleep call above...
		t.Fatal("Expected Put to be proxied to the backend",
			backend.numItems())
	}

	// Create a new (empty) testCache, without a proxy backend.
	cacheDir = testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	testCacheI, err = New(cacheDir, cacheSize, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	testCache = testCacheI.(*diskCache)

	// Confirm that it does not contain the item we added to the
	// first testCache and the proxy backend.

	found, _ := testCache.Contains(ctx, cache.CAS, hashing.DefaultHasher, casHash, blobSize)
	if found {
		t.Fatalf("Expected the cache not to contain %s", casHash)
	}

	r, _, err = testCache.Get(ctx, cache.CAS, hashing.DefaultHasher, casHash, blobSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r != nil {
		t.Fatal("Expected testCache to be empty")
	}

	// Add the proxy backend
	testCache.proxy = proxy
	testCache.maxProxyBlobSize = blobSize - 1
	found, _ = testCache.Contains(ctx, cache.CAS, hashing.DefaultHasher, casHash, blobSize)
	if found {
		t.Fatalf("Expected the cache to not contain %s (via the proxy)", casHash)
	}

	r, _, err = testCache.Get(ctx, cache.CAS, hashing.DefaultHasher, casHash, blobSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r != nil {
		t.Fatal("Expected the Get to fail")
	}

	// Set a larger max proxy blob size and check that we can Get the item.
	testCache.maxProxyBlobSize = math.MaxInt64

	found, _ = testCache.Contains(ctx, cache.CAS, hashing.DefaultHasher, casHash, blobSize)
	if !found {
		t.Fatalf("Expected the cache to contain %s (via the proxy)",
			casHash)
	}

	r, fetchedSize, err := testCache.Get(ctx, cache.CAS, hashing.DefaultHasher, casHash, blobSize, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("Expected the Get to succeed")
	}
	if fetchedSize != blobSize {
		t.Fatalf("Expected a blob of size %d, got %d", blobSize, fetchedSize)
	}

	retrievedData, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if int64(len(retrievedData)) != blobSize {
		t.Fatalf("Expected '%d' bytes of data, but received '%d'",
			blobSize, len(retrievedData))
	}

	if !bytes.Equal(retrievedData, blob) {
		t.Fatalf("Expected '%v' but received '%v", retrievedData, blob)
	}
}

// Store an ActionResult with an output directory, then confirm that
// GetValidatedActionResult returns the original item.
func TestGetValidatedActionResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	testCacheI, err := New(cacheDir, 1024*32, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	testCache := testCacheI.(*diskCache)

	// Create a directory tree like so:
	// /bar/foo.txt
	// /bar/grok.txt

	grokData := []byte("grok test data")
	grokHashStr := hashing.DefaultHasher.Hash(grokData)

	err = testCache.Put(ctx, cache.CAS, hashing.DefaultHasher, grokHashStr, int64(len(grokData)),
		bytes.NewReader(grokData))
	if err != nil {
		t.Fatal(err)
	}

	fooData := []byte("foo test data")
	fooHashStr := hashing.DefaultHasher.Hash(fooData)

	err = testCache.Put(ctx, cache.CAS, hashing.DefaultHasher, fooHashStr, int64(len(fooData)),
		bytes.NewReader(fooData))
	if err != nil {
		t.Fatal(err)
	}

	barDir := pb.Directory{
		Files: []*pb.FileNode{
			{
				Name: "foo.txt",
				Digest: &pb.Digest{
					Hash:      fooHashStr,
					SizeBytes: int64(len(fooData)),
				},
			},
			{
				Name: "grok.txt",
				Digest: &pb.Digest{
					Hash:      grokHashStr,
					SizeBytes: int64(len(grokData)),
				},
			},
		},
	}

	barData, err := proto.Marshal(&barDir)
	if err != nil {
		t.Fatal(err)
	}
	barDataHashStr := hashing.DefaultHasher.Hash(barData)

	err = testCache.Put(ctx, cache.CAS, hashing.DefaultHasher, barDataHashStr, int64(len(barData)),
		bytes.NewReader(barData))
	if err != nil {
		t.Fatal(err)
	}

	rootDir := pb.Directory{
		Directories: []*pb.DirectoryNode{
			{
				Name: "bar",
				Digest: &pb.Digest{
					Hash:      barDataHashStr,
					SizeBytes: int64(len(barData)),
				},
			},
		},
	}

	rootData, err := proto.Marshal(&rootDir)
	if err != nil {
		t.Fatal(err)
	}
	rootDataHashStr := hashing.DefaultHasher.Hash(rootData)

	err = testCache.Put(ctx, cache.CAS, hashing.DefaultHasher, rootDataHashStr, int64(len(rootData)),
		bytes.NewReader(rootData))
	if err != nil {
		t.Fatal(err)
	}

	tree := pb.Tree{
		Root:     &rootDir,
		Children: []*pb.Directory{&barDir},
	}
	treeData, err := proto.Marshal(&tree)
	if err != nil {
		t.Fatal(err)
	}
	treeDataHashStr := hashing.DefaultHasher.Hash(treeData)

	err = testCache.Put(ctx, cache.CAS, hashing.DefaultHasher, treeDataHashStr, int64(len(treeData)),
		bytes.NewReader(treeData))
	if err != nil {
		t.Fatal(err)
	}

	// Now add an ActionResult that refers to this tree.

	ar := pb.ActionResult{
		OutputFiles: []*pb.OutputFile{
			{
				Path: "bar/grok.txt",
				Digest: &pb.Digest{
					Hash:      grokHashStr,
					SizeBytes: int64(len(grokData)),
				},
			},
			{
				Path: "foo.txt",
				Digest: &pb.Digest{
					Hash:      fooHashStr,
					SizeBytes: int64(len(fooData)),
				},
			},
		},
		OutputDirectories: []*pb.OutputDirectory{
			{
				Path: "",
				TreeDigest: &pb.Digest{
					Hash:      treeDataHashStr,
					SizeBytes: int64(len(treeData)),
				},
			},
		},
	}
	arData, err := proto.Marshal(&ar)
	if err != nil {
		t.Fatal(err)
	}
	arDataHashStr := hashing.DefaultHasher.Hash([]byte("pretend action"))

	err = testCache.Put(ctx, cache.AC, hashing.DefaultHasher, arDataHashStr, int64(len(arData)),
		bytes.NewReader(arData))
	if err != nil {
		t.Fatal(err)
	}

	// Finally, check that the validated+returned data is correct.
	//
	// Note: we (sometimes) add metadata in the gRPC/HTTP layer, which
	// would then return a different ActionResult message. But it's safe
	// to assume that the value should be returned unchanged by the cache
	// layer.

	rAR, rData, err := testCache.GetValidatedActionResult(ctx, hashing.DefaultHasher, arDataHashStr)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(arData, rData) {
		t.Fatal("Returned ActionResult data does not match")
	}

	if !proto.Equal(rAR, &ar) {
		t.Fatal("Returned ActionResult proto does not match")
	}
}

func TestGetWithOffset(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	const blobSize = 2048 + 256

	testCacheI, err := New(cacheDir, blobSize*2, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	testCache := testCacheI.(*diskCache)

	data, hash := testutils.RandomDataAndHash(blobSize, hashing.DefaultHasher)

	err = testCache.Put(ctx, cache.CAS, hashing.DefaultHasher, hash, blobSize,
		io.NopCloser(bytes.NewReader(data)))
	if err != nil {
		t.Fatal(err)
	}

	rc, foundSize, err := testCache.Get(ctx, cache.CAS, hashing.DefaultHasher, hash, int64(len(data)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if foundSize != int64(len(data)) {
		t.Fatalf("Got back a blob with size %d, original blob had size %d",
			foundSize, int64(len(data)))
	}

	// Read back the full blob, confirm the test state is OK.
	foundData, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if !bytes.Equal(data, foundData) {
		t.Fatal("Got back different data")
	}

	// Now try some partial reads.
	for _, offset := range []int64{42, 1023, 1024, 1025, 2048, 2303} {
		rc, foundSize, err = testCache.Get(ctx, cache.CAS, hashing.DefaultHasher, hash, int64(len(data)), offset)
		if err != nil {
			t.Fatal(err)
		}
		if foundSize != int64(len(data)) {
			t.Fatalf("Got back a blob with size %d, original blob had size %d",
				foundSize, int64(len(data)))
		}

		foundData, err = io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		rc.Close()
		if !bytes.Equal(data[offset:], foundData) {
			t.Fatalf("Expected data (%d bytes), differs from actual data (%d bytes) for offset %d",
				len(data[offset:]), len(foundData), offset)
		}
	}
}

func count(counter *prometheus.CounterVec, kind string, status string) float64 {
	gets := testutil.ToFloat64(counter.With(prometheus.Labels{"method": getMethod, "kind": kind, "status": status}))
	contains := testutil.ToFloat64(counter.With(prometheus.Labels{"method": containsMethod, "kind": kind, "status": status}))
	return gets + contains
}

func TestMetricsUnvalidatedAC(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)

	cacheSize := int64(100000)

	testCacheI, err := New(cacheDir, cacheSize,
		WithAccessLogger(testutils.NewSilentLogger()),
		WithEndpointMetrics())
	if err != nil {
		t.Fatal(err)
	}
	testCache := testCacheI.(*metricsDecorator)

	// Add an AC entry with a missing cas blob.
	randomBlob, hash := testutils.RandomDataAndHash(100, hashing.DefaultHasher)
	ar := pb.ActionResult{
		StdoutDigest: &pb.Digest{
			Hash:      hash,
			SizeBytes: int64(len(randomBlob)),
		},
	}
	arData, err := proto.Marshal(&ar)
	if err != nil {
		t.Fatal(err)
	}
	fakeActionHash := hashing.DefaultHasher.Hash(arData)

	err = testCache.Put(context.Background(), cache.AC, hashing.DefaultHasher, fakeActionHash, int64(len(arData)), bytes.NewReader(arData))
	if err != nil {
		t.Fatal(err)
	}

	contains, size := testCache.Contains(context.Background(), cache.AC, hashing.DefaultHasher, fakeActionHash, -1)
	if !contains {
		t.Fatalf("Expected hash %q to exist in the cache", fakeActionHash)
	}
	if size != int64(len(arData)) {
		t.Fatalf("Expected cached blob to be of size %d, found %d", len(arData), size)
	}

	acHits := count(testCache.counter, acKind, hitStatus)
	if acHits != 1 {
		t.Fatalf("Expected acHit counter to be 1, found %f", acHits)
	}

	acMiss := count(testCache.counter, acKind, missStatus)
	if acMiss != 0 {
		t.Fatalf("Expected acMiss counter to be 0, found %f", acMiss)
	}

	casHits := count(testCache.counter, casKind, hitStatus)
	if casHits != 0 {
		t.Fatalf("Expected casHit counter to be 0, found %f", casHits)
	}

	casMisses := count(testCache.counter, casKind, missStatus)
	if casMisses != 0 {
		t.Fatalf("Expected casMiss counter to be 0, found %f", casMisses)
	}

	rawHits := count(testCache.counter, rawKind, hitStatus)
	if rawHits != 0 {
		t.Fatalf("Expected rawHit counter to be 0, found %f", rawHits)
	}

	rawMisses := count(testCache.counter, rawKind, missStatus)
	if rawMisses != 0 {
		t.Fatalf("Expected rawMiss counter to be 0, found %f", rawMisses)
	}

	rc, _, err := testCache.Get(context.Background(), cache.AC, hashing.DefaultHasher, fakeActionHash, -1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rc == nil {
		t.Fatalf("Expected %q to be found in the action cache", fakeActionHash)
	}

	acHits = count(testCache.counter, acKind, hitStatus)
	if acHits != 2 {
		t.Fatalf("Expected acHit counter to be 2, found %f", acHits)
	}

	acMiss = count(testCache.counter, acKind, missStatus)
	if acMiss != 0 {
		t.Fatalf("Expected acMiss counter to be 0, found %f", acMiss)
	}

	casHits = count(testCache.counter, casKind, hitStatus)
	if casHits != 0 {
		t.Fatalf("Expected casHit counter to be 0, found %f", casHits)
	}

	casMisses = count(testCache.counter, casKind, missStatus)
	if casMisses != 0 {
		t.Fatalf("Expected casMiss counter to be 0, found %f", casMisses)
	}

	rawHits = count(testCache.counter, rawKind, hitStatus)
	if rawHits != 0 {
		t.Fatalf("Expected rawHit counter to be 0, found %f", rawHits)
	}

	rawMisses = count(testCache.counter, rawKind, missStatus)
	if rawMisses != 0 {
		t.Fatalf("Expected rawMiss counter to be 0, found %f", rawMisses)
	}
}

func TestMetricsValidatedAC(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)

	cacheSize := int64(100000)

	testCacheI, err := New(cacheDir, cacheSize,
		WithAccessLogger(testutils.NewSilentLogger()),
		WithEndpointMetrics())
	if err != nil {
		t.Fatal(err)
	}
	testCache := testCacheI.(*metricsDecorator)

	// Add an AC entry with a missing cas blob.
	randomBlob, hash := testutils.RandomDataAndHash(100, hashing.DefaultHasher)
	ar := pb.ActionResult{
		StdoutDigest: &pb.Digest{
			Hash:      hash,
			SizeBytes: int64(len(randomBlob)),
		},
	}
	arData, err := proto.Marshal(&ar)
	if err != nil {
		t.Fatal(err)
	}
	fakeActionHash := hashing.DefaultHasher.Hash(arData)

	err = testCache.Put(context.Background(), cache.AC, hashing.DefaultHasher, fakeActionHash, int64(len(arData)), bytes.NewReader(arData))
	if err != nil {
		t.Fatal(err)
	}

	// Neither Get nor Contains are supposed to be called on AC blobs in this mode.
	// GetValidatedActionResult is used instead in this case.
	// TODO: should those methods return errors for AC requests in that mode?

	gotAr, _, err := testCache.GetValidatedActionResult(context.Background(), hashing.DefaultHasher, fakeActionHash)
	if err != nil {
		t.Fatal(err)
	}
	if gotAr != nil {
		t.Fatal("Expected a cache miss, since the referenced CAS blob is missing")
	}

	acHits := count(testCache.counter, acKind, hitStatus)
	if acHits != 0 {
		t.Fatalf("Expected acHit counter to be 0, found %f", acHits)
	}

	acMisses := count(testCache.counter, acKind, missStatus)
	if acMisses != 1 {
		t.Fatalf("Expected acMiss counter to be 1, found %f", acMisses)
	}

	casHits := count(testCache.counter, casKind, hitStatus)
	if casHits != 0 {
		t.Fatalf("Expected casHit counter to be 0, found %f", casHits)
	}

	casMisses := count(testCache.counter, casKind, missStatus)
	if casMisses != 0 {
		// The referenced stdout blob is missing, but we're only supposed to count the AC lookup.
		t.Fatalf("Expected casMiss counter to be 1, found %f", casMisses)
	}

	rawHits := count(testCache.counter, rawKind, hitStatus)
	if rawHits != 0 {
		t.Fatalf("Expected rawHit counter to be 0, found %f", rawHits)
	}

	rawMisses := count(testCache.counter, rawKind, missStatus)
	if rawMisses != 0 {
		t.Fatalf("Expected rawMiss counter to be 0, found %f", rawMisses)
	}
}

func TestCacheDirLostAndFound(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)

	var err error

	// Create "lost+found" directories in every expected subdir of the cache dir.
	// We expect to be able to load this cache dir and ignore them.
	err = os.Mkdir(path.Join(cacheDir, "lost+found"), os.ModePerm)
	if err != nil {
		t.Fatal(err)
	}

	hexChars := []rune("0123456789abcdef")
	keySpaces := []string{"ac.v2", "cas.v2", "raw.v2"}

	for _, i := range hexChars {
		for _, j := range hexChars {
			bd := string(i) + string(j)
			for _, sd := range keySpaces {
				err = os.MkdirAll(path.Join(cacheDir, sd, bd, "lost+found"), os.ModePerm)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}

	_, err = New(cacheDir, 4096, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
}
