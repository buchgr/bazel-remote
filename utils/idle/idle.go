package idle

import (
	"sync"
	"time"
)

// IdleTimer keeps track of the request activity and notifies
// registered channels once an idle timeout has been reached.
type IdleTimer struct {
	mu          sync.Mutex
	timeout     time.Duration
	notify      []chan struct{}
	lastRequest time.Time
}

// NewTimer creates a new IdleTimer that will send notifications on
// any registered channels once the idle timeout has been reached.
func NewTimer(timeout time.Duration) *IdleTimer {
	return &IdleTimer{
		timeout:     timeout,
		lastRequest: time.Now(),
		notify:      make([]chan struct{}, 0),
	}
}

// Register adds a channel that will be notified once the idle
// timeout is reached.
func (t *IdleTimer) Register(c chan struct{}) {
	t.mu.Lock()
	t.notify = append(t.notify, c)
	t.mu.Unlock()
}

// Start begins the idle timer, and returns immediately.
func (t *IdleTimer) Start() {
	go t.start()
}

func (t *IdleTimer) start() {
	var elapsed time.Duration
	ticker := time.NewTicker(time.Second)

	for now := range ticker.C {
		t.mu.Lock()
		elapsed = now.Sub(t.lastRequest)
		t.mu.Unlock()

		if elapsed > t.timeout {
			ticker.Stop()
			for _, c := range t.notify {
				c <- struct{}{}
			}
			return
		}
	}
}

// ResetTimer resets the idle timer countdown. It should be called once
// at the start of every new request.
func (t *IdleTimer) ResetTimer() {
	now := time.Now()
	t.mu.Lock()
	t.lastRequest = now
	t.mu.Unlock()
}
