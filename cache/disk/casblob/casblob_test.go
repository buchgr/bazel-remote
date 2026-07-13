package casblob_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"testing"
	"unsafe"

	"github.com/buchgr/bazel-remote/v2/cache/disk/casblob"
	"github.com/buchgr/bazel-remote/v2/cache/disk/zstdimpl"
	testutils "github.com/buchgr/bazel-remote/v2/utils"
)

func TestLenSize(t *testing.T) {
	slice := []int{}
	if unsafe.Sizeof(len(slice)) > 8 {
		// If this fails, then we have a bunch of potential truncation
		// errors all over the place.
		t.Errorf("len() returns a value larger than 8 bytes")
	}
	if len(slice) != 0 {
		// We should never hit this case.
		t.Errorf("This should silence linters that think slice is never used")
	}
}

func TestZstdFromLegacy(t *testing.T) {
	size := 1024
	zstd, err := zstdimpl.Get("go")
	if err != nil {
		t.Fatal(err)
	}

	data, hash := testutils.RandomDataAndHash(int64(size))
	dir := testutils.TempDir(t)
	filename := fmt.Sprintf("%s/%s", dir, hash)
	file, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0664)
	if err != nil {
		t.Fatal(err)
	}
	n, err := file.Write(data)
	if err != nil {
		t.Fatal(err)
	}
	if n != size {
		t.Fatalf("Unexpected short write %d, expected %d", n, size)
	}
	err = file.Close()
	if err != nil {
		t.Fatal(err)
	}

	file, err = os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	zrc, err := casblob.GetLegacyZstdReadCloser(zstd, file)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := zstd.GetDecoder(zrc)
	if err != nil {
		t.Fatal(err)
	}
	buf := bytes.NewBuffer(nil)
	_, err = io.Copy(buf, rc)
	if err != nil {
		t.Fatal(err)
	}

	if buf.Len() != size {
		t.Fatalf("Unexpected buf size %d, expected %d", buf.Len(), size)
	}

	h := sha256.Sum256(data)
	hs := hex.EncodeToString(h[:])
	if hs != hash {
		t.Fatalf("Unexpected content sha %s, expected %s", hs, hash)
	}
}

// blobSizeForBenchmark spans several 1 MiB chunks so WriteAndClose compresses
// in a loop, exercising the per-chunk output-buffer reuse.
// See https://github.com/buchgr/bazel-remote/pull/907.
const blobSizeForBenchmark = 16 * 1024 * 1024 // 16 MiB => 16 chunks

// writeBlob is the benchmarks' unit of work: one WriteAndClose to a fresh temp
// file, then remove it.
func writeBlob(tb testing.TB, zstd zstdimpl.ZstdImpl, dir string, data []byte, hash string) {
	f, err := os.CreateTemp(dir, "blob-")
	if err != nil {
		tb.Fatal(err)
	}
	name := f.Name()
	_, err = casblob.WriteAndClose(zstd, bytes.NewReader(data), f,
		casblob.Zstandard, hash, int64(len(data)))
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.Remove(name); err != nil {
		tb.Fatal(err)
	}
}

// BenchmarkWriteAndCloseZstd measures allocations of the zstd write path for a
// single upload. Run with -benchmem; B/op is the regression metric.
func BenchmarkWriteAndCloseZstd(b *testing.B) {
	zstd, err := zstdimpl.Get("go")
	if err != nil {
		b.Fatal(err)
	}

	// Incompressible data is the worst case: each chunk's output stays near the
	// full 1 MiB.
	data, hash := testutils.RandomDataAndHash(blobSizeForBenchmark)
	dir := b.TempDir()

	b.SetBytes(blobSizeForBenchmark)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		writeBlob(b, zstd, dir, data, hash)
	}
}

// BenchmarkWriteAndCloseZstdParallel reproduces a concurrent upload burst: many
// Puts compressing at once. Run with -benchmem for the aggregate alloc rate.
func BenchmarkWriteAndCloseZstdParallel(b *testing.B) {
	zstd, err := zstdimpl.Get("go")
	if err != nil {
		b.Fatal(err)
	}

	data, hash := testutils.RandomDataAndHash(blobSizeForBenchmark)
	dir := b.TempDir()

	b.SetBytes(blobSizeForBenchmark)
	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			writeBlob(b, zstd, dir, data, hash)
		}
	})
}
