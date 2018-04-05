package cache

import (
	"fmt"
	"reflect"
	"testing"
)

type testSizedItem struct {
	s       int64
	payload string
}

func (it *testSizedItem) Size() int64 {
	return it.s
}

func checkSizeAndNumItems(t *testing.T, lru SizedLRU, expSize int64, expNum int) {
	currentSize := lru.CurrentSize()
	if currentSize != expSize {
		t.Fatalf("CurrentSize: expected %d, got %d", expSize, currentSize)
	}

	numItems := lru.Len()
	if numItems != expNum {
		t.Fatalf("Len: expected %d, got %d", expNum, numItems)
	}
}

func TestBasics(t *testing.T) {
	MAX_SIZE := int64(10)
	lru := NewSizedLRU(MAX_SIZE, nil)

	// Empty cache
	maxSize := lru.MaxSize()
	if maxSize != MAX_SIZE {
		t.Fatalf("MaxSize: expected %d, got %d", MAX_SIZE, maxSize)
	}

	_, ok := lru.Get("1")
	if ok {
		t.Fatalf("Get: unexpected element found")
	}

	checkSizeAndNumItems(t, lru, 0, 0)

	// Add an item
	A_KEY := "akey"
	AN_ITEM := testSizedItem{5, "hello"}
	ok = lru.Add(A_KEY, &AN_ITEM)
	if !ok {
		t.Fatalf("Add: failed inserting item")
	}

	getItem, getOk := lru.Get(A_KEY)
	if !getOk {
		t.Fatalf("Get: failed getting item")
	}
	if *getItem.(*testSizedItem) != AN_ITEM {
		t.Fatalf("Get: got a different item back")
	}

	checkSizeAndNumItems(t, lru, 5, 1)

	// Remove the item
	lru.Remove(A_KEY)
	checkSizeAndNumItems(t, lru, 0, 0)
}

func TestEviction(t *testing.T) {
	// Keep track of evictions using the callback
	var evictions []int
	onEvict := func(key Key, value SizedItem) {
		evictions = append(evictions, key.(int))
	}

	lru := NewSizedLRU(10, onEvict)

	expectedSizesNumItems := []struct {
		expSize     int64
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
		item := testSizedItem{int64(i), fmt.Sprintf("%d", i)}
		ok := lru.Add(i, &item)
		if !ok {
			t.Fatalf("Add: failed adding %d", i)
		}

		checkSizeAndNumItems(t, lru, thisExpected.expSize, thisExpected.expNumItems)

		expectedEvictions = append(expectedEvictions, thisExpected.expEvicted...)
		if !reflect.DeepEqual(expectedEvictions, evictions) {
			t.Fatalf("Expecting evictions %v, found %v", expectedEvictions, evictions)
		}
	}
}

func TestRejectBigItem(t *testing.T) {
	// Bounded caches should reject big items
	lru := NewSizedLRU(10, nil)

	ok := lru.Add("hello", &testSizedItem{11, "hello"})
	if ok {
		t.Fatalf("Add succeeded, expected it to fail")
	}

	checkSizeAndNumItems(t, lru, 0, 0)
}
