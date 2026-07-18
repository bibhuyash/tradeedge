package replay

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrInvalidReplayState = errors.New("invalid replay state transition")

type State string

const (
	StateReady     State = "READY"
	StateRunning   State = "RUNNING"
	StatePaused    State = "PAUSED"
	StateCompleted State = "COMPLETED"
	StateCancelled State = "CANCELLED"
	StateFailed    State = "FAILED"
)

type Controller struct {
	mu          sync.Mutex
	clock       Clock
	state       State
	changed     chan struct{}
	pauseStart  time.Time
	totalPaused time.Duration
}

func NewController(clock Clock) *Controller {
	return &Controller{clock: clock, state: StateReady, changed: make(chan struct{})}
}

func (c *Controller) Pause() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateRunning {
		return ErrInvalidReplayState
	}
	c.state = StatePaused
	c.pauseStart = c.clock.Now()
	c.broadcast()
	return nil
}

func (c *Controller) Resume() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StatePaused {
		return ErrInvalidReplayState
	}
	c.totalPaused += c.clock.Now().Sub(c.pauseStart)
	c.pauseStart = time.Time{}
	c.state = StateRunning
	c.broadcast()
	return nil
}

func (c *Controller) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *Controller) TotalPaused() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := c.totalPaused
	if c.state == StatePaused {
		total += c.clock.Now().Sub(c.pauseStart)
	}
	return total
}

func (c *Controller) start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state != StateReady {
		return ErrInvalidReplayState
	}
	c.state = StateRunning
	c.broadcast()
	return nil
}

func (c *Controller) finish(state State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = state
	c.broadcast()
}

func (c *Controller) waitRunning(ctx context.Context) error {
	for {
		c.mu.Lock()
		if c.state != StatePaused {
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

func (c *Controller) broadcast() {
	close(c.changed)
	c.changed = make(chan struct{})
}
