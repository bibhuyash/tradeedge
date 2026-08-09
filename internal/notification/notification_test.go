package notification

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeSender struct {
	mu       sync.Mutex
	calls    int
	failures int
	block    <-chan struct{}
	panic    bool
}

func (f *fakeSender) Send(ctx context.Context, _ RenderedMessage) (Receipt, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	if f.panic {
		panic("adapter")
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return Receipt{}, &RetryableError{Class: "TRANSPORT", Cause: ctx.Err()}
		}
	}
	if call <= f.failures {
		return Receipt{}, &RetryableError{Class: "TRANSPORT"}
	}
	return Receipt{ProviderMessageID: "1"}, nil
}
func (*fakeSender) Status() ProviderStatus { return ProviderStatus{Provider: "test", State: "READY"} }
func (f *fakeSender) Calls() int           { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }
func event(t *testing.T, id string, severity Severity, kind Kind) Event {
	t.Helper()
	v, err := NewEvent(EventSpec{SourceID: id, TradingDate: "2026-08-10", OccurredAt: time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC), Mode: "PAPER", Category: CategoryExecution, Kind: kind, Severity: severity, Details: Details{Reason: "SAFE_CODE"}})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestEventIdentityAndReplaySuppression(t *testing.T) {
	first := event(t, "source", SeverityInfo, KindPaperFill)
	second := event(t, "source", SeverityInfo, KindPaperFill)
	if first.ID != second.ID || first.Checksum != second.Checksum {
		t.Fatal("event identity is not deterministic")
	}
	store, _ := NewStore(10, 20)
	sender := &fakeSender{}
	d, err := NewDispatcher(DefaultConfig(), sender, store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Publish(first, true)
	if sender.Calls() != 0 || store.RecentDeliveries(1, false)[0].Reason != "REPLAY_POLICY" {
		t.Fatal("replay reached provider")
	}
	_ = d.Shutdown(context.Background())
}
func TestDuplicateRetryAndExhaustionEvidence(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RetryBase = time.Millisecond
	cfg.RetryMaximum = 2 * time.Millisecond
	cfg.RetryHorizon = 20 * time.Millisecond
	cfg.MaxAttempts = 3
	store, _ := NewStore(20, 100)
	sender := &fakeSender{failures: 2}
	d, _ := NewDispatcher(cfg, sender, store, nil, nil)
	value := event(t, "retry", SeverityWarning, KindRiskRejected)
	d.Publish(value, false)
	waitFor(t, func() bool { return sender.Calls() == 3 })
	d.Publish(value, false)
	if got := store.RecentDeliveries(1, false)[0]; got.Reason != "DUPLICATE" {
		t.Fatalf("duplicate not suppressed: %+v", got)
	}
	_ = d.Shutdown(context.Background())
}
func TestIdentityCollisionIsFailedExplicitly(t *testing.T) {
	store, _ := NewStore(10, 30)
	sender := &fakeSender{}
	d, _ := NewDispatcher(DefaultConfig(), sender, store, nil, nil)
	first := event(t, "collision", SeverityWarning, KindRiskRejected)
	second, err := NewEvent(EventSpec{SourceID: "collision", TradingDate: first.TradingDate, OccurredAt: first.OccurredAt, Mode: first.Mode, Category: first.Category, Kind: first.Kind, Severity: first.Severity, Details: Details{Reason: "DIFFERENT"}})
	if err != nil {
		t.Fatal(err)
	}
	d.Publish(first, false)
	waitFor(t, func() bool { return sender.Calls() == 1 })
	d.Publish(second, false)
	latest := store.RecentDeliveries(1, false)[0]
	if latest.State != DeliveryFailed || latest.Reason != "IDENTITY_COLLISION" {
		t.Fatalf("collision not evidenced: %+v", latest)
	}
	_ = d.Shutdown(context.Background())
}

func TestCriticalQueueFullIsExplicitAndBounded(t *testing.T) {
	block := make(chan struct{})
	cfg := DefaultConfig()
	cfg.Capacity = 4
	cfg.Workers = 1
	cfg.RequestTimeout = time.Second
	store, _ := NewStore(20, 100)
	sender := &fakeSender{block: block}
	d, _ := NewDispatcher(cfg, sender, store, nil, nil)
	for i := 0; i < 4; i++ {
		d.Publish(event(t, string(rune('a'+i)), SeverityCritical, KindExecutionUnknown), false)
	}
	waitFor(t, func() bool { return d.Health().FailureCount > 0 })
	failed := store.RecentDeliveries(100, true)
	if len(failed) == 0 || failed[0].Reason != "QUEUE_FULL" {
		t.Fatalf("critical loss not evidenced: %+v", failed)
	}
	close(block)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestCriticalAdmissionEvictsLowerPriorityWithEvidence(t *testing.T) {
	block := make(chan struct{})
	cfg := DefaultConfig()
	cfg.Capacity = 4
	cfg.Workers = 1
	cfg.RequestTimeout = time.Second
	store, _ := NewStore(30, 100)
	sender := &fakeSender{block: block}
	d, _ := NewDispatcher(cfg, sender, store, nil, nil)
	d.Publish(event(t, "info-running", SeverityInfo, KindPaperFill), false)
	waitFor(t, func() bool { return d.Health().InFlight == 1 })
	d.Publish(event(t, "info-queued-1", SeverityInfo, KindPaperFill), false)
	d.Publish(event(t, "info-queued-2", SeverityInfo, KindPaperFill), false)
	d.Publish(event(t, "critical-1", SeverityCritical, KindExecutionUnknown), false)
	d.Publish(event(t, "critical-2", SeverityCritical, KindKillSwitch), false)
	deliveries := store.RecentDeliveries(20, false)
	found := false
	for _, value := range deliveries {
		if value.State == DeliveryDropped && value.Reason == "EVICTED_BY_CRITICAL" {
			found = true
		}
	}
	if !found {
		t.Fatalf("lower-priority eviction not evidenced: %+v", deliveries)
	}
	close(block)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestApprovedEventTemplatesAreBounded(t *testing.T) {
	kinds := []Kind{KindShadowTrade, KindPaperFill, KindRiskRejected, KindExecutionUnknown, KindKillSwitch, KindPreCAS, KindReadinessLost, KindEndOfDay, KindReconciliationMismatch, KindCircuitBreaker}
	for index, kind := range kinds {
		value := event(t, string(rune('k'+index)), SeverityWarning, kind)
		message := Render(value)
		if message.Text == "" || len(message.Text) > 1000 {
			t.Fatalf("invalid %s template", kind)
		}
	}
}
func TestDispatcherContainsProviderPanic(t *testing.T) {
	store, _ := NewStore(10, 20)
	sender := &fakeSender{panic: true}
	d, _ := NewDispatcher(DefaultConfig(), sender, store, nil, nil)
	d.Publish(event(t, "panic", SeverityCritical, KindKillSwitch), false)
	waitFor(t, func() bool { return d.Health().FailureCount == 1 })
	if d.Health().State != "DEGRADED" {
		t.Fatal("panic did not degrade notification health")
	}
	_ = d.Shutdown(context.Background())
}
func TestInvalidConfig(t *testing.T) {
	if _, err := NewDispatcher(Config{}, &fakeSender{}, mustStore(t), nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
}
func mustStore(t *testing.T) *Store {
	s, err := NewStore(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(time.Millisecond)
	}
}
