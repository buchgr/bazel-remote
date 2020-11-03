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
	"path"
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

const KEY = "a-key"
const contents = "hello"
const contentsHash = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
const contentsLength = int64(len(contents))

func TestCacheBasics(t *testing.T) {
	cacheDir := tempDir(t)
	defer os.RemoveAll(cacheDir)

	itemSize := int64(256)
	cacheSize := itemSize

	testCache, err := New(cacheDir, cacheSize, nil)
	if err != nil {
		t.Fatal(err)
	}

	if testCache.lru.Len() != 0 {
		t.Fatalf("Expected to start with an empty disk cache, found %d items",
			testCache.lru.Len())
	}

	data, hash := testutils.RandomDataAndHash(itemSize)

	// Non-existing item.
	rdr, _, err := testCache.Get(cache.CAS, hash, itemSize)
	if err != nil {
		t.Fatal(err)
	}
	if rdr != nil {
		t.Fatal("expected the item not to exist")
	}

	// Add an item.
	err = testCache.Put(cache.CAS, hash, itemSize,
		ioutil.NopCloser(bytes.NewReader(data)))
	if err != nil {
		t.Fatal(err)
	}

	// Get the item back.
	rdr, sizeBytes, err := testCache.Get(cache.CAS, hash, itemSize)
	if err != nil {
		t.Fatal(err)
	}

	err = expectContentEquals(rdr, sizeBytes, data)
	if err != nil {
		t.Fatal(err)
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

	if testCache.lru.Len() != 0 {
		t.Fatalf("Expected to start with an empty disk cache, found %d items",
			testCache.lru.Len())
	}

	found, _ = testCache.Contains(cache.CAS, contentsHash, contentsLength+1)
	if found {
		t.Fatal("Expected not found, due to size being different")
	}

	rdr, _, _ = testCache.Get(cache.CAS, contentsHash, contentsLength+1)
	if rdr != nil {
		t.Fatal("Expected not found, due to size being different")
	}

	if testCache.lru.Len() != 0 {
		t.Fatalf("Expected cache to be empty at this point, found %d items",
			testCache.lru.Len())
	}

	found, _ = testCache.Contains(cache.CAS, contentsHash, -1)
	if !found {
		t.Fatal("Expected found, when unknown size")
	}

	rdr, _, _ = testCache.Get(cache.CAS, contentsHash, -1)
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

	items := []struct {
		contents string
		hash     string
		file     string
	}{
		{
			"hej",
			"9c478bf63e9500cb5db1e85ece82f18c8eb9e52e2f9135acd7f10972c8d563ba",
			"cas/9c/9c478bf63e9500cb5db1e85ece82f18c8eb9e52e2f9135acd7f10972c8d563ba",
		},
		{
			"världen",
			"d497feaa39156f4ae61317db9d2adc3a8f2ff1437fd48ccb56f814f0b7ac5fe1",
			"cas/d4/d497feaa39156f4ae61317db9d2adc3a8f2ff1437fd48ccb56f814f0b7ac5fe1",
		},
		{
			"foo",
			"733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
			"ac/73/733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
		},
		{
			"bar",
			"733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
			"raw/73/733e21b37cef883579a88183eed0d00cdeea0b59e1bcd77db6957f881c3a6b54",
		},
	}

	var err error
	for _, it := range items {

		fp := path.Join(cacheDir, it.file)
		ensureDirExists(path.Dir(fp))

		err = ioutil.WriteFile(fp, []byte(it.contents), os.ModePerm)
		if err != nil {
			t.Fatal(err)
		}

		// Wait a bit to account for atime granularity
		time.Sleep(10 * time.Millisecond)
	}

	testCache, err := New(cacheDir, 1024, nil)
	if err != nil {
		t.Fatal(err)
	}

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
		data, hash := testutils.RandomDataAndHash(32)

		if items[0].hash == hash {
			// Add any item but this one, to ensure it will be evicted first.
			continue
		}

		err = testCache.Put(cache.CAS, hash, int64(len(data)),
			bytes.NewReader(data))
		if err != nil {
			t.Fatal("failed to Put CAS blob", hash, err)
		}

		if len(evicted) == 0 {
			// Nothing evicted yet.
			continue
		}

		if evicted[0] != items[0].file {
			t.Fatalf("Expected first evicted item to be %s, was %s",
				items[0].file, evicted[0])
		}

		break // First item evicted as expected.
	}

	found, _ := testCache.Contains(cache.CAS, items[0].hash, contentsLength)
	if found {
		t.Fatalf("%s should have been evicted", items[0].file)
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
