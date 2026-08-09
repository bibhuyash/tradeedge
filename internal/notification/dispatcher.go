package notification

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

type ReplayPolicy string

const (
	ReplayVerifyOnly ReplayPolicy = "VERIFY_ONLY"
	ReplayDeliver    ReplayPolicy = "DELIVER"
)

type Config struct {
	Capacity, Workers, MaxAttempts                                                         int
	RequestTimeout, RetryBase, RetryMaximum, RetryHorizon, CoalesceWindow, DuplicateWindow time.Duration
	ReplayPolicy                                                                           ReplayPolicy
}

func DefaultConfig() Config {
	return Config{Capacity: 512, Workers: 2, MaxAttempts: 5, RequestTimeout: 3 * time.Second, RetryBase: time.Second, RetryMaximum: 2 * time.Minute, RetryHorizon: 15 * time.Minute, CoalesceWindow: 5 * time.Minute, DuplicateWindow: 24 * time.Hour, ReplayPolicy: ReplayVerifyOnly}
}
func (c Config) Validate() error {
	if c.Capacity < 4 || c.Capacity > 10000 || c.Workers < 1 || c.Workers > 32 || c.MaxAttempts < 1 || c.MaxAttempts > 20 || c.RequestTimeout <= 0 || c.RetryBase <= 0 || c.RetryMaximum < c.RetryBase || c.RetryHorizon < c.RetryMaximum || c.CoalesceWindow < 0 || c.DuplicateWindow <= 0 || (c.ReplayPolicy != ReplayVerifyOnly && c.ReplayPolicy != ReplayDeliver) {
		return ErrInvalid
	}
	return nil
}

type RetryableError struct {
	Class string
	After time.Duration
	Cause error
}

func (e *RetryableError) Error() string { return e.Class }
func (e *RetryableError) Unwrap() error { return e.Cause }

type work struct {
	event    Event
	message  RenderedMessage
	admitted time.Time
}
type seenValue struct {
	checksum string
	at       time.Time
}

type Dispatcher struct {
	cfg                     Config
	sender                  Sender
	store                   *Store
	telemetry               Telemetry
	now                     func() time.Time
	critical, warning, info chan work
	ctx                     context.Context
	cancel                  context.CancelFunc
	wg                      sync.WaitGroup
	mu                      sync.Mutex
	accepting               bool
	seen                    map[string]seenValue
	seenOrder               []string
	coalesced               map[string]time.Time
	inFlight                atomic.Int64
	failures                atomic.Uint64
	dropped                 atomic.Uint64
}

func NewDispatcher(cfg Config, sender Sender, store *Store, telemetry Telemetry, now func() time.Time) (*Dispatcher, error) {
	if cfg.Validate() != nil || sender == nil || store == nil {
		return nil, ErrInvalid
	}
	if telemetry == nil {
		telemetry = NoopTelemetry{}
	}
	if now == nil {
		now = time.Now
	}
	criticalCap := cfg.Capacity / 4
	remaining := cfg.Capacity - criticalCap
	warningCap := remaining / 2
	infoCap := remaining - warningCap
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dispatcher{cfg: cfg, sender: sender, store: store, telemetry: telemetry, now: now, critical: make(chan work, criticalCap), warning: make(chan work, warningCap), info: make(chan work, infoCap), ctx: ctx, cancel: cancel, accepting: true, seen: map[string]seenValue{}, coalesced: map[string]time.Time{}}
	for i := 0; i < cfg.Workers; i++ {
		d.wg.Add(1)
		go d.worker()
	}
	return d, nil
}

func (d *Dispatcher) Publish(event Event, replay bool) {
	d.store.RecordEvent(event)
	message := Render(event)
	now := d.now().UTC()
	if event.Kind == KindFinancialSnapshot {
		d.record(message, event, DeliverySuppressed, 0, "PRESENTATION_POLICY", now)
		return
	}
	if d.sender.Status().State == "DISABLED" {
		d.record(message, event, DeliverySuppressed, 0, "PROVIDER_DISABLED", now)
		return
	}
	if replay && d.cfg.ReplayPolicy == ReplayVerifyOnly {
		d.record(message, event, DeliverySuppressed, 0, "REPLAY_POLICY", now)
		return
	}
	d.mu.Lock()
	if !d.accepting {
		d.mu.Unlock()
		d.failAdmission(message, event, "SHUTDOWN", now)
		return
	}
	previous, seen := d.seen[message.NotificationID]
	if seen && now.Sub(previous.at) < d.cfg.DuplicateWindow {
		d.mu.Unlock()
		if previous.checksum != event.Checksum {
			d.failures.Add(1)
			d.record(message, event, DeliveryFailed, 0, "IDENTITY_COLLISION", now)
			d.metric(event, "admission", "failed", "IDENTITY_COLLISION")
			return
		}
		d.record(message, event, DeliverySuppressed, 0, "DUPLICATE", now)
		return
	}
	key := string(event.Category) + "|" + string(event.Kind) + "|" + event.Details.Subject + "|" + event.Details.State + "|" + event.Details.Reason
	coalescible := event.Category == CategoryReadiness || event.Category == CategoryRuntime
	if last, ok := d.coalesced[key]; coalescible && ok && now.Sub(last) < d.cfg.CoalesceWindow {
		d.mu.Unlock()
		d.record(message, event, DeliveryCoalesced, 0, "COALESCE_WINDOW", now)
		d.metric(event, "coalesce", "coalesced", "COALESCE_WINDOW")
		return
	}
	d.seen[message.NotificationID] = seenValue{checksum: event.Checksum, at: now}
	if !seen {
		d.seenOrder = append(d.seenOrder, message.NotificationID)
	}
	if coalescible {
		d.coalesced[key] = now
	}
	if len(d.seenOrder) > d.cfg.Capacity*4 {
		delete(d.seen, d.seenOrder[0])
		d.seenOrder = d.seenOrder[1:]
	}
	d.mu.Unlock()
	w := work{event: event, message: message, admitted: now}
	var queued bool
	switch event.Severity {
	case SeverityCritical:
		select {
		case d.critical <- w:
			queued = true
		default:
		}
		if !queued {
			queued = d.replaceLower(d.info, w, "EVICTED_BY_CRITICAL")
		}
		if !queued {
			queued = d.replaceLower(d.warning, w, "EVICTED_BY_CRITICAL")
		}
	case SeverityWarning:
		select {
		case d.warning <- w:
			queued = true
		default:
		}
		if !queued {
			queued = d.replaceLower(d.info, w, "EVICTED_BY_WARNING")
		}
	default:
		select {
		case d.info <- w:
			queued = true
		default:
		}
	}
	if !queued {
		d.failAdmission(message, event, "QUEUE_FULL", now)
		return
	}
	d.record(message, event, DeliveryPending, 0, "", now)
	d.metric(event, "admission", "queued", "")
}

func (d *Dispatcher) replaceLower(queue chan work, replacement work, reason string) bool {
	select {
	case evicted := <-queue:
		d.dropped.Add(1)
		d.record(evicted.message, evicted.event, DeliveryDropped, 0, reason, d.now().UTC())
		d.metric(evicted.event, "admission", "dropped", reason)
		select {
		case queue <- replacement:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// Observe implements the trading runtime's best-effort operational observer.
func (d *Dispatcher) Observe(event Event) { d.Publish(event, false) }

func (d *Dispatcher) failAdmission(message RenderedMessage, event Event, reason string, at time.Time) {
	d.dropped.Add(1)
	d.failures.Add(1)
	d.record(message, event, DeliveryFailed, 0, reason, at)
	d.metric(event, "admission", "failed", reason)
}
func (d *Dispatcher) record(m RenderedMessage, e Event, state DeliveryState, attempts int, reason string, at time.Time) {
	d.store.RecordDelivery(Delivery{NotificationID: m.NotificationID, EventID: e.ID, Provider: "telegram", State: state, Attempts: attempts, Reason: reason, UpdatedAt: at})
}
func (d *Dispatcher) metric(e Event, op, outcome, reason string) {
	d.telemetry.RecordNotification(MetricEvent{Operation: op, Outcome: outcome, Severity: string(e.Severity), Category: string(e.Category), Kind: string(e.Kind), Reason: reason, QueueDepth: d.depth()})
}

func (d *Dispatcher) next() (work, bool) {
	select {
	case w, ok := <-d.critical:
		return w, ok
	default:
	}
	select {
	case w, ok := <-d.warning:
		return w, ok
	default:
	}
	select {
	case w, ok := <-d.critical:
		return w, ok
	case w, ok := <-d.warning:
		return w, ok
	case w, ok := <-d.info:
		return w, ok
	case <-d.ctx.Done():
		return work{}, false
	}
}
func (d *Dispatcher) worker() {
	defer d.wg.Done()
	for {
		w, ok := d.next()
		if !ok {
			return
		}
		d.deliver(w)
	}
}
func (d *Dispatcher) deliver(w work) {
	d.inFlight.Add(1)
	defer d.inFlight.Add(-1)
	defer func() {
		if recovered := recover(); recovered != nil {
			d.failures.Add(1)
			d.record(w.message, w.event, DeliveryFailed, 0, "DISPATCHER_PANIC", d.now().UTC())
			d.metric(w.event, "delivery", "failed", "DISPATCHER_PANIC")
			_ = debug.Stack()
		}
	}()
	started := d.now()
	delay := d.cfg.RetryBase
	for attempt := 1; attempt <= d.cfg.MaxAttempts; attempt++ {
		d.record(w.message, w.event, DeliveryDelivering, attempt, "", d.now().UTC())
		ctx, cancel := context.WithTimeout(d.ctx, d.cfg.RequestTimeout)
		_, err := d.sender.Send(ctx, w.message)
		cancel()
		if err == nil {
			d.record(w.message, w.event, DeliveryDelivered, attempt, "", d.now().UTC())
			d.metric(w.event, "delivery", "delivered", "")
			return
		}
		var retry *RetryableError
		if !errors.As(err, &retry) || attempt == d.cfg.MaxAttempts || d.now().Sub(started)+delay > d.cfg.RetryHorizon {
			d.failures.Add(1)
			d.record(w.message, w.event, DeliveryFailed, attempt, boundedReason(err), d.now().UTC())
			d.metric(w.event, "delivery", "failed", boundedReason(err))
			return
		}
		if retry.After > delay {
			delay = retry.After
		}
		if delay > d.cfg.RetryMaximum {
			delay = d.cfg.RetryMaximum
		}
		d.record(w.message, w.event, DeliveryRetryWait, attempt, retry.Class, d.now().UTC())
		d.metric(w.event, "delivery", "retried", retry.Class)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-d.ctx.Done():
			timer.Stop()
			d.record(w.message, w.event, DeliveryFailed, attempt, "SHUTDOWN_TIMEOUT", d.now().UTC())
			return
		}
		delay *= 2
		if delay > d.cfg.RetryMaximum {
			delay = d.cfg.RetryMaximum
		}
	}
}
func boundedReason(err error) string {
	var retry *RetryableError
	if errors.As(err, &retry) && retry.Class != "" {
		return retry.Class
	}
	return "PERMANENT_FAILURE"
}

func (d *Dispatcher) depth() int { return len(d.critical) + len(d.warning) + len(d.info) }
func (d *Dispatcher) Health() Health {
	d.mu.Lock()
	accepting := d.accepting
	d.mu.Unlock()
	state := "READY"
	if !accepting {
		state = "STOPPED"
	} else if d.failures.Load() > 0 {
		state = "DEGRADED"
	}
	return Health{State: state, Accepting: accepting, QueueDepth: d.depth(), QueueCapacity: d.cfg.Capacity, InFlight: int(d.inFlight.Load()), FailureCount: d.failures.Load(), DroppedCount: d.dropped.Load(), UpdatedAt: d.now().UTC()}
}
func (d *Dispatcher) ProviderStatus() ProviderStatus { return d.sender.Status() }
func (d *Dispatcher) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	if !d.accepting {
		d.mu.Unlock()
		return nil
	}
	d.accepting = false
	d.mu.Unlock()
	done := make(chan struct{})
	go func() {
		for d.depth() > 0 || d.inFlight.Load() > 0 {
			time.Sleep(time.Millisecond)
		}
		d.cancel()
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		d.cancel()
		return fmt.Errorf("notification shutdown: %w", ctx.Err())
	}
}
