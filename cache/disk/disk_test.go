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
	testutils "github.com/buchgr/bazel-remote/utils"

	pb "github.com/buchgr/bazel-remote/genproto/build/bazel/remote/execution/v2"
	"github.com/golang/protobuf/proto"
)

func tempDir(t *testing.T) string {
	dir, err := ioutil.TempDir("", "bazel-remote")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func checkItems(cache *Cache, expSize int64, expNum int) error {
	if cache.lru.Len() != expNum {
		return fmt.Errorf("expected %d files in the cache, found %d", expNum, cache.lru.Len())
	}
	if cache.lru.TotalSize() != expSize {
		return fmt.Errorf("expected %d bytes in the cache, found %d", expSize, cache.lru.TotalSize())
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
const contents = "hello"
const contentsHash = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
const contentsLength = int64(len(contents))

func TestCacheBasics(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache, err := New(cacheDir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}

	err = checkItems(testCache, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Non-existing item
	rdr, _, err := testCache.Get(cache.CAS, contentsHash, contentsLength)
	if err != nil {
		t.Fatal(err)
	}
	if rdr != nil {
		t.Fatal("expected the item not to exist")
	}

	// Add an item
	err = testCache.Put(cache.CAS, contentsHash, int64(len(contents)),
		ioutil.NopCloser(strings.NewReader(contents)))
	if err != nil {
		t.Fatal(err)
	}

	// Dig into the internals to make sure that the cache state has been
	// updated correctly
	err = checkItems(testCache, int64(len(contents)), 1)
	if err != nil {
		t.Fatal(err)
	}

	// Get the item back
	rdr, sizeBytes, err := testCache.Get(cache.CAS, contentsHash, contentsLength)
	if err != nil {
		t.Fatal(err)
	}

	err = expectContentEquals(rdr, sizeBytes, []byte(contents))
	if err != nil {
		t.Fatal(err)
	}
}

func TestCacheEviction(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache, err := New(cacheDir, 10, nil)
	if err != nil {
		t.Fatal(err)
	}

	expectedSizesNumItems := []struct {
		expSize int64
		expNum  int
	}{
		{0, 1},  // 0
		{1, 2},  // 0, 1
		{3, 3},  // 0, 1, 2
		{6, 4},  // 0, 1, 2, 3
		{10, 5}, // 0, 1, 2, 3, 4
		{9, 2},  // 4, 5
		{6, 1},  // 6
		{7, 1},  // 7
	}

	for i, thisExp := range expectedSizesNumItems {
		strReader := strings.NewReader(strings.Repeat("a", i))

		// Suitably-sized, unique key for these testcases:
		key := fmt.Sprintf("%0*d", sha256HashStrSize, i)
		if len(key) != sha256.Size*2 {
			t.Fatalf("invalid testcase- key length should be %d, not %d: %s",
				sha256.Size*2, len(key), key)
		}

		err := testCache.Put(cache.AC, key, int64(i),
			ioutil.NopCloser(strReader))
		if err != nil {
			t.Fatal(err)
		}

		err = checkItems(testCache, thisExp.expSize, thisExp.expNum)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCachePutWrongSize(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache, err := New(cacheDir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}

	content := "hello"
	hash := hashStr(content)

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

func TestCacheGetContainsWrongSize(t *testing.T) {

	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache, err := New(cacheDir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	var rdr io.ReadCloser

	err = testCache.Put(cache.CAS, contentsHash, contentsLength, strings.NewReader(contents))
	if err != nil {
		t.Fatal("Expected success", err)
	}

	found, _ = testCache.Contains(cache.CAS, contentsHash, contentsLength+1)
	if found {
		t.Error("Expected not found, due to size being different")
	}

	rdr, _, _ = testCache.Get(cache.CAS, contentsHash, contentsLength+1)
	if rdr != nil {
		t.Error("Expected not found, due to size being different")
	}

	found, _ = testCache.Contains(cache.CAS, contentsHash, -1)
	if !found {
		t.Error("Expected found, when unknown size")
	}

	rdr, _, _ = testCache.Get(cache.CAS, contentsHash, -1)
	if rdr == nil {
		t.Error("Expected found, when unknown size")
	}
}

func TestCacheGetContainsWrongSizeWithProxy(t *testing.T) {

	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache, err := New(cacheDir, 100, new(proxyStub))
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	var rdr io.ReadCloser

	// The proxyStub contains the digest {contentsHash, contentsLength}.

	found, _ = testCache.Contains(cache.CAS, contentsHash, contentsLength+1)
	if found {
		t.Error("Expected not found, due to size being different")
	}

	rdr, _, _ = testCache.Get(cache.CAS, contentsHash, contentsLength+1)
	if rdr != nil {
		t.Error("Expected not found, due to size being different")
	}
	if err := checkItems(testCache, 0, 0); err != nil {
		t.Fatal(err)
	}

	found, _ = testCache.Contains(cache.CAS, contentsHash, -1)
	if !found {
		t.Error("Expected found, when unknown size")
	}

	rdr, _, _ = testCache.Get(cache.CAS, contentsHash, -1)
	if rdr == nil {
		t.Error("Expected found, when unknown size")
	}
	if err := checkItems(testCache, contentsLength, 1); err != nil {
		t.Fatal(err)
	}
}

// proxyStub implements the cache.Proxy interface for a single blob with
// digest {contentsHash, contentsLength}.
type proxyStub struct{}

func (d proxyStub) Put(kind cache.EntryKind, hash string, size int64, rc io.ReadCloser) {}

func (d proxyStub) Get(kind cache.EntryKind, hash string) (io.ReadCloser, int64, error) {
	if hash != contentsHash {
		return nil, -1, nil
	}

	return ioutil.NopCloser(strings.NewReader(contents)), contentsLength, nil
}

func (d proxyStub) Contains(kind cache.EntryKind, hash string) (bool, int64) {
	if hash != contentsHash {
		return false, -1
	}

	return true, contentsLength
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

func putGetCompare(kind cache.EntryKind, hash string, content string, testCache *Cache) error {
	return putGetCompareBytes(kind, hash, []byte(content), testCache)
}

func putGetCompareBytes(kind cache.EntryKind, hash string, data []byte, testCache *Cache) error {

	r := bytes.NewReader(data)

	err := testCache.Put(kind, hash, int64(len(data)), r)
	if err != nil {
		return err
	}

	rdr, sizeBytes, err := testCache.Get(kind, hash, int64(len(data)))
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
	testCache, err := New(cacheDir, 10, nil)
	if err != nil {
		t.Fatal(err)
	}

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

func TestCacheExistingFiles(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)

	ensureDirExists(filepath.Join(cacheDir, "cas", "f5"))
	ensureDirExists(filepath.Join(cacheDir, "cas", "fd"))
	ensureDirExists(filepath.Join(cacheDir, "ac", "73"))
	ensureDirExists(filepath.Join(cacheDir, "raw", "73"))

	items := []string{
		"cas/f5/f53b46209596d170f7659a414c9ff9f6b545cf77ffd6e1cbe9bcc57e1afacfbd",
		"cas/fd/fdce205a735f407ae2910426611893d99ed985e3d9a341820283ea0b7d046ee3",
		"ac/73/733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
		"raw/73/733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
	}

	for _, it := range items {
		err := ioutil.WriteFile(filepath.Join(cacheDir, it), []byte(contents), os.ModePerm)
		if err != nil {
			t.Fatal(err)
		}
		// Wait a bit to account for atime granularity
		time.Sleep(10 * time.Millisecond)
	}

	const expectedSize = 4 * int64(len(contents))
	testCache, err := New(cacheDir, expectedSize, nil)
	if err != nil {
		t.Fatal(err)
	}

	err = checkItems(testCache, expectedSize, 4)
	if err != nil {
		t.Fatal(err)
	}

	// Adding a new file should evict items[0] (the oldest)
	err = testCache.Put(cache.CAS, contentsHash, int64(len(contents)), strings.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}

	err = checkItems(testCache, expectedSize, 4)
	if err != nil {
		t.Fatal(err)
	}
	found, _ := testCache.Contains(cache.CAS, "f53b46209596d170f7659a414c9ff9f6b545cf77ffd6e1cbe9bcc57e1afacfbd", contentsLength)
	if found {
		t.Fatalf("%s should have been evicted", items[0])
	}
}

// Make sure that the cache returns http.StatusInsufficientStorage when trying to upload an item
// that's bigger than the maximum size.
func TestCacheBlobTooLarge(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)
	testCache, err := New(cacheDir, 100, nil)
	if err != nil {
		t.Fatal(err)
	}

	for k := range []cache.EntryKind{cache.AC, cache.RAW} {
		kind := cache.EntryKind(k)
		err := testCache.Put(kind, hashStr("foo"), 10000, strings.NewReader(contents))
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
	testCache, err := New(cacheDir, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}

	err = testCache.Put(cache.CAS, hashStr("foo"), int64(len(contents)),
		strings.NewReader(contents))
	if err == nil {
		t.Fatal("expected hash mismatch error")
	}

	// We expect the upload to succeed without validation:
	err = testCache.Put(cache.RAW, hashStr("foo"), int64(len(contents)),
		strings.NewReader(contents))
	if err != nil {
		t.Fatal(err)
	}
}

// Create a random file of a certain size in the given directory, and
// return its hash.
func createRandomFile(dir string, size int64) (string, error) {
	data, hash := testutils.RandomDataAndHash(size)
	os.MkdirAll(dir, os.ModePerm)
	filepath := dir + "/" + hash

	return hash, ioutil.WriteFile(filepath, data, os.ModePerm)
}

func TestMigrateFromOldDirectoryStructure(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	acHash, err := createRandomFile(cacheDir+"/ac/", 512)
	if err != nil {
		t.Fatal(err)
	}
	casHash1, err := createRandomFile(cacheDir+"/cas/", 1024)
	if err != nil {
		t.Fatal(err)
	}
	casHash2, err := createRandomFile(cacheDir+"/cas/", 1024)
	if err != nil {
		t.Fatal(err)
	}
	testCache, err := New(cacheDir, 2560, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, _, numItems := testCache.Stats()
	if numItems != 3 {
		t.Fatalf("Expected test cache size 3 but was %d", numItems)
	}

	var found bool
	found, _ = testCache.Contains(cache.AC, acHash, 512)
	if !found {
		t.Fatalf("Expected cache to contain AC entry '%s'", acHash)
	}

	found, _ = testCache.Contains(cache.CAS, casHash1, 1024)
	if !found {
		t.Fatalf("Expected cache to contain CAS entry '%s'", casHash1)
	}

	found, _ = testCache.Contains(cache.CAS, casHash2, 1024)
	if !found {
		t.Fatalf("Expected cache to contain CAS entry '%s'", casHash2)
	}
}

func TestLoadExistingEntries(t *testing.T) {
	// Test that loading existing items works
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	numBlobs := int64(3)
	blobSize := int64(1024)

	acHash, err := testutils.CreateCacheFile(cacheDir+"/ac/", blobSize)
	if err != nil {
		t.Fatal(err)
	}
	casHash, err := testutils.CreateCacheFile(cacheDir+"/cas/", blobSize)
	if err != nil {
		t.Fatal(err)
	}
	rawHash, err := testutils.CreateCacheFile(cacheDir+"/raw/", blobSize)
	if err != nil {
		t.Fatal(err)
	}

	testCache, err := New(cacheDir, blobSize*numBlobs, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, _, numItems := testCache.Stats()
	if int64(numItems) != numBlobs {
		t.Fatalf("Expected test cache size %d but was %d",
			numBlobs, numItems)
	}

	var found bool

	found, _ = testCache.Contains(cache.AC, acHash, blobSize)
	if !found {
		t.Fatalf("Expected cache to contain AC entry '%s'", acHash)
	}

	found, _ = testCache.Contains(cache.CAS, casHash, blobSize)
	if !found {
		t.Fatalf("Expected cache to contain CAS entry '%s'", casHash)
	}

	found, _ = testCache.Contains(cache.RAW, rawHash, blobSize)
	if !found {
		t.Fatalf("Expected cache to contain RAW entry '%s'", rawHash)
	}
}

func TestDistinctKeyspaces(t *testing.T) {
	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	blobSize := 1024
	cacheSize := int64(blobSize * 3)

	testCache, err := New(cacheDir, cacheSize, nil)
	if err != nil {
		t.Fatal(err)
	}

	blob, casHash := testutils.RandomDataAndHash(1024)

	// Add the same blob with the same key, to each of the three
	// keyspaces, and verify that we have exactly three items in
	// the cache.

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

	_, _, numItems := testCache.Stats()
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

	proxy := httpproxy.New(url, &http.Client{}, accessLogger, errorLogger, 100, 1000000)

	cacheDir := testutils.TempDir(t)
	defer os.RemoveAll(cacheDir)

	cacheSize := int64(1024 * 10)

	testCache, err := New(cacheDir, cacheSize, proxy)
	if err != nil {
		t.Fatal(err)
	}

	blobSize := int64(1024)
	blob, casHash := testutils.RandomDataAndHash(blobSize)

	// Non-existing item
	r, _, err := testCache.Get(cache.CAS, casHash, blobSize)
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
	testCache, err = New(cacheDir, cacheSize, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Confirm that it does not contain the item we added to the
	// first testCache and the proxy backend.

	found, _ := testCache.Contains(cache.CAS, casHash, blobSize)
	if found {
		t.Fatalf("Expected the cache not to contain %s", casHash)
	}

	r, _, err = testCache.Get(cache.CAS, casHash, blobSize)
	if err != nil {
		t.Fatal(err)
	}
	if r != nil {
		t.Fatal("Expected testCache to be empty")
	}

	// Add the proxy backend and check that we can Get the item.
	testCache.proxy = proxy

	found, _ = testCache.Contains(cache.CAS, casHash, blobSize)
	if !found {
		t.Fatalf("Expected the cache to contain %s (via the proxy)",
			casHash)
	}

	r, fetchedSize, err := testCache.Get(cache.CAS, casHash, blobSize)
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

	testCache, err := New(cacheDir, 1024*32, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Create a directory tree like so:
	// /bar/foo.txt
	// /bar/grok.txt

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
