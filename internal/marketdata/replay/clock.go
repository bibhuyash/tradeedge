package replay

import (
	"context"
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
	WaitUntil(ctx context.Context, target time.Time) error
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) WaitUntil(ctx context.Context, target time.Time) error {
	delay := time.Until(target)
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type ManualClock struct {
	mu      sync.Mutex
	now     time.Time
	changed chan struct{}
}

func NewManualClock(now time.Time) *ManualClock {
	return &ManualClock{now: now, changed: make(chan struct{})}
}

func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ManualClock) WaitUntil(ctx context.Context, target time.Time) error {
	for {
		c.mu.Lock()
		if !c.now.Before(target) {
			c.mu.Unlock()
			return nil
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (c *ManualClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	close(c.changed)
	c.changed = make(chan struct{})
	c.mu.Unlock()
}

func (c *ManualClock) Set(now time.Time) {
	c.mu.Lock()
	c.now = now
	close(c.changed)
	c.changed = make(chan struct{})
	c.mu.Unlock()
}
