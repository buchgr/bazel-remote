package casblob_test

import (
	"testing"
	"unsafe"
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
