package casblob_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"
	"unsafe"

	"github.com/buchgr/bazel-remote/v2/cache/disk/casblob"
	"github.com/buchgr/bazel-remote/v2/cache/disk/zstdimpl"
	"github.com/buchgr/bazel-remote/v2/cache/hashing"
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

	data, hash := testutils.RandomDataAndHash(int64(size), hashing.DefaultHasher)
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
	file.Close()

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

	hs := hashing.DefaultHasher.Hash(data)
	if hs != hash {
		t.Fatalf("Unexpected content sha %s, expected %s", hs, hash)
	}
}
