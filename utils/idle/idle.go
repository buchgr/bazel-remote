package idle

import (
	"sync"
	"time"
)

// Timer keeps track of the request activity and notifies
// registered channels once an idle timeout has been reached.
type Timer struct {
	mu          sync.Mutex
	timeout     time.Duration
	notify      chan struct{}
	lastRequest time.Time
}

// NewTimer creates a new Timer that will send notifications on
// any registered channels once the idle timeout has been reached.
func NewTimer(timeout time.Duration, notificationChan chan struct{}) *Timer {
	return &Timer{
		timeout:     timeout,
		lastRequest: time.Now(),
		notify:      notificationChan,
	}
}

// Start begins the idle timer, and returns immediately.
func (t *Timer) Start() {
	go t.start()
}

func (t *Timer) start() {
	var elapsed time.Duration
	ticker := time.NewTicker(time.Second)

	for now := range ticker.C {
		t.mu.Lock()
		elapsed = now.Sub(t.lastRequest)
		t.mu.Unlock()

		if elapsed > t.timeout {
			ticker.Stop()
			t.notify <- struct{}{}
			return
		}
	}
}

// ResetTimer resets the idle timer countdown. It should be called once
// at the start of every new request.
func (t *Timer) ResetTimer() {
	now := time.Now()
	t.mu.Lock()
	t.lastRequest = now
	t.mu.Unlock()
}
