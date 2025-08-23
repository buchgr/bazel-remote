package disk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/buchgr/bazel-remote/v2/cache"
	testutils "github.com/buchgr/bazel-remote/v2/utils"
	"google.golang.org/protobuf/proto"

	pb "github.com/buchgr/bazel-remote/v2/genproto/build/bazel/remote/execution/v2"
)

func TestFilterNonNIl(t *testing.T) {
	t.Parallel()

	blob1 := pb.Digest{
		Hash:      "7f715e87ab77cfa3084ce8f7bb8f51e4059d02147b2139635673b7751004a170",
		SizeBytes: 152,
	}
	blob2 := pb.Digest{
		Hash:      "3db63cc7c4972b451c075f1ee198f4c02d8e5ec065f04b5d7b6cb2ba3aeb8ca6",
		SizeBytes: 136,
	}
	blob3 := pb.Digest{
		Hash:      "9205adc12a2c8b65e7cd77918ff8e6e20f39bdd0b7fc4b984abfd690c79d80c1",
		SizeBytes: 217,
	}

	tcs := []struct {
		input    []*pb.Digest
		expected map[*pb.Digest]struct{}
	}{
		{
			[]*pb.Digest{},
			map[*pb.Digest]struct{}{},
		},
		{
			[]*pb.Digest{nil},
			map[*pb.Digest]struct{}{},
		},
		{
			[]*pb.Digest{nil, nil, nil},
			map[*pb.Digest]struct{}{},
		},
		{
			[]*pb.Digest{nil, &blob1, &blob2, nil, &blob3},
			map[*pb.Digest]struct{}{
				&blob1: {},
				&blob2: {},
				&blob3: {},
			},
		},
	}

	for _, tc := range tcs {
		output := filterNonNil(tc.input)

		if len(output) != len(tc.expected) {
			t.Errorf("Expected %d items, found %d",
				len(tc.expected), len(output))
		}

		for _, ptr := range output {
			if ptr == nil {
				t.Errorf("Found nil pointer in output")
			}
		}

		for _, ptr := range tc.input {
			if ptr == nil {
				continue
			}

			_, exists := tc.expected[ptr]
			if !exists {
				t.Errorf("Expected to find %q in output", ptr)
			}
		}
	}
}

type testCWProxy struct {
	blob string
}

func (p *testCWProxy) Put(ctx context.Context, kind cache.EntryKind, hash string, logicalSize int64, sizeOnDisk int64, rc io.ReadCloser) {
}

func (p *testCWProxy) Get(ctx context.Context, kind cache.EntryKind, hash string, _ int64) (io.ReadCloser, int64, error) {
	return nil, -1, nil
}

func (p *testCWProxy) Contains(ctx context.Context, kind cache.EntryKind, hash string, _ int64) (bool, int64) {
	if kind == cache.CAS && hash == p.blob {
		return true, 42
	}
	return false, -1
}

func TestContainsWorker(t *testing.T) {
	t.Parallel()

	tp := testCWProxy{blob: "9205adc12a2c8b65e7cd77918ff8e6e20f39bdd0b7fc4b984abfd690c79d80c1"}

	c := diskCache{
		accessLogger:  testutils.NewSilentLogger(),
		proxy:         &tp,
		containsQueue: make(chan proxyCheck, 2),
	}

	// Spawn a single worker.
	go c.containsWorker()

	digests := []*pb.Digest{
		// Expect this to be found in the proxy, and replaced with nil.
		{Hash: tp.blob, SizeBytes: 42},

		// Expect this not to be found in the proxy, and left unchanged.
		{Hash: "423789fae66b9539c5622134c580700a154a15e355af4e3311a4e12ee0c9d243", SizeBytes: 43},
	}

	if cap(c.containsQueue) != len(digests) {
		t.Fatalf("Broken test setup, expected containsQueue capacity %d to match number of digests %d",
			cap(c.containsQueue), len(digests))
	}

	var wg sync.WaitGroup

	for i := range digests {
		wg.Add(1)
		c.containsQueue <- proxyCheck{
			wg:     &wg,
			digest: &digests[i],
		}
	}

	// Wait for the worker to process each request.
	wg.Wait()

	// Allow the worker goroutine to finish.
	close(c.containsQueue)

	if digests[0] != nil {
		t.Error("Expected digests[0] to be found in the proxy and replaced by nil")
	}

	if digests[1] == nil {
		t.Error("Expected digests[1] to not be found in the proxy and left as-is")
	}
}

type proxyAdapter struct {
	cache Cache
}

func NewProxyAdapter(cache Cache) (*proxyAdapter, error) {
	if cache == nil {
		return nil, fmt.Errorf("cache cannot be nil")
	}
	return &proxyAdapter{
		cache: cache,
	}, nil
}

func (p *proxyAdapter) Put(ctx context.Context, kind cache.EntryKind, hash string, logicalSize int64, sizeOnDisk int64, rc io.ReadCloser) {
	err := p.cache.Put(ctx, kind, hash, logicalSize, rc)
	if err != nil {
		panic(err)
	}
}

func (p *proxyAdapter) Get(ctx context.Context, kind cache.EntryKind, hash string, _ int64) (rc io.ReadCloser, size int64, err error) {
	return p.cache.Get(ctx, kind, hash, size, 0)
}

func (p *proxyAdapter) Contains(ctx context.Context, kind cache.EntryKind, hash string, _ int64) (bool, int64) {
	return p.cache.Contains(ctx, kind, hash, -1)
}

func TestFindMissingCasBlobsWithProxy(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := tempDir(t)
	defer func() { _ = os.RemoveAll(cacheDir) }()
	proxyCacheDir := tempDir(t)
	defer func() { _ = os.RemoveAll(proxyCacheDir) }()

	cacheForProxy, err := New(proxyCacheDir, 10*1024, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	proxy, err := NewProxyAdapter(cacheForProxy)
	if err != nil {
		t.Fatal(err)
	}

	testCache, err := New(cacheDir, 10*1024, WithProxyBackend(proxy), WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	data1, digest1 := testutils.RandomDataAndDigest(100)
	_, digest2 := testutils.RandomDataAndDigest(200)
	data3, digest3 := testutils.RandomDataAndDigest(300)
	_, digest4 := testutils.RandomDataAndDigest(400)

	proxy.Put(ctx, cache.CAS, digest1.Hash, digest1.SizeBytes, digest1.SizeBytes, io.NopCloser(bytes.NewReader(data1)))
	proxy.Put(ctx, cache.CAS, digest3.Hash, digest3.SizeBytes, digest3.SizeBytes, io.NopCloser(bytes.NewReader(data3)))

	missing, err := testCache.FindMissingCasBlobs(ctx, []*pb.Digest{
		&digest1,
		&digest2,
		&digest3,
		&digest4,
	})

	if err != nil {
		t.Fatal(err)
	}

	if len(missing) != 2 {
		t.Fatalf("Expected missing array to have exactly two entries, got %d", len(missing))
	}

	if !proto.Equal(missing[0], &digest2) {
		t.Fatalf("Expected missing[0] == digest2, got: %+v", missing[0])
	}

	if !proto.Equal(missing[1], &digest4) {
		t.Fatalf("Expected missing[1] == digest4, got: %+v", missing[1])
	}
}

func TestFindMissingCasBlobsWithProxyFailFast(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := tempDir(t)
	defer func() { _ = os.RemoveAll(cacheDir) }()
	proxyCacheDir := tempDir(t)
	defer func() { _ = os.RemoveAll(proxyCacheDir) }()

	cacheForProxy, err := New(proxyCacheDir, 10*1024, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	proxy, err := NewProxyAdapter(cacheForProxy)
	if err != nil {
		t.Fatal(err)
	}

	// Explicitly avoid using WithProxyBackEnd, as we want to control the workers.
	testCacheI, err := New(cacheDir, 10*1024, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	actualDiskCache := testCacheI.(*diskCache)
	actualDiskCache.proxy = proxy
	actualDiskCache.containsQueue = make(chan proxyCheck, 4)
	defer func() {
		close(actualDiskCache.containsQueue)
	}()
	// Spawn a single worker.
	go actualDiskCache.containsWorker()

	data1, digest1 := testutils.RandomDataAndDigest(100)
	_, digest2 := testutils.RandomDataAndDigest(200)
	data3, digest3 := testutils.RandomDataAndDigest(300)
	_, digest4 := testutils.RandomDataAndDigest(400)

	proxy.Put(ctx, cache.CAS, digest1.Hash, digest1.SizeBytes, digest1.SizeBytes, io.NopCloser(bytes.NewReader(data1)))
	proxy.Put(ctx, cache.CAS, digest3.Hash, digest3.SizeBytes, digest3.SizeBytes, io.NopCloser(bytes.NewReader(data3)))

	blobs := []*pb.Digest{
		&digest1,
		&digest2,
		&digest3,
		&digest4,
	}
	err = actualDiskCache.findMissingCasBlobsInternal(ctx, blobs, true)

	if !errors.Is(err, errMissingBlob) {
		t.Fatalf("Expected err to be errMissingBlob, got: %s", err)
	}

	if proto.Equal(blobs[0], &digest1) {
		t.Fatalf("Expected blobs[0] to equal digest1, got: %+v", blobs[0])
	}
}

func TestFindMissingCasBlobsWithProxyFailFastNoneMissing(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := tempDir(t)
	defer func() { _ = os.RemoveAll(cacheDir) }()
	proxyCacheDir := tempDir(t)
	defer func() { _ = os.RemoveAll(proxyCacheDir) }()

	cacheForProxy, err := New(proxyCacheDir, 40*1024, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	proxy, err := NewProxyAdapter(cacheForProxy)
	if err != nil {
		t.Fatal(err)
	}

	// Explicitly avoid using WithProxyBackEnd, as we want to control the workers.
	testCacheI, err := New(cacheDir, 40*1024, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}
	actualDiskCache := testCacheI.(*diskCache)
	actualDiskCache.proxy = proxy
	actualDiskCache.containsQueue = make(chan proxyCheck, 4)
	defer func() {
		close(actualDiskCache.containsQueue)
	}()
	// Spawn a single worker.
	go actualDiskCache.containsWorker()

	data1, digest1 := testutils.RandomDataAndDigest(100)
	data2, digest2 := testutils.RandomDataAndDigest(200)
	data3, digest3 := testutils.RandomDataAndDigest(300)
	data4, digest4 := testutils.RandomDataAndDigest(400)

	proxy.Put(ctx, cache.CAS, digest1.Hash, digest1.SizeBytes, digest1.SizeBytes, io.NopCloser(bytes.NewReader(data1)))
	proxy.Put(ctx, cache.CAS, digest2.Hash, digest2.SizeBytes, digest2.SizeBytes, io.NopCloser(bytes.NewReader(data2)))
	proxy.Put(ctx, cache.CAS, digest3.Hash, digest3.SizeBytes, digest3.SizeBytes, io.NopCloser(bytes.NewReader(data3)))
	proxy.Put(ctx, cache.CAS, digest4.Hash, digest4.SizeBytes, digest4.SizeBytes, io.NopCloser(bytes.NewReader(data4)))

	blobs := []*pb.Digest{
		&digest1,
		&digest2,
		&digest3,
		&digest4,
	}

	err = actualDiskCache.findMissingCasBlobsInternal(ctx, blobs, true)

	if err != nil {
		t.Fatal(err)
	}

	if blobs[0] != nil {
		t.Fatalf("Expected blobs[0] to be nil, got: %+v", blobs[0])
	}

	if blobs[1] != nil {
		t.Fatalf("Expected blobs[1] to be nil, got: %+v", blobs[1])
	}

	if blobs[2] != nil {
		t.Fatalf("Expected blobs[3] to be nil, got: %+v", blobs[2])
	}

	if blobs[3] != nil {
		t.Fatalf("Expected blobs[3] to be nil, got: %+v", blobs[3])
	}
}

func TestFindMissingCasBlobsWithProxyFailFastMaxProxyBlobSize(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := tempDir(t)
	defer func() { _ = os.RemoveAll(cacheDir) }()
	proxyCacheDir := tempDir(t)
	defer func() { _ = os.RemoveAll(proxyCacheDir) }()

	cacheForProxy, err := New(proxyCacheDir, 10*1024, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	proxy, err := NewProxyAdapter(cacheForProxy)
	if err != nil {
		t.Fatal(err)
	}

	// Explicitly avoid using WithProxyBackEnd, as we want to control the workers.
	testCacheI, err := New(cacheDir, 10*1024, WithAccessLogger(testutils.NewSilentLogger()), WithProxyMaxBlobSize(300))
	if err != nil {
		t.Fatal(err)
	}
	actualDiskCache := testCacheI.(*diskCache)
	actualDiskCache.proxy = proxy
	actualDiskCache.containsQueue = make(chan proxyCheck, 4)
	defer func() {
		close(actualDiskCache.containsQueue)
	}()
	// Spawn a single worker.
	go actualDiskCache.containsWorker()

	data1, digest1 := testutils.RandomDataAndDigest(100)
	data2, digest2 := testutils.RandomDataAndDigest(200)
	data3, digest3 := testutils.RandomDataAndDigest(300) // We expect this blob to not be found.

	// Put blobs directly into proxy backend, where it will not be filtered out.
	proxy.Put(ctx, cache.CAS, digest1.Hash, digest1.SizeBytes, digest1.SizeBytes, io.NopCloser(bytes.NewReader(data1)))
	proxy.Put(ctx, cache.CAS, digest2.Hash, digest2.SizeBytes, digest2.SizeBytes, io.NopCloser(bytes.NewReader(data2)))
	proxy.Put(ctx, cache.CAS, digest3.Hash, digest3.SizeBytes, digest3.SizeBytes, io.NopCloser(bytes.NewReader(data3)))

	blobs := []*pb.Digest{
		&digest1,
		&digest2,
		&digest3,
	}
	err = actualDiskCache.findMissingCasBlobsInternal(ctx, blobs, true)

	if !errors.Is(err, errMissingBlob) {
		t.Fatalf("Expected err to be errMissingBlob, got: %s", err)
	}

	if blobs[0] == nil {
		t.Fatalf("Expected blobs[0] to be nil, got: %+v", blobs[0])
	}

	if blobs[1] == nil {
		t.Fatalf("Expected blobs[1] to be nil, got: %+v", blobs[1])
	}

	if !proto.Equal(blobs[2], &digest3) {
		t.Fatalf("Expected blobs[2] == digest3, got %+v", blobs[2])
	}
}

func TestFindMissingCasBlobsWithProxyMaxProxyBlobSize(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheDir := tempDir(t)
	defer func() { _ = os.RemoveAll(cacheDir) }()
	proxyCacheDir := tempDir(t)
	defer func() { _ = os.RemoveAll(proxyCacheDir) }()

	cacheForProxy, err := New(proxyCacheDir, 10*1024, WithAccessLogger(testutils.NewSilentLogger()))
	if err != nil {
		t.Fatal(err)
	}

	proxy, err := NewProxyAdapter(cacheForProxy)
	if err != nil {
		t.Fatal(err)
	}

	testCache, err := New(cacheDir, 10*1024, WithProxyBackend(proxy), WithAccessLogger(testutils.NewSilentLogger()), WithProxyMaxBlobSize(500))
	if err != nil {
		t.Fatal(err)
	}

	data1, digest1 := testutils.RandomDataAndDigest(100)
	data2, digest2 := testutils.RandomDataAndDigest(600)

	proxy.Put(ctx, cache.CAS, digest1.Hash, digest1.SizeBytes, digest1.SizeBytes, io.NopCloser(bytes.NewReader(data1)))
	proxy.Put(ctx, cache.CAS, digest2.Hash, digest2.SizeBytes, digest2.SizeBytes, io.NopCloser(bytes.NewReader(data2)))

	missing, err := testCache.FindMissingCasBlobs(ctx, []*pb.Digest{
		&digest1,
		&digest2,
	})

	if err != nil {
		t.Fatal(err)
	}

	if len(missing) != 1 {
		t.Fatalf("Expected missing array to have exactly one entry, got %d", len(missing))
	}

	if !proto.Equal(missing[0], &digest2) {
		t.Fatalf("Expected missing[0] == digest2, got %+v", missing[0])
	}
}
