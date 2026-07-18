package replay

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
	"github.com/bibhuyash/tradeedge/internal/strategy/runner"
)

type recordingEvaluator struct {
	mu         sync.Mutex
	frames     []strategymodel.CandleFrame
	active     int
	concurrent bool
	err        error
}

func (evaluator *recordingEvaluator) EvaluateFrame(
	_ context.Context,
	_ domain.StrategyID,
	frame strategymodel.CandleFrame,
) (runner.Receipt, error) {
	evaluator.mu.Lock()
	defer evaluator.mu.Unlock()
	evaluator.active++
	if evaluator.active > 1 {
		evaluator.concurrent = true
	}
	defer func() { evaluator.active-- }()
	if evaluator.err != nil {
		return runner.Receipt{}, evaluator.err
	}
	evaluator.frames = append(evaluator.frames, frame)
	return runner.Receipt{Outcome: runner.OutcomeNoAction}, nil
}

func TestSinkBuildsBoundedFramesSerially(t *testing.T) {
	t.Parallel()
	instrument, _ := domain.InstrumentIDFromCanonicalKey("NSE|INDEX|replay-sink")
	subscriptions, _ := strategymodel.NewSubscriptionSpec(
		strategymodel.SubscriptionSingleStream,
		[]strategymodel.InputSubscription{{
			Role: "primary", InstrumentID: instrument, Interval: marketmodel.Interval1Minute,
			Required: true, Trigger: true, Lookback: 3,
		}},
	)
	instanceID, _ := domain.NewStrategyID("replay-sink-instance")
	evaluator := &recordingEvaluator{}
	sink, err := NewSink(evaluator, instanceID, subscriptions, "calendar/v1")
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4; index++ {
		if err := sink.Consume(context.Background(), replayCandle(t, instrument, index)); err != nil {
			t.Fatal(err)
		}
	}
	if evaluator.concurrent || len(evaluator.frames) != 4 || len(sink.Receipts()) != 4 {
		t.Fatalf("frames=%d receipts=%d concurrent=%t",
			len(evaluator.frames), len(sink.Receipts()), evaluator.concurrent)
	}
	want := []int{1, 2, 3, 3}
	for index, frame := range evaluator.frames {
		if got := len(frame.Series()[0].Candles); got != want[index] {
			t.Fatalf("frame %d candle count = %d, want %d", index, got, want[index])
		}
	}
}

func TestSinkPropagatesSynchronousConsumerFailure(t *testing.T) {
	t.Parallel()
	instrument, _ := domain.InstrumentIDFromCanonicalKey("NSE|INDEX|replay-error")
	subscriptions, _ := strategymodel.NewSubscriptionSpec(
		strategymodel.SubscriptionSingleStream,
		[]strategymodel.InputSubscription{{
			Role: "primary", InstrumentID: instrument, Interval: marketmodel.Interval1Minute,
			Required: true, Trigger: true, Lookback: 1,
		}},
	)
	instanceID, _ := domain.NewStrategyID("replay-error-instance")
	expected := errors.New("consumer failed")
	sink, _ := NewSink(&recordingEvaluator{err: expected}, instanceID, subscriptions, "calendar/v1")
	if err := sink.Consume(
		context.Background(), replayCandle(t, instrument, 0),
	); !errors.Is(err, expected) {
		t.Fatalf("Consume() error = %v", err)
	}
	if len(sink.Receipts()) != 0 {
		t.Fatal("failed evaluation created receipt")
	}
}

func replayCandle(
	t *testing.T,
	instrument domain.InstrumentID,
	index int,
) marketmodel.CompletedCandleEvent {
	t.Helper()
	open := time.Date(2026, 7, 18, 3, 45+index, 0, 0, time.UTC)
	price, _ := domain.NewPrice(int64(100+index), "INR")
	value, err := marketmodel.NewCompletedCandleEvent(marketmodel.CandleSpec{
		InstrumentID: instrument, Interval: marketmodel.Interval1Minute,
		OpenTime: open, CloseTime: open.Add(time.Minute),
		Open: price, High: price, Low: price, Close: price, Volume: 1, EventCount: 1,
		IngestedAt: open.Add(time.Minute), Provenance: marketmodel.Provenance{
			Provider: "fixture", ProviderToken: "fixture-token",
			MasterVersion: "master/v1", DatasetRevision: "dataset/v1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
