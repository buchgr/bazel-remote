package idle_test

import (
	"testing"
	"time"

	"github.com/buchgr/bazel-remote/v2/utils/idle"
)

func TestIdleTimer(t *testing.T) {
	tearDown := make(chan struct{})
	it := idle.NewTimer(time.Second, tearDown)
	it.Start()

	for i := 0; i < 5; i++ {
		select {
		case <-time.After(500 * time.Millisecond):
			it.ResetTimer()
		case <-tearDown:
			t.Fatal("unexpected timeout")
		}
	}

	select {
	case <-tearDown:
		return
	case <-time.After(2 * time.Second):
		t.Fatal("expected idle timer to trigger")
	}
}
