package disk

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	testutils "github.com/buchgr/bazel-remote/v2/utils"
	"github.com/buchgr/bazel-remote/v2/utils/tempfile"
)

func checkSizeAndNumItems(t *testing.T, lru SizedLRU, expSize int64, expNum int) {
	currentSize := lru.TotalSize()
	if currentSize != expSize {
		t.Fatalf("TotalSize: expected %d, got %d", expSize, currentSize)
	}

	numItems := lru.Len()
	if numItems != expNum {
		t.Fatalf("Len: expected %d, got %d", expNum, numItems)
	}
}

func TestBasics(t *testing.T) {
	maxSize := int64(BlockSize)
	lru := NewSizedLRU(maxSize, nil, 0)

	// Empty cache
	if maxSize != lru.MaxSize() {
		t.Fatalf("MaxSize: expected %d, got %d", maxSize, lru.MaxSize())
	}

	_, ok := lru.Get("1")
	if ok {
		t.Fatalf("Get: unexpected element found")
	}

	checkSizeAndNumItems(t, lru, 0, 0)

	// Add an item
	aKey := "akey"
	anItem := lruItem{size: 5, sizeOnDisk: 5}
	err := lru.Add(aKey, anItem)
	if err != nil {
		t.Fatalf("Add: failed inserting item: %s", err)
	}

	getItem, getOk := lru.Get(aKey)
	if !getOk {
		t.Fatalf("Get: failed getting item")
	}
	if getItem.size != anItem.size {
		t.Fatalf("Get: got a different item back")
	}

	checkSizeAndNumItems(t, lru, BlockSize, 1)

	// Remove the item
	err = lru.Remove(aKey)
	if err != nil {
		t.Fatal(err)
	}
	checkSizeAndNumItems(t, lru, 0, 0)
}

func TestEviction(t *testing.T) {
	// Keep track of evictions using the callback
	var evictions []int
	onEvict := func(key Key, value lruItem) error {
		evictions = append(evictions, key.(int))
		return nil
	}

	lru := NewSizedLRU(10*BlockSize, onEvict, 0)

	expectedSizesNumItems := []struct {
		expBlocks   int64
		expNumItems int
		expEvicted  []int
	}{
		{0, 1, []int{}},           // 0
		{1, 2, []int{}},           // 0, 1
		{3, 3, []int{}},           // 0, 1, 2
		{6, 4, []int{}},           // 0, 1, 2, 3
		{10, 5, []int{}},          // 0, 1, 2, 3, 4
		{9, 2, []int{0, 1, 2, 3}}, // 4, 5
		{6, 1, []int{4, 5}},       // 6
		{7, 1, []int{6}},          // 7
	}

	var expectedEvictions []int

	for i, thisExpected := range expectedSizesNumItems {
		item := lruItem{size: int64(i) * BlockSize, sizeOnDisk: int64(i) * BlockSize}
		err := lru.Add(i, item)
		if err != nil {
			t.Fatalf("Add: failed adding %d: %s", i, err)
		}

		checkSizeAndNumItems(t, lru, thisExpected.expBlocks*BlockSize, thisExpected.expNumItems)

		expectedEvictions = append(expectedEvictions, thisExpected.expEvicted...)
		if !reflect.DeepEqual(expectedEvictions, evictions) {
			t.Fatalf("Expecting evictions %v, found %v", expectedEvictions, evictions)
		}
	}
}

func TestRejectBigItem(t *testing.T) {
	// Bounded caches should reject big items
	lru := NewSizedLRU(10, nil, 0)

	err := lru.Add("hello", lruItem{size: 11, sizeOnDisk: 11})
	if err == nil {
		t.Fatalf("Add succeeded, expected it to fail")
	}

	checkSizeAndNumItems(t, lru, 0, 0)
}

func TestReserveZeroAlwaysPossible(t *testing.T) {
	largeItem := lruItem{size: math.MaxInt64, sizeOnDisk: math.MaxInt64}

	lru := NewSizedLRU(math.MaxInt64, nil, 0)
	err := lru.Add("foo", largeItem)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := lru.Reserve(0)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Should always be able to reserve 0")
	}
}

func TestReserveAtCapacity(t *testing.T) {
	var ok bool
	var err error

	lru := NewSizedLRU(math.MaxInt64, nil, 0)

	ok, err = lru.Reserve(math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Should be able to reserve all the space")
	}
	if lru.TotalSize() != math.MaxInt64 {
		t.Fatalf("Expected total size %d, actual size %d", math.MaxInt64,
			lru.TotalSize())
	}

	ok, err = lru.Reserve(0)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Should always be able to reserve 0")
	}
	if lru.TotalSize() != math.MaxInt64 {
		t.Fatalf("Expected total size %d, actual size %d", math.MaxInt64,
			lru.TotalSize())
	}

	ok, err = lru.Reserve(1)
	if ok || err == nil {
		t.Fatal("Should not be able to reserve any space")
	}
	if lru.TotalSize() != math.MaxInt64 {
		t.Fatalf("Expected total size %d, actual size %d", math.MaxInt64,
			lru.TotalSize())
	}
}

func TestReserveOverflow(t *testing.T) {
	var lru SizedLRU
	var ok bool
	var err error

	lru = NewSizedLRU(1, nil, 0)

	ok, err = lru.Reserve(1)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("Expected to be able to reserve 1")
	}

	ok, err = lru.Reserve(math.MaxInt64)
	if ok || err == nil {
		t.Fatal("Expected overflow")
	}

	lru = NewSizedLRU(10, nil, 0)
	ok, err = lru.Reserve(math.MaxInt64)
	if ok || err == nil {
		t.Fatal("Expected overflow")
	}
}

func TestUnreserve(t *testing.T) {
	var ok bool
	var err error

	cap := int64(10)
	lru := NewSizedLRU(cap, nil, 0)

	for i := int64(1); i <= cap; i++ {
		ok, err = lru.Reserve(1)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatal("Expected to be able to reserve 1")
		}
		if lru.TotalSize() != i {
			t.Fatalf("Expected total size %d, actual size %d", i,
				lru.TotalSize())
		}
	}

	if lru.TotalSize() != cap {
		t.Fatalf("Expected total size %d, actual size %d", cap,
			lru.TotalSize())
	}

	for i := cap; i > 0; i-- {
		err = lru.Unreserve(1)
		if err != nil {
			t.Fatal("Expected to be able to unreserve 1:", err)
		}
		if lru.TotalSize() != i-1 {
			t.Fatalf("Expected total size %d, actual size %d", i-1,
				lru.TotalSize())
		}
	}

	if lru.TotalSize() != 0 {
		t.Fatalf("Expected total size 0, actual size %d",
			lru.TotalSize())
	}
}

func TestAddWithSpaceReserved(t *testing.T) {
	lru := NewSizedLRU(roundUp4k(2), nil, 0)

	ok, err := lru.Reserve(1)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("Expected to be able to reserve 1")
	}

	err = lru.Add("hello", lruItem{size: 2, sizeOnDisk: 2})
	if err == nil {
		t.Fatal("Expected to not be able to add item with size 2")
	}

	err = lru.Unreserve(1)
	if err != nil {
		t.Fatal("Expected to be able to unreserve 1:", err)
	}

	err = lru.Add("hello", lruItem{size: 2, sizeOnDisk: 2})
	if err != nil {
		t.Fatalf("Expected to be able to add item with size 2: %s", err)
	}
}

func BenchmarkEvictions(b *testing.B) {
	readSliceFromEnv := func(env string, def []int) []int {
		if v := os.Getenv(env); v != "" {
			strs := strings.Split(v, ",")
			values := make([]int, len(strs))
			for i, s := range strs {
				size, err := strconv.Atoi(s)
				if err != nil {
					b.Fatal(err)
				}
				values[i] = size
			}
			return values
		}
		return def
	}

	sizes := readSliceFromEnv("EVICTION_BENCHMARK_SIZE", []int{4 * 1024})
	concurrencies := readSliceFromEnv("EVICTION_BENCHMARK_CONCURRENCY", []int{1, 2, 4, 8, 10, 100, 1000})
	filecount := readSliceFromEnv("EVICTION_BENCHMARK_FILECOUNT", []int{1000000})[0]

	creator := tempfile.NewCreator()
	logger := testutils.NewSilentLogger()
	basedir := b.TempDir()

	b.Cleanup(func() { os.RemoveAll(basedir) })

	type FileEntry struct {
		size  int
		dir   string
		data  []byte
		wg    *sync.WaitGroup
		index int
	}
	errCh := make(chan error, filecount)
	ch := make(chan FileEntry)
	for i := 0; i < readSliceFromEnv("EVICTION_BENCHMARK_POOL_SIZE", []int{1})[0]; i++ {
		go func() {
			for e := range ch {
				func() {
					defer e.wg.Done()
					hash32 := sha256.Sum256([]byte(fmt.Sprintf("%d", e.index)))
					hash := hex.EncodeToString(hash32[:])
					prefix := fmt.Sprintf("%s/cas.v2/%s", e.dir, hash[:2])
					err := os.MkdirAll(prefix, os.ModePerm)
					if err != nil {
						errCh <- err
						return
					}
					base := fmt.Sprintf("%s/%s", prefix, hash)
					f, _, err := creator.Create(base, true)
					if err != nil {
						errCh <- err
						return
					}
					defer f.Close()
					_, err = f.Write(e.data)
					if err != nil {
						errCh <- err
						return
					}
					err = f.Close()
					if err != nil {
						errCh <- err
					}
					if err != nil {
						errCh <- err
					}
				}()
			}
		}()
	}

	for _, size := range sizes {
		data := make([]byte, size)
		_, err := rand.Read(data)
		if err != nil {
			b.Fatal(err)
		}
		for _, concurrency := range concurrencies {
			maxSize := filecount * size / 10
			for n := 0; n < b.N; n++ {
				dir := fmt.Sprintf("%s/%d/%d/%d", basedir, concurrency, size, n)
				b.Run(fmt.Sprintf("Benchmark %d %d %d %d", concurrency, size, maxSize, n), func(b *testing.B) {
					b.StopTimer()
					wg := sync.WaitGroup{}
					for i := 0; i < filecount; i++ {
						wg.Add(1)
						ch <- FileEntry{
							data:  data,
							size:  size,
							wg:    &wg,
							dir:   dir,
							index: i,
						}
					}
					wg.Wait()
					select {
					case err = <-errCh:
						b.Fatal(err)
					default:
					}
					time.Sleep(1 * time.Nanosecond)

					b.StartTimer()
					_, err = New(dir,
						int64(maxSize),
						WithAccessLogger(logger),
						WithMaxQueuedEvictions(filecount),
						WithMaxConcurrentEvictions(concurrency))
					b.StopTimer()

					if err != nil {
						b.Fatal(err)
					}
				})
			}
		}
	}
}
