package startup

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type fakePreparation struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakePreparation) Prepare(context.Context) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.err
}

type fakeRuntime struct {
	running, healthy bool
	starts           int
	mu               sync.Mutex
	err              error
}

func (f *fakeRuntime) Status(context.Context) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running, f.healthy, f.err
}
func (f *fakeRuntime) StartShadow(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.running = true
	f.healthy = true
	return f.err
}

type fakeReady struct {
	ready bool
	err   error
}

func (f fakeReady) Ready(context.Context) (bool, error) { return f.ready, f.err }

type fakeSession bool

func (f fakeSession) Authenticated(context.Context) (bool, error) { return bool(f), nil }

type fakeMarket struct {
	closed bool
	reason string
	date   string
}

func (f fakeMarket) Closed(context.Context) (bool, string, error) { return f.closed, f.reason, nil }
func (f fakeMarket) TradingDate() string {
	if f.date != "" {
		return f.date
	}
	return "2026-01-01"
}

type rollingMarket struct {
	mu     sync.Mutex
	date   string
	closed bool
	reason string
	calls  int
}

func (m *rollingMarket) TradingDate() string { m.mu.Lock(); defer m.mu.Unlock(); return m.date }
func (m *rollingMarket) Closed(context.Context) (bool, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.closed, m.reason, nil
}
func (m *rollingMarket) advance(date string, closed bool, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.date, m.closed, m.reason = date, closed, reason
}

func TestStartIsIdempotentAndReusesHealthyRuntime(t *testing.T) {
	p := &fakePreparation{}
	r := &fakeRuntime{running: true, healthy: true}
	s, _ := New(p, r, fakeReady{ready: true}, fakeSession(true), fakeMarket{})
	if got := s.Start(context.Background()).State; got != Ready {
		t.Fatalf("state=%s", got)
	}
	if r.starts != 0 {
		t.Fatalf("starts=%d", r.starts)
	}
}

func TestConcurrentStartRunsOnce(t *testing.T) {
	p := &fakePreparation{}
	r := &fakeRuntime{}
	s, _ := New(p, r, fakeReady{ready: true}, fakeSession(true), fakeMarket{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := s.Start(context.Background()).State; got != Ready {
				t.Errorf("state=%s", got)
			}
		}()
	}
	wg.Wait()
	if p.calls != 1 || r.starts != 1 {
		t.Fatalf("prepare=%d starts=%d", p.calls, r.starts)
	}
}

func TestLoginClosedAndFailureStates(t *testing.T) {
	s, _ := New(&fakePreparation{}, &fakeRuntime{}, fakeReady{ready: true}, fakeSession(false), fakeMarket{})
	if s.Start(context.Background()).State != LoginRequired {
		t.Fatal("expected login required")
	}
	s, _ = New(&fakePreparation{err: errors.New("blocked")}, &fakeRuntime{}, fakeReady{ready: true}, fakeSession(true), fakeMarket{})
	if s.Start(context.Background()).State != Failed {
		t.Fatal("expected failure")
	}
	s, _ = New(&fakePreparation{}, &fakeRuntime{}, fakeReady{ready: true}, fakeSession(true), fakeMarket{closed: true, reason: "WEEKEND"})
	if got := s.Start(context.Background()); got.State != MarketClosed || got.Reason != "WEEKEND" {
		t.Fatalf("%+v", got)
	}
}

func TestMarketClosedCanBeReevaluated(t *testing.T) {
	p := &fakePreparation{}
	r := &fakeRuntime{}
	market := &fakeMarket{closed: true, reason: "WEEKEND"}
	s, _ := New(p, r, fakeReady{ready: true}, fakeSession(true), market)
	if got := s.Start(context.Background()).State; got != MarketClosed {
		t.Fatalf("state=%s", got)
	}
	market.closed, market.reason = false, ""
	if got := s.Start(context.Background()).State; got != Ready {
		t.Fatalf("state=%s", got)
	}
	if p.calls != 1 || r.starts != 1 {
		t.Fatalf("prepare=%d starts=%d", p.calls, r.starts)
	}
}

func TestSundayToMondayRecomputesWithoutRestart(t *testing.T) {
	p := &fakePreparation{}
	r := &fakeRuntime{}
	m := &rollingMarket{date: "2026-09-20", closed: true, reason: "WEEKEND"}
	s, _ := New(p, r, fakeReady{ready: true}, fakeSession(true), m)
	if got := s.Start(context.Background()); got.State != MarketClosed || got.Reason != "WEEKEND" {
		t.Fatalf("sunday=%+v", got)
	}
	m.advance("2026-09-21", false, "")
	if got := s.Start(context.Background()); got.State != Ready || got.Reason == "WEEKEND" || got.TradingDate != "2026-09-21" {
		t.Fatalf("monday=%+v", got)
	}
}

func TestAfterCloseToNextTradingDayRecomputes(t *testing.T) {
	r := &fakeRuntime{running: true, healthy: true}
	m := &rollingMarket{date: "2026-09-21", closed: true, reason: "SESSION_CLOSED"}
	s, _ := New(&fakePreparation{}, r, fakeReady{ready: true}, fakeSession(true), m)
	if got := s.Start(context.Background()); got.State != MarketClosed {
		t.Fatalf("monday=%+v", got)
	}
	m.advance("2026-09-22", false, "")
	if got := s.Start(context.Background()); got.State != Ready || got.TradingDate != "2026-09-22" {
		t.Fatalf("tuesday=%+v", got)
	}
}

func TestHolidayDoesNotLeakIntoTradingDay(t *testing.T) {
	m := &rollingMarket{date: "2026-10-20", closed: true, reason: "HOLIDAY"}
	s, _ := New(&fakePreparation{}, &fakeRuntime{}, fakeReady{ready: true}, fakeSession(true), m)
	if got := s.Start(context.Background()); got.State != MarketClosed || got.Reason != "HOLIDAY" {
		t.Fatalf("holiday=%+v", got)
	}
	m.advance("2026-10-21", false, "")
	if got := s.Start(context.Background()); got.State != Ready || got.Reason == "HOLIDAY" {
		t.Fatalf("trading day=%+v", got)
	}
}

func TestConcurrentRolloverHasSingleOwner(t *testing.T) {
	p := &fakePreparation{}
	r := &fakeRuntime{}
	m := &rollingMarket{date: "2026-09-20", closed: true, reason: "WEEKEND"}
	s, _ := New(p, r, fakeReady{ready: true}, fakeSession(true), m)
	_ = s.Start(context.Background())
	m.advance("2026-09-21", false, "")
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := s.Start(context.Background()); got.State != Ready {
				t.Errorf("state=%s", got.State)
			}
		}()
	}
	wg.Wait()
	p.mu.Lock()
	calls := p.calls
	p.mu.Unlock()
	if calls != 1 || r.starts != 1 {
		t.Fatalf("prepare=%d starts=%d", calls, r.starts)
	}
}

func TestStatusInvalidatesPriorTradingDate(t *testing.T) {
	m := &rollingMarket{date: "2026-09-20", closed: true, reason: "WEEKEND"}
	s, _ := New(&fakePreparation{}, &fakeRuntime{}, fakeReady{ready: true}, fakeSession(true), m)
	_ = s.Start(context.Background())
	m.advance("2026-09-21", false, "")
	got := s.Status(context.Background())
	if got.State != Authenticated || got.Reason == "WEEKEND" || got.TradingDate != "2026-09-21" {
		t.Fatalf("status=%+v", got)
	}
}
