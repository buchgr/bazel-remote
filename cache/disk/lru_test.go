package disk

import (
	"math"
	"reflect"
	"testing"
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
	ok = lru.Add(aKey, anItem)
	if !ok {
		t.Fatalf("Add: failed inserting item")
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
	lru.Remove(aKey)
	checkSizeAndNumItems(t, lru, 0, 0)
}

func TestEviction(t *testing.T) {
	// Keep track of evictions using the callback
	var evictions []int
	onEvict := func(key Key, value lruItem) {
		evictions = append(evictions, key.(int))
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
		ok := lru.Add(i, item)
		if !ok {
			t.Fatalf("Add: failed adding %d", i)
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

	ok := lru.Add("hello", lruItem{size: 11, sizeOnDisk: 11})
	if ok {
		t.Fatalf("Add succeeded, expected it to fail")
	}

	checkSizeAndNumItems(t, lru, 0, 0)
}

func TestReserveZeroAlwaysPossible(t *testing.T) {
	largeItem := lruItem{size: math.MaxInt64, sizeOnDisk: math.MaxInt64}

	lru := NewSizedLRU(math.MaxInt64, nil, 0)
	lru.Add("foo", largeItem)
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
	if err != nil {
		t.Fatal(err)
	}
	if ok {
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
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("Expected overflow")
	}

	lru = NewSizedLRU(10, nil, 0)
	ok, err = lru.Reserve(math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
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

	ok = lru.Add("hello", lruItem{size: 2, sizeOnDisk: 2})
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
