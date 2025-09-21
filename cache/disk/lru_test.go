package disk

import (
	"math"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	testutils "github.com/buchgr/bazel-remote/v2/utils"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
)

func checkSizeAndNumItems(t *testing.T, lru *SizedLRU, expSize int64, expNum int) {
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

	_, listElem := lru.Get("1")
	if listElem != nil {
		t.Fatalf("Get: unexpected element found")
	}

	checkSizeAndNumItems(t, &lru, 0, 0)

	// Add an item
	aKey := "akey"
	anItem := lruItem{size: 5, sizeOnDisk: 5}
	ok := lru.Add(aKey, anItem)
	if !ok {
		t.Fatalf("Add: failed inserting item")
	}

	getItem, listElem := lru.Get(aKey)
	if listElem == nil {
		t.Fatalf("Get: failed getting item")
	}
	if getItem.size != anItem.size {
		t.Fatalf("Get: got a different item back")
	}

	checkSizeAndNumItems(t, &lru, BlockSize, 1)

	// Remove the item
	lru.RemoveKey(aKey)
	checkSizeAndNumItems(t, &lru, 0, 0)
}

func TestEviction(t *testing.T) {
	// Keep track of evictions using the callback
	var evictions []string
	onEvict := func(key string, value lruItem) {
		evictions = append(evictions, key)
	}

	lru := NewSizedLRU(10*BlockSize, onEvict, 0)

	expectedSizesNumItems := []struct {
		expBlocks   int64
		expNumItems int
		expEvicted  []string
	}{
		{0, 1, []string{}},                   // 0
		{1, 2, []string{}},                   // 0, 1
		{3, 3, []string{}},                   // 0, 1, 2
		{6, 4, []string{}},                   // 0, 1, 2, 3
		{10, 5, []string{}},                  // 0, 1, 2, 3, 4
		{9, 2, []string{"0", "1", "2", "3"}}, // 4, 5
		{6, 1, []string{"4", "5"}},           // 6
		{7, 1, []string{"6"}},                // 7
	}

	var expectedEvictions []string

	for i, thisExpected := range expectedSizesNumItems {
		item := lruItem{size: int64(i) * BlockSize, sizeOnDisk: int64(i) * BlockSize}
		ok := lru.Add(strconv.Itoa(i), item)
		if !ok {
			t.Fatalf("Add: failed adding %d", i)
		}
		if len(lru.queuedEvictionsChan) > 0 {
			lru.performQueuedEvictions()
		}
		checkSizeAndNumItems(t, &lru, thisExpected.expBlocks*BlockSize, thisExpected.expNumItems)

		expectedEvictions = append(expectedEvictions, thisExpected.expEvicted...)
		if !reflect.DeepEqual(expectedEvictions, evictions) {
			t.Fatalf("Expecting evictions %v, found %v", expectedEvictions, evictions)
		}
	}
}

func TestRejectBigItem(t *testing.T) {
	// Bounded caches should reject big items
	lru := NewSizedLRU(10, nil, 0)

	ok := lru.Add("hello", lruItem{size: 11, sizeOnDisk: 11})
	if ok {
		t.Fatalf("Add succeeded, expected it to fail")
	}

	checkSizeAndNumItems(t, &lru, 0, 0)
}

func TestReserveZeroAlwaysPossible(t *testing.T) {
	largeItem := lruItem{size: math.MaxInt64, sizeOnDisk: math.MaxInt64}

	lru := NewSizedLRU(math.MaxInt64, nil, 0)
	lru.Add("foo", largeItem)
	err := lru.Reserve(0)
	if err != nil {
		t.Fatalf("Should always be able to reserve 0, but got: %v", err)
	}
}

func TestReserveAtCapacity(t *testing.T) {
	var err error

	lru := NewSizedLRU(math.MaxInt64, nil, 0)

	err = lru.Reserve(math.MaxInt64)
	if err != nil {
		t.Fatalf("Should be able to reserve all the space, but got: %v", err)
	}
	if lru.TotalSize() != math.MaxInt64 {
		t.Fatalf("Expected total size %d, actual size %d", math.MaxInt64,
			lru.TotalSize())
	}

	err = lru.Reserve(0)
	if err != nil {
		t.Fatalf("Should always be able to reserve 0, but got: %v", err)
	}
	if lru.TotalSize() != math.MaxInt64 {
		t.Fatalf("Expected total size %d, actual size %d", math.MaxInt64,
			lru.TotalSize())
	}

	err = lru.Reserve(1)
	if err == nil {
		t.Fatal("Should not be able to reserve any space")
	}
	if lru.TotalSize() != math.MaxInt64 {
		t.Fatalf("Expected total size %d, actual size %d", math.MaxInt64,
			lru.TotalSize())
	}
}

func TestReserveAtEvictionQueueLimit(t *testing.T) {

	lru := NewSizedLRU(BlockSize*2, func(string, lruItem) {}, 0)
	lru.maxSizeHardLimit = BlockSize * 3

	blockSize1Key := "7777"
	blockSize1Item := lruItem{size: BlockSize * 1, sizeOnDisk: BlockSize * 1}

	blockSize2Key := "8888"
	blockSize2Item := lruItem{size: BlockSize * 2, sizeOnDisk: BlockSize * 2}

	// Add large item.
	testutils.AssertSuccess(t, lru.Add(blockSize2Key, blockSize2Item))
	testutils.AssertEquals(t, BlockSize*2, lru.totalDiskSizePeak)

	// Move large item into eviction queue.
	lru.RemoveKey(blockSize2Key)
	testutils.AssertEquals(t, BlockSize*2, lru.queuedEvictionsSize.Load())

	// Accept reservation since not exceeding maxSizeHardLimit.
	testutils.AssertSuccess(t, lru.Reserve(BlockSize))
	testutils.AssertEquals(t, BlockSize*2, lru.queuedEvictionsSize.Load())
	testutils.AssertEquals(t, BlockSize*3, lru.totalDiskSizePeak)

	// Reject reservation when the item + reserved + queued > maxSizeHardLimit.
	testutils.AssertFailureWithCode(t, lru.Reserve(BlockSize), http.StatusInsufficientStorage)
	testutils.AssertEquals(t, BlockSize*4, lru.totalDiskSizePeak) // Includes rejected item.

	// Convert reservation into added item.
	testutils.AssertSuccess(t, lru.Unreserve(BlockSize))
	testutils.AssertSuccess(t, lru.Add(blockSize1Key, blockSize1Item))

	// Reject reservation when the item + added + queued > maxSizeHardLimit.
	testutils.AssertFailureWithCode(t, lru.Reserve(BlockSize), http.StatusInsufficientStorage)
	testutils.AssertEquals(t, BlockSize*4, lru.totalDiskSizePeak) // Includes rejected item.

	// Complete queued evictions
	lru.performQueuedEvictions()
	testutils.AssertEquals(t, BlockSize*0, lru.queuedEvictionsSize.Load())
	testutils.AssertEquals(t, BlockSize*4, lru.totalDiskSizePeak) // Not reset until next period.

	// Accept reservation since more space is available after completed evictions.
	testutils.AssertSuccess(t, lru.Reserve(BlockSize))
	testutils.AssertEquals(t, BlockSize*4, lru.totalDiskSizePeak)
}

func TestPeriodicMetricUpdate(t *testing.T) {

	lru := NewSizedLRU(BlockSize*10, nil, 0)

	// Reserve so that peak become 5 + 2 = 7
	testutils.AssertSuccess(t, lru.Reserve(BlockSize*5))
	testutils.AssertSuccess(t, lru.Reserve(BlockSize*2))
	testutils.AssertEquals(t, BlockSize*7, lru.totalDiskSizePeak)

	// Peak remains the same while in same period even after unreserve.
	testutils.AssertSuccess(t, lru.Unreserve(BlockSize*2))
	testutils.AssertEquals(t, BlockSize*7, lru.totalDiskSizePeak)

	lru.shiftToNextMetricPeriod()
	testutils.AssertEquals(t, BlockSize*5, lru.totalDiskSizePeak)
	testutils.AssertEquals(t, BlockSize*7, promtestutil.ToFloat64(lru.gaugeCacheSizeBytes))

	lru.shiftToNextMetricPeriod()
	testutils.AssertEquals(t, BlockSize*5, lru.totalDiskSizePeak)
	testutils.AssertEquals(t, BlockSize*5, promtestutil.ToFloat64(lru.gaugeCacheSizeBytes))
}

func TestReserveOverflow(t *testing.T) {
	var lru SizedLRU
	var err error

	lru = NewSizedLRU(1, nil, 0)

	err = lru.Reserve(1)
	if err != nil {
		t.Fatalf("Expected to be able to reserve 1, but got: %v", err)
	}

	err = lru.Reserve(math.MaxInt64)
	if err == nil {
		t.Fatal("Expected overflow")
	}

	lru = NewSizedLRU(10, nil, 0)
	err = lru.Reserve(math.MaxInt64)
	if err == nil {
		t.Fatal("Expected overflow")
	}
}

func TestUnreserve(t *testing.T) {
	var err error

	cap := int64(10)
	lru := NewSizedLRU(cap, nil, 0)

	for i := int64(1); i <= cap; i++ {
		err = lru.Reserve(1)
		if err != nil {
			t.Fatalf("Expected to be able to reserve 1, but got: %v", err)
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

	err := lru.Reserve(1)
	if err != nil {
		t.Fatalf("Expected to be able to reserve 1, but got: %v", err)
	}

	ok := lru.Add("hello", lruItem{size: 2, sizeOnDisk: 2})
	if ok {
		t.Fatal("Expected to not be able to add item with size 2")
	}

	err = lru.Unreserve(1)
	if err != nil {
		t.Fatal("Expected to be able to unreserve 1:", err)
	}

	ok = lru.Add("hello", lruItem{size: 2, sizeOnDisk: 2})
	if !ok {
		t.Fatal("Expected to be able to add item with size 2")
	}
}
