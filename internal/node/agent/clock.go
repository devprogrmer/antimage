package agent

import (
	"sync"
	"time"
)

// Clock exists so reconciliation timing is testable without sleeping.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time                         { return time.Now() }
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// FakeClock drives tests deterministically. Timers created by After fire when
// Advance passes their deadline.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	deadline time.Time
	ch       chan time.Time
}

func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{now: t} }

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.timers = append(c.timers, fakeTimer{deadline: c.now.Add(d), ch: ch})
	return ch
}

func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var remaining []fakeTimer
	for _, t := range c.timers {
		if !t.deadline.After(now) {
			t.ch <- now
			continue
		}
		remaining = append(remaining, t)
	}
	c.timers = remaining
	c.mu.Unlock()
}
