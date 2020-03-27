package disk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buchgr/bazel-remote/cache"
	"github.com/buchgr/bazel-remote/cache/httpproxy"
	"github.com/buchgr/bazel-remote/utils"

	pb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"github.com/golang/protobuf/proto"
)

func tempDir(t *testing.T) string {
	dir, err := ioutil.TempDir("", "bazel-remote")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func checkItems(cache *DiskCache, expSize int64, expNum int) error {
	if cache.lru.Len() != expNum {
		return fmt.Errorf("expected %d files in the cache, found %d", expNum, cache.lru.Len())
	}
	if cache.lru.CurrentSize() != expSize {
		return fmt.Errorf("expected %d bytes in the cache, found %d", expSize, cache.lru.CurrentSize())
	}

	// Dig into the internals of the cache to make sure that all items are committed.
	for _, it := range cache.lru.(*sizedLRU).cache {
		if it.Value.(*entry).value.(*lruItem).committed != true {
			return fmt.Errorf("expected committed = true")
		}
	}

	numFiles := 0
	err := filepath.Walk(cache.dir, func(name string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			numFiles++
		}
		return nil
	})
	if err != nil {
		return err
	}

	if numFiles != expNum {
		return fmt.Errorf("expected %d files on disk, found %d", expNum, numFiles)
	}

	return nil
}

const KEY = "a-key"
const CONTENTS = "hello"
const CONTENTS_HASH = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

func TestCacheBasics(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache := New(cacheDir, 100, nil)

	err := checkItems(testCache, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Non-existing item
	rdr, _, err := testCache.Get(cache.CAS, CONTENTS_HASH)
	if err != nil {
		t.Fatal(err)
	}
	if rdr != nil {
		t.Fatal("expected the item not to exist")
	}

	// Add an item
	err = testCache.Put(cache.CAS, CONTENTS_HASH, int64(len(CONTENTS)), strings.NewReader(CONTENTS))
	if err != nil {
		t.Fatal(err)
	}

	// Dig into the internals to make sure that the cache state has been
	// updated correctly
	err = checkItems(testCache, int64(len(CONTENTS))+headerSize[pb.DigestFunction_SHA256], 1)
	if err != nil {
		t.Fatal(err)
	}

	// Get the item back
	rdr, sizeBytes, err := testCache.Get(cache.CAS, CONTENTS_HASH)
	if err != nil {
		t.Fatal(err)
	}

	err = expectContentEquals(rdr, sizeBytes, []byte(CONTENTS))
	if err != nil {
		t.Fatal(err)
	}
}

func TestCacheEviction(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)

	testCache := New(cacheDir, 450, nil)

	expectedSizesNumItems := []struct {
		blobSize  int
		totalSize int64
		expNum    int
	}{
		{0, 44, 1},    // 0
		{10, 98, 2},   // 1, 0
		{30, 172, 3},  // 2, 1, 0
		{60, 276, 4},  // 3, 2, 1, 0
		{120, 440, 5}, // 4, 3, 2, 1, 0
		{90, 402, 3},  // 5, 4, 3 ; 574 evict 0 => 530, evict 1 => 476, evict 2 => 402
		{60, 402, 3},  // 6, 5, 4 ; 506 evict 3 => 402
		{70, 352, 3},  // 7, 6, 5 ; 516 evict 4 => 238
	}

	for i, thisExp := range expectedSizesNumItems {
		strReader := strings.NewReader(strings.Repeat("a", thisExp.blobSize))

		// Suitably-sized, unique key for these testcases:
		key := fmt.Sprintf("%0*d", sha256HashStrSize, i)
		if len(key) != sha256.Size*2 {
			t.Fatalf("invalid testcase- key length should be %d, not %d: %s",
				sha256.Size*2, len(key), key)
		}

		err := testCache.Put(cache.AC, key, int64(thisExp.blobSize), strReader)
		if err != nil {
			t.Fatal(err)
		}

		err = checkItems(testCache, thisExp.totalSize, thisExp.expNum)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCachePutWrongSize(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache := New(cacheDir, 100, nil)

	content := "hello"
	hash := hashStr(content)

	var err error

	err = testCache.Put(cache.AC, hash, int64(len(content)), strings.NewReader(content))
	if err != nil {
		t.Fatal("Expected success", err)
	}

	err = testCache.Put(cache.AC, hash, int64(len(content))+1, strings.NewReader(content))
	if err == nil {
		t.Error("Expected error due to size being different")
	}
	err = testCache.Put(cache.AC, hash, int64(len(content))-1, strings.NewReader(content))
	if err == nil {
		t.Error("Expected error due to size being different")
	}
}

func expectContentEquals(rdr io.ReadCloser, sizeBytes int64, expectedContent []byte) error {
	if rdr == nil {
		return fmt.Errorf("expected the item to exist")
	}
	data, err := ioutil.ReadAll(rdr)
	if err != nil {
		return err
	}
	if bytes.Compare(data, expectedContent) != 0 {
		return fmt.Errorf("expected response '%s', but received '%s'",
			expectedContent, data)
	}
	if int64(len(data)) != sizeBytes {
		return fmt.Errorf("Expected sizeBytes to be '%d' but was '%d'",
			sizeBytes, len(data))
	}

	return nil
}

func putGetCompare(kind cache.EntryKind, hash string, content string, testCache *DiskCache) error {
	return putGetCompareBytes(kind, hash, []byte(content), testCache)
}

func putGetCompareBytes(kind cache.EntryKind, hash string, data []byte, testCache *DiskCache) error {

	r := bytes.NewReader(data)

	err := testCache.Put(kind, hash, int64(len(data)), r)
	if err != nil {
		return err
	}

	rdr, sizeBytes, err := testCache.Get(kind, hash)
	if err != nil {
		return err
	}
	// Get the item back
	return expectContentEquals(rdr, sizeBytes, data)
}

func hashStr(content string) string {
	hashBytes := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hashBytes[:])
}

// Make sure that we can overwrite items if we upload the same key again.
func TestOverwrite(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache := New(cacheDir, 10+headerSize[pb.DigestFunction_SHA256], nil)

	var err error
	err = putGetCompare(cache.CAS, hashStr("hello"), "hello", testCache)
	if err != nil {
		t.Fatal(err)
	}
	err = putGetCompare(cache.CAS, hashStr("hello"), "hello", testCache)
	if err != nil {
		t.Fatal(err)
	}

	err = putGetCompare(cache.AC, hashStr("world"), "world1", testCache)
	if err != nil {
		t.Fatal(err)
	}
	err = putGetCompare(cache.AC, hashStr("world"), "world2", testCache)
	if err != nil {
		t.Fatal(err)
	}

	err = putGetCompare(cache.RAW, hashStr("world"), "world3", testCache)
	if err != nil {
		t.Fatal(err)
	}
	err = putGetCompare(cache.RAW, hashStr("world"), "world4", testCache)
	if err != nil {
		t.Fatal(err)
	}
}

func ensureDirExists(t *testing.T, path string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		err = os.MkdirAll(path, os.ModePerm)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCacheExistingFiles(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)

	blobs := make([]struct {
		data       []byte
		sha256hash string
		file       string
	}, 4, 4)
	blobs[0].data, blobs[0].sha256hash = testutils.RandomDataAndHash(64)
	blobs[0].file = filepath.Join("cas", blobs[0].sha256hash[:2], blobs[0].sha256hash)

	blobs[1].data = make([]byte, len(blobs[0].data))
	copy(blobs[1].data, blobs[0].data)
	blobs[1].data[0]++
	hb := sha256.Sum256(blobs[1].data)
	blobs[1].sha256hash = hex.EncodeToString(hb[:])
	blobs[1].file = filepath.Join("cas", blobs[1].sha256hash[:2], blobs[1].sha256hash)

	blobs[2].data = make([]byte, len(blobs[0].data))
	copy(blobs[2].data, blobs[0].data)
	blobs[2].data[0]++
	hb = sha256.Sum256(blobs[2].data)
	blobs[2].sha256hash = hex.EncodeToString(hb[:])
	blobs[2].file = filepath.Join("ac.v2", blobs[2].sha256hash[:2], blobs[2].sha256hash)

	blobs[3].data = make([]byte, len(blobs[0].data))
	copy(blobs[3].data, blobs[2].data)
	blobs[3].sha256hash = blobs[2].sha256hash
	blobs[3].file = filepath.Join("raw.v2", blobs[3].sha256hash[:2], blobs[3].sha256hash)

	for _, it := range blobs {
		dn := filepath.Join(cacheDir, filepath.Dir(it.file))
		ensureDirExists(t, dn)
		fn := filepath.Join(cacheDir, it.file)
		f, err := os.Create(fn)
		if err != nil {
			t.Fatal(err)
		}

		err = writeHeader(f, it.sha256hash, int64(len(it.data)))
		if err != nil {
			t.Fatal(err)
		}

		n, err := f.Write(it.data)
		if err != nil {
			t.Fatal(err)
		}
		if n != len(it.data) {
			t.Fatalf("short write: %d, expected: %d", n, len(it.data))
		}

		// Wait a bit to account for atime granularity
		time.Sleep(10 * time.Millisecond)
	}

	expectedSize := 4 * (int64(len(blobs[0].data)) + headerSize[pb.DigestFunction_SHA256])
	testCache := New(cacheDir, expectedSize, nil)

	err := checkItems(testCache, expectedSize, 4)
	if err != nil {
		t.Fatal(err)
	}

	// Adding a new file should evict items[0] (the oldest)
	err = testCache.Put(cache.CAS, CONTENTS_HASH, int64(len(CONTENTS)),
		strings.NewReader(CONTENTS))
	if err != nil {
		t.Fatal(err)
	}

	expectedSize = expectedSize - int64(len(blobs[0].data)) + int64(len(CONTENTS))

	err = checkItems(testCache, expectedSize, 4)
	if err != nil {
		t.Fatal(err)
	}
	found, _ := testCache.Contains(cache.CAS, "f53b46209596d170f7659a414c9ff9f6b545cf77ffd6e1cbe9bcc57e1afacfbd")
	if found {
		t.Fatalf("%s should have been evicted", blobs[0].sha256hash)
	}
}

// Make sure that the cache returns http.StatusInsufficientStorage when trying to upload an item
// that's bigger than the maximum size.
func TestCacheBlobTooLarge(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache := New(cacheDir, 100, nil)

	for k := range []cache.EntryKind{cache.AC, cache.RAW} {
		kind := cache.EntryKind(k)
		err := testCache.Put(kind, hashStr("foo"), 10000, strings.NewReader(CONTENTS))
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
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache := New(cacheDir, 1000, nil)

	err := testCache.Put(cache.CAS, hashStr("foo"), int64(len(CONTENTS)),
		strings.NewReader(CONTENTS))
	if err == nil {
		t.Fatal("expected hash mismatch error")
	}

	// We expect the upload to succeed without validation:
	err = testCache.Put(cache.RAW, hashStr("foo"), int64(len(CONTENTS)),
		strings.NewReader(CONTENTS))
	if err != nil {
		t.Fatal(err)
	}
}

func TestMigrateFromOldDirectoryStructure(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	acSize := int64(512)
	acHash, err := testutils.CreateRandomFile(cacheDir+"/ac/", acSize)
	if err != nil {
		t.Fatal(err)
	}

	casSize := int64(1024)
	casHash1, err := testutils.CreateRandomFile(cacheDir+"/cas/", casSize)
	if err != nil {
		t.Fatal(err)
	}
	casHash2, err := testutils.CreateRandomFile(cacheDir+"/cas/", casSize)
	if err != nil {
		t.Fatal(err)
	}

	sha256HeaderSize := headerSize[pb.DigestFunction_SHA256]

	cacheSize := acSize + (casSize+sha256HeaderSize)*2
	testCache := New(cacheDir, cacheSize, nil)

	var found bool
	found, _ = testCache.Contains(cache.AC, acHash)
	if !found {
		t.Fatalf("Expected cache to contain AC entry '%s'", acHash)
	}

	found, _ = testCache.Contains(cache.CAS, casHash1)
	if !found {
		t.Fatalf("Expected cache to contain CAS entry '%s'", casHash1)
	}

	found, _ = testCache.Contains(cache.CAS, casHash2)
	if !found {
		t.Fatalf("Expected cache to contain CAS entry '%s'", casHash2)
	}

	_, numItems := testCache.Stats()
	if numItems != 3 {
		t.Fatalf("Expected test cache size 3 but was %d", numItems)
	}
}

func TestLoadExistingEntries(t *testing.T) {
	// Test that loading existing items works
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	numBlobs := int64(3)
	blobSize := int64(1024)

	acHash, err := testutils.CreateCacheFile(cacheDir+"/ac.v2/", blobSize)
	if err != nil {
		t.Fatal(err)
	}
	casHash, err := testutils.CreateCacheFile(cacheDir+"/cas/", blobSize)
	if err != nil {
		t.Fatal(err)
	}
	rawHash, err := testutils.CreateCacheFile(cacheDir+"/raw.v2/", blobSize)
	if err != nil {
		t.Fatal(err)
	}

	testCache := New(cacheDir, blobSize*numBlobs, nil)
	_, numItems := testCache.Stats()
	if int64(numItems) != numBlobs {
		t.Fatalf("Expected test cache size %d but was %d",
			numBlobs, numItems)
	}

	var found bool

	found, _ = testCache.Contains(cache.AC, acHash)
	if !found {
		t.Fatalf("Expected cache to contain AC entry '%s'", acHash)
	}

	found, _ = testCache.Contains(cache.CAS, casHash)
	if !found {
		t.Fatalf("Expected cache to contain CAS entry '%s'", casHash)
	}

	found, _ = testCache.Contains(cache.RAW, rawHash)
	if !found {
		t.Fatalf("Expected cache to contain RAW entry '%s'", rawHash)
	}
}

func TestDistinctKeyspaces(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	blobSize := int64(1024)
	cacheSize := (blobSize + headerSize[pb.DigestFunction_SHA256]) * 3

	testCache := New(cacheDir, cacheSize, nil)

	blob, casHash := testutils.RandomDataAndHash(blobSize)

	// Add the same blob with the same key, to each of the three
	// keyspaces, and verify that we have exactly three items in
	// the cache.

	var err error

	err = putGetCompareBytes(cache.CAS, casHash, blob, testCache)
	if err != nil {
		t.Fatal(err)
	}

	err = putGetCompareBytes(cache.AC, casHash, blob, testCache)
	if err != nil {
		t.Fatal(err)
	}

	err = putGetCompareBytes(cache.RAW, casHash, blob, testCache)
	if err != nil {
		t.Fatal(err)
	}

	_, numItems := testCache.Stats()
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
		w.Write(data)

	case http.MethodPut:
		data, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusInternalServerError)
		}
		kindMap[hash] = data

	case http.MethodHead:
		_, ok := kindMap[hash]
		if !ok {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
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

	backend := newTestServer(t)
	url, err := url.Parse(backend.srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	accessLogger := testutils.NewSilentLogger()
	errorLogger := testutils.NewSilentLogger()

	proxy := httpproxy.New(url, &http.Client{}, accessLogger, errorLogger)

	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	cacheSize := int64(1024 * 10)

	testCache := New(cacheDir, cacheSize, proxy)

	blobSize := int64(1024)
	blob, casHash := testutils.RandomDataAndHash(blobSize)

	// Non-existing item
	r, _, err := testCache.Get(cache.CAS, casHash)
	if err != nil {
		t.Fatal(err)
	}
	if r != nil {
		t.Fatal("Expected nil reader")
	}

	if backend.numItems() != 0 {
		t.Fatal("Expected empty backend")
	}

	err = testCache.Put(cache.CAS, casHash, int64(len(blob)),
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
	testCache = New(cacheDir, cacheSize, nil)

	// Confirm that it does not contain the item we added to the
	// first testCache and the proxy backend.

	found, _ := testCache.Contains(cache.CAS, casHash)
	if found {
		t.Fatalf("Expected the cache not to contain %s", casHash)
	}

	r, _, err = testCache.Get(cache.CAS, casHash)
	if err != nil {
		t.Fatal(err)
	}
	if r != nil {
		t.Fatal("Expected testCache to be empty")
	}

	// Add the proxy backend and check that we can Get the item.
	testCache.proxy = proxy

	found, _ = testCache.Contains(cache.CAS, casHash)
	if !found {
		t.Fatalf("Expected the cache to contain %s (via the proxy)",
			casHash)
	}

	r, fetchedSize, err := testCache.Get(cache.CAS, casHash)
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("Expected the Get to succeed")
	}
	if fetchedSize != blobSize {
		t.Fatalf("Expected a blob of size %d, got %d", blobSize, fetchedSize)
	}

	retrievedData, err := ioutil.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if int64(len(retrievedData)) != blobSize {
		t.Fatalf("Expected '%d' bytes of data, but received '%d'",
			blobSize, len(retrievedData))
	}

	if bytes.Compare(retrievedData, blob) != 0 {
		t.Fatalf("Expected '%v' but received '%v", retrievedData, blob)
	}
}

// Store an ActionResult with an output directory, then confirm that
// GetValidatedActionResult returns the original item.
func TestGetValidatedActionResult(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	testCache := New(cacheDir, 1024*32, nil)

	// Create a directory tree like so:
	// /bar/foo.txt
	// /bar/grok.txt

	var err error

	grokData := []byte("grok test data")
	grokHash := sha256.Sum256(grokData)
	grokHashStr := hex.EncodeToString(grokHash[:])

	err = testCache.Put(cache.CAS, grokHashStr, int64(len(grokData)),
		bytes.NewReader(grokData))
	if err != nil {
		t.Fatal(err)
	}

	fooData := []byte("foo test data")
	fooHash := sha256.Sum256(fooData)
	fooHashStr := hex.EncodeToString(fooHash[:])

	err = testCache.Put(cache.CAS, fooHashStr, int64(len(fooData)),
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
	barDataHash := sha256.Sum256(barData)
	barDataHashStr := hex.EncodeToString(barDataHash[:])

	err = testCache.Put(cache.CAS, barDataHashStr, int64(len(barData)),
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
	rootDataHash := sha256.Sum256(rootData)
	rootDataHashStr := hex.EncodeToString(rootDataHash[:])

	err = testCache.Put(cache.CAS, rootDataHashStr, int64(len(rootData)),
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
	treeDataHash := sha256.Sum256(treeData)
	treeDataHashStr := hex.EncodeToString(treeDataHash[:])

	err = testCache.Put(cache.CAS, treeDataHashStr, int64(len(treeData)),
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
	arDataHash := sha256.Sum256([]byte("pretend action"))
	arDataHashStr := hex.EncodeToString(arDataHash[:])

	err = testCache.Put(cache.AC, arDataHashStr, int64(len(arData)),
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

	rAR, rData, err := testCache.GetValidatedActionResult(arDataHashStr)
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

func TestChecksumHeader(t *testing.T) {

	blob := []byte{0, 1, 2, 3, 4, 5, 6, 7}

	testCases := []struct {
		kind    pb.DigestFunction_Value
		hash    string
		size    int64
		success bool // True if the {hash,size} are valid.
	}{
		{pb.DigestFunction_SHA256,
			"0000000011111111222222223333333344444444555555556666666677777777",
			42, true},
		{pb.DigestFunction_SHA256,
			"0000000011111111222222223333333344444444555555556666666677777777",
			0, true},

		{pb.DigestFunction_UNKNOWN,
			"00000000111111112222222233333333444444445555555566666666777777778",
			42, false}, // invalid hex string (odd length)
		{pb.DigestFunction_UNKNOWN,
			"000000001111111122222222333333334444444455555555666666667777777788",
			42, false}, // hash too long
		{pb.DigestFunction_UNKNOWN,
			"000000001111111122222222333333334444444455555555666666667777777",
			42, false}, // invalid hex string (odd length)
		{pb.DigestFunction_UNKNOWN,
			"00000000111111112222222233333333444444445555555566666666777777",
			42, false}, // hash too short
		{pb.DigestFunction_UNKNOWN,
			"",
			42, false},
		{pb.DigestFunction_UNKNOWN,
			"0000000011111111222222223333333344444444555555556666666677777777",
			-1, false}, // invalid (negative) size
	}

	// Note that these tests just confirm that we can read/write a valid
	// header and a blob. They dot not confirm that the header describes
	// the blob.

	for _, tc := range testCases {
		var buf bytes.Buffer

		err := writeHeader(&buf, tc.hash, tc.size)
		if !tc.success {
			if err == nil {
				t.Error("Expected testcase to fail", tc.hash, tc.size)
			}

			continue
		}
		if err != nil {
			t.Fatal("Expected testscase to succeed, got:", err)
		}

		// Check the header size manually, since it's not exposed by
		// the readHeader function.
		if int64(buf.Len()) != headerSize[tc.kind] {
			t.Fatalf("Expected data header of size %d bytes, got %d. %s %d %v %s",
				headerSize[tc.kind], buf.Len(), tc.hash, tc.size, tc.success, err)
		}

		// Write the blob.
		n, err := buf.Write(blob)
		if err != nil {
			t.Fatal(err)
		}
		if n != len(blob) {
			t.Fatalf("expected to write %d bytes, instead wrote %d bytes",
				len(blob), n)
		}

		dt, readHash, readSize, err := readHeader(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if dt == pb.DigestFunction_UNKNOWN {
			t.Fatal("Unknown digest type")
		}

		if readHash != tc.hash {
			t.Fatalf("Read a different hash '%s' than was written '%s'",
				readHash, tc.hash)
		}

		if readSize != tc.size {
			t.Fatalf("Read a different size %d than was written %d",
				readSize, tc.size)
		}

		readBlob, err := ioutil.ReadAll(&buf)
		if err != nil {
			t.Fatal(err)
		}

		if bytes.Compare(blob, readBlob) != 0 {
			t.Fatal("Read a different blob than was written")
		}
	}
}
