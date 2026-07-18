package replay

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/storage"
)

func TestReplayEmptyDataset(t *testing.T) {
	reader := datasetReader(t, nil)
	clock := NewManualClock(time.Unix(0, 0))
	engine := NewEngine(clock, nil)
	var calls int
	if err := engine.Replay(context.Background(), reader, Request{Rate: MaximumRate()},
		func(context.Context, model.Event) error { calls++; return nil }); err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if calls != 0 || engine.Metrics().TerminalState != StateCompleted {
		t.Fatalf("calls=%d metrics=%#v", calls, engine.Metrics())
	}
}

func TestReplayOneEventAndMultipleInstrumentsDeterministically(t *testing.T) {
	base := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	events := []model.Event{
		quoteEvent(t, "instrument-b", base, 10200),
		quoteEvent(t, "instrument-a", base, 10100),
		quoteEvent(t, "instrument-a", base.Add(time.Second), 10300),
	}
	reader := datasetReader(t, events)
	run := func() []string {
		clock := NewManualClock(base)
		engine := NewEngine(clock, nil)
		var ids []string
		if err := engine.Replay(context.Background(), reader, Request{Rate: MaximumRate()},
			func(_ context.Context, event model.Event) error {
				ids = append(ids, event.ID().String())
				return nil
			}); err != nil {
			t.Fatalf("Replay() error = %v", err)
		}
		return ids
	}
	first := run()
	second := run()
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("replay lengths = %d and %d", len(first), len(second))
	}
	for index := range first {
		if first[index] != second[index] {
			t.Fatalf("replay differs at %d", index)
		}
	}
}

func TestReplayAcrossCandleBoundaries(t *testing.T) {
	base := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	first := candleEvent(t, "instrument", base)
	second := candleEvent(t, "instrument", base.Add(time.Minute))
	reader := datasetReader(t, []model.Event{second, first})
	engine := NewEngine(NewManualClock(base), nil)
	var opens []time.Time
	err := engine.Replay(context.Background(), reader, Request{Rate: MaximumRate()},
		func(_ context.Context, event model.Event) error {
			opens = append(opens, event.(model.CompletedCandleEvent).OpenTime())
			return nil
		})
	if err != nil || len(opens) != 2 || !opens[0].Before(opens[1]) {
		t.Fatalf("Replay() opens=%v error=%v", opens, err)
	}
}

func TestReplayUsesInclusiveStartAndExclusiveEnd(t *testing.T) {
	base := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	reader := datasetReader(t, []model.Event{
		quoteEvent(t, "instrument", base, 100),
		quoteEvent(t, "instrument", base.Add(time.Second), 101),
		quoteEvent(t, "instrument", base.Add(2*time.Second), 102),
	})
	engine := NewEngine(NewManualClock(base), nil)
	var times []time.Time
	err := engine.Replay(context.Background(), reader, Request{
		Rate:  MaximumRate(),
		Query: storage.EventQuery{Start: base.Add(time.Second), End: base.Add(2 * time.Second)},
	}, func(_ context.Context, event model.Event) error {
		times = append(times, event.ExchangeTime())
		return nil
	})
	if err != nil || len(times) != 1 || !times[0].Equal(base.Add(time.Second)) {
		t.Fatalf("Replay() times=%v error=%v", times, err)
	}
}

func TestReplayCancellationStopsPromptlyWithoutLeakingWorker(t *testing.T) {
	before := runtime.NumGoroutine()
	base := time.Now()
	reader := datasetReader(t, []model.Event{
		quoteEvent(t, "instrument", base, 100),
		quoteEvent(t, "instrument", base.Add(time.Hour), 101),
	})
	engine := NewEngine(RealClock{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := engine.Replay(ctx, reader, Request{Rate: RealTimeRate()}, func(context.Context, model.Event) error {
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Replay() error = %v, want deadline exceeded", err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatalf("cancellation was not prompt: %s", time.Since(started))
	}
	time.Sleep(10 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Fatalf("goroutines before=%d after=%d", before, after)
	}
}

func TestReplayPropagatesConsumerError(t *testing.T) {
	want := errors.New("consumer failed")
	reader := datasetReader(t, []model.Event{quoteEvent(t, "instrument", time.Now(), 100)})
	engine := NewEngine(NewManualClock(time.Now()), nil)
	err := engine.Replay(context.Background(), reader, Request{Rate: MaximumRate()},
		func(context.Context, model.Event) error { return want })
	if !errors.Is(err, want) || engine.Metrics().TerminalState != StateFailed {
		t.Fatalf("Replay() error=%v metrics=%#v", err, engine.Metrics())
	}
}

func TestReplayAppliesSynchronousBackpressureAndPauseResume(t *testing.T) {
	base := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	reader := datasetReader(t, []model.Event{
		quoteEvent(t, "instrument", base, 100),
		quoteEvent(t, "instrument", base.Add(time.Second), 101),
	})
	clock := NewManualClock(base)
	controller := NewController(clock)
	engine := NewEngine(clock, controller)
	firstSeen := make(chan struct{})
	done := make(chan error, 1)
	var active atomic.Int32
	var calls atomic.Int32
	go func() {
		done <- engine.Replay(context.Background(), reader, Request{Rate: MaximumRate()},
			func(context.Context, model.Event) error {
				if active.Add(1) != 1 {
					t.Error("consumer was invoked concurrently")
				}
				defer active.Add(-1)
				if calls.Add(1) == 1 {
					if err := controller.Pause(); err != nil {
						return err
					}
					close(firstSeen)
				}
				return nil
			})
	}()
	<-firstSeen
	time.Sleep(10 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("calls while paused = %d, want 1", calls.Load())
	}
	if err := controller.Resume(); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func datasetReader(t *testing.T, events []model.Event) storage.DatasetReader {
	t.Helper()
	repository := storage.NewMemoryRepository()
	writer, err := repository.Create(context.Background(), storage.DraftManifest{
		MasterVersion: "v1", Source: "test", OrderingVersion: "v1", CreatedAt: time.Unix(0, 0),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	for _, event := range events {
		// The writer enforces order; tests may supply deliberately unsorted input.
		_ = event
	}
	sorted := append([]model.Event(nil), events...)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if model.EventLess(sorted[j], sorted[i]) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	for _, event := range sorted {
		if err := writer.Append(context.Background(), event); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}
	manifest, err := writer.Commit(context.Background())
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	reader, err := repository.Open(context.Background(), manifest.ID)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return reader
}

func quoteEvent(t *testing.T, key string, at time.Time, minor int64) model.QuoteEvent {
	t.Helper()
	id, _ := domain.InstrumentIDFromCanonicalKey(key)
	price, _ := domain.NewPrice(minor, "INR")
	event, err := model.NewQuoteEvent(model.QuoteSpec{
		InstrumentID: id, LastPrice: price, ExchangeTime: at, IngestedAt: at.Add(time.Millisecond),
		Provenance: model.Provenance{Provider: "fixture", ProviderToken: key, MasterVersion: "v1"},
	})
	if err != nil {
		t.Fatalf("NewQuoteEvent() error = %v", err)
	}
	return event
}

func candleEvent(t *testing.T, key string, start time.Time) model.CompletedCandleEvent {
	t.Helper()
	id, _ := domain.InstrumentIDFromCanonicalKey(key)
	open, _ := domain.NewPrice(100, "INR")
	high, _ := domain.NewPrice(110, "INR")
	low, _ := domain.NewPrice(90, "INR")
	closePrice, _ := domain.NewPrice(105, "INR")
	event, err := model.NewCompletedCandleEvent(model.CandleSpec{
		InstrumentID: id, Interval: model.Interval1Minute, OpenTime: start, CloseTime: start.Add(time.Minute),
		Open: open, High: high, Low: low, Close: closePrice, Volume: 10,
		IngestedAt: start.Add(time.Minute), Provenance: model.Provenance{
			Provider: "fixture", ProviderToken: key, MasterVersion: "v1",
		},
	})
	if err != nil {
		t.Fatalf("NewCompletedCandleEvent() error = %v", err)
	}
	return event
}
