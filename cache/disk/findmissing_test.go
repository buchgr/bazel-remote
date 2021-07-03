package disk

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/buchgr/bazel-remote/cache"
	testutils "github.com/buchgr/bazel-remote/utils"

	pb "github.com/buchgr/bazel-remote/genproto/build/bazel/remote/execution/v2"
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
				t.Errorf("Expected to find %q in output", *ptr)
			}
		}
	}
}

type testCWProxy struct {
	blob string
}

func (p *testCWProxy) Put(kind cache.EntryKind, hash string, size int64, rc io.ReadCloser) {
}
func (p *testCWProxy) Get(ctx context.Context, kind cache.EntryKind, hash string) (io.ReadCloser, int64, error) {
	return nil, -1, nil
}
func (p *testCWProxy) Contains(ctx context.Context, kind cache.EntryKind, hash string) (bool, int64) {
	if kind == cache.CAS && hash == p.blob {
		return true, 42
	}
	return false, -1
}

func TestContainsWorker(t *testing.T) {
	t.Parallel()

	tp := testCWProxy{blob: "9205adc12a2c8b65e7cd77918ff8e6e20f39bdd0b7fc4b984abfd690c79d80c1"}

	c := Cache{
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
