package operator

import (
	"sync"
	"time"
)

// Clock is the time source reconcilers and the cert issuer share.
// Tests substitute FakeClock.
type Clock interface {
	Now() time.Time
}

// RealClock is time.Now.
type RealClock struct{}

// Now returns the current time.
func (RealClock) Now() time.Time { return time.Now() }

// FakeClock is a mutex-guarded instant tests advance by hand.
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

// NewFakeClock returns a clock frozen at t.
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{t: t}
}

// Now returns the frozen instant.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock forward.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}
