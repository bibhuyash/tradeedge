package replay

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/storage"
)

var (
	ErrInvalidRate   = errors.New("invalid replay rate")
	ErrReplayRunning = errors.New("replay engine is already running")
)

type Rate struct {
	EventsTime time.Duration
	WallTime   time.Duration
}

func MaximumRate() Rate  { return Rate{EventsTime: time.Second, WallTime: 0} }
func RealTimeRate() Rate { return Rate{EventsTime: time.Second, WallTime: time.Second} }

func (r Rate) Validate() error {
	if r.EventsTime <= 0 || r.WallTime < 0 {
		return ErrInvalidRate
	}
	return nil
}

type Request struct {
	Query storage.EventQuery
	Rate  Rate
}

type Metrics struct {
	Events            uint64
	ConsumerTime      time.Duration
	ScheduledWaitTime time.Duration
	PauseTime         time.Duration
	StartedAt         time.Time
	CompletedAt       time.Time
	TerminalState     State
}

type Engine struct {
	clock      Clock
	controller *Controller
	mu         sync.Mutex
	running    bool
	metrics    Metrics
}

func NewEngine(clock Clock, controller *Controller) *Engine {
	if controller == nil {
		controller = NewController(clock)
	}
	return &Engine{clock: clock, controller: controller}
}

func (e *Engine) Controller() *Controller { return e.controller }

func (e *Engine) Metrics() Metrics {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.metrics
}

func (e *Engine) Replay(
	ctx context.Context,
	reader storage.DatasetReader,
	request Request,
	sink storage.EventSink,
) (err error) {
	if err := request.Rate.Validate(); err != nil {
		return err
	}
	if reader == nil || sink == nil || e.clock == nil {
		return errors.New("replay reader, sink, and clock are required")
	}
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return ErrReplayRunning
	}
	e.running = true
	e.metrics = Metrics{StartedAt: e.clock.Now(), TerminalState: StateRunning}
	e.mu.Unlock()
	if err := e.controller.start(); err != nil {
		e.setFinished(StateFailed)
		return err
	}
	defer func() {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			e.controller.finish(StateCancelled)
			e.setFinished(StateCancelled)
		case err != nil:
			e.controller.finish(StateFailed)
			e.setFinished(StateFailed)
		default:
			e.controller.finish(StateCompleted)
			e.setFinished(StateCompleted)
		}
	}()

	var firstEventTime time.Time
	wallStart := e.clock.Now()
	err = reader.Scan(ctx, request.Query, func(ctx context.Context, event model.Event) error {
		if firstEventTime.IsZero() {
			firstEventTime = event.ExchangeTime()
			wallStart = e.clock.Now()
		}
		if request.Rate.WallTime > 0 {
			for {
				if err := e.controller.waitRunning(ctx); err != nil {
					return err
				}
				offset := scaleDuration(event.ExchangeTime().Sub(firstEventTime), request.Rate)
				target := wallStart.Add(offset).Add(e.controller.TotalPaused())
				now := e.clock.Now()
				if !now.Before(target) {
					break
				}
				if err := e.clock.WaitUntil(ctx, target); err != nil {
					return err
				}
				e.mu.Lock()
				e.metrics.ScheduledWaitTime += target.Sub(now)
				e.mu.Unlock()
			}
		}
		if err := e.controller.waitRunning(ctx); err != nil {
			return err
		}
		started := e.clock.Now()
		if err := sink(ctx, event); err != nil {
			return err
		}
		e.mu.Lock()
		e.metrics.Events++
		e.metrics.ConsumerTime += e.clock.Now().Sub(started)
		e.mu.Unlock()
		return nil
	})
	return err
}

func (e *Engine) setFinished(state State) {
	e.mu.Lock()
	e.running = false
	e.metrics.PauseTime = e.controller.TotalPaused()
	e.metrics.CompletedAt = e.clock.Now()
	e.metrics.TerminalState = state
	e.mu.Unlock()
}

func scaleDuration(duration time.Duration, rate Rate) time.Duration {
	numerator := new(big.Int).Mul(big.NewInt(duration.Nanoseconds()), big.NewInt(rate.WallTime.Nanoseconds()))
	numerator.Quo(numerator, big.NewInt(rate.EventsTime.Nanoseconds()))
	if !numerator.IsInt64() {
		return time.Duration(1<<63 - 1)
	}
	return time.Duration(numerator.Int64())
}
