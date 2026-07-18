package movingaverage

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

func TestConfigurationAndWarmup(t *testing.T) {
	t.Parallel()
	strategy, configuration, instrument := fixture(t, 2, 3)
	initial, err := strategy.InitialState(configuration)
	if err != nil {
		t.Fatal(err)
	}
	result, err := strategy.Evaluate(context.Background(),
		input(t, strategy, configuration, initial, instrument, []int64{100, 101}))
	if err != nil || result.Kind() != strategymodel.ResultNoAction {
		t.Fatalf("warmup = %#v, %v", result, err)
	}
	noAction, _ := result.NoAction()
	if noAction.Reason != strategymodel.NoActionInsufficientHistory {
		t.Fatalf("warmup reason = %s", noAction.Reason)
	}
}

func TestBullishBearishAndNoRepeatedProposal(t *testing.T) {
	t.Parallel()
	strategy, configuration, instrument := fixture(t, 2, 3)
	current, _ := strategy.InitialState(configuration)
	steps := []struct {
		prices []int64
		kind   strategymodel.ResultKind
		side   domain.Side
	}{
		{[]int64{300, 200, 100}, strategymodel.ResultNoAction, ""},
		{[]int64{100, 200, 400}, strategymodel.ResultTradeProposal, domain.SideBuy},
		{[]int64{200, 400, 500}, strategymodel.ResultNoAction, ""},
		{[]int64{500, 200, 100}, strategymodel.ResultTradeProposal, domain.SideSell},
	}
	for index, step := range steps {
		result, err := strategy.Evaluate(context.Background(),
			input(t, strategy, configuration, current, instrument, step.prices))
		if err != nil || result.Kind() != step.kind {
			t.Fatalf("step %d = %s, %v", index, result.Kind(), err)
		}
		if step.kind == strategymodel.ResultTradeProposal {
			proposal, ok := result.Proposal()
			if !ok || len(proposal.Legs) != 1 || proposal.Legs[0].Side != step.side {
				t.Fatalf("step %d proposal = %#v", index, proposal)
			}
		}
		current = result.NextState()
	}
}

func TestInvalidConfigurationAndIntegerOverflow(t *testing.T) {
	t.Parallel()
	instrument, _ := domain.InstrumentIDFromCanonicalKey("NSE|INDEX|moving-average-invalid")
	strategy, err := New(instrument, marketmodel.Interval1Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	invalid, _ := strategymodel.NewStrategyConfiguration(
		"moving-average-config/v1",
		[]byte(`{"short_window":3,"long_window":3,"sizing_bps":100}`),
	)
	if !errors.Is(strategy.ValidateConfiguration(invalid), ErrInvalidConfiguration) {
		t.Fatal("equal windows accepted")
	}

	valid, _ := strategymodel.NewStrategyConfiguration(
		"moving-average-config/v1",
		[]byte(`{"short_window":2,"long_window":3,"sizing_bps":100}`),
	)
	initial, _ := strategy.InitialState(valid)
	_, err = strategy.Evaluate(context.Background(),
		input(t, strategy, valid, initial, instrument,
			[]int64{math.MaxInt64, math.MaxInt64, math.MaxInt64}))
	if err == nil || err.Error() != "moving-average integer overflow" {
		t.Fatalf("overflow error = %v", err)
	}
}

func fixture(
	t *testing.T,
	shortWindow, longWindow int,
) (*Strategy, strategymodel.StrategyConfiguration, domain.InstrumentID) {
	t.Helper()
	instrument, _ := domain.InstrumentIDFromCanonicalKey("NSE|INDEX|" + t.Name())
	strategy, err := New(instrument, marketmodel.Interval1Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := strategymodel.NewStrategyConfiguration(
		"moving-average-config/v1",
		[]byte(
			`{"short_window":`+integer(shortWindow)+
				`,"long_window":`+integer(longWindow)+`,"sizing_bps":100}`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	return strategy, configuration, instrument
}

func input(
	t *testing.T,
	strategy *Strategy,
	configuration strategymodel.StrategyConfiguration,
	state strategymodel.StrategyRuntimeState,
	instrument domain.InstrumentID,
	prices []int64,
) strategymodel.EvaluationContext {
	t.Helper()
	subscription := strategy.Descriptor().Subscriptions.Subscriptions()[0]
	candles := make([]marketmodel.CompletedCandleEvent, len(prices))
	start := time.Date(2026, 7, 18, 3, 45, 0, 0, time.UTC)
	for index, value := range prices {
		price, _ := domain.NewPrice(value, "INR")
		candle, err := marketmodel.NewCompletedCandleEvent(marketmodel.CandleSpec{
			InstrumentID: instrument, Interval: marketmodel.Interval1Minute,
			OpenTime:  start.Add(time.Duration(index) * time.Minute),
			CloseTime: start.Add(time.Duration(index+1) * time.Minute),
			Open:      price, High: price, Low: price, Close: price,
			Volume: 1, EventCount: 1,
			IngestedAt: start.Add(time.Duration(index+1) * time.Minute),
			Provenance: marketmodel.Provenance{
				Provider: "fixture", ProviderToken: "fixture-token",
				MasterVersion: "master/v1", DatasetRevision: "dataset/v1",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		candles[index] = candle
	}
	series, err := strategymodel.NewCandleSeries(subscription, candles)
	if err != nil {
		t.Fatal(err)
	}
	trigger, _ := strategymodel.NewTriggerID("moving-average-frame|" + candles[len(candles)-1].ID().String())
	frame, err := strategymodel.NewCandleFrame(strategymodel.CandleFrameSpec{
		TriggerID: trigger, LogicalTime: candles[len(candles)-1].CloseTime(),
		Subscription: strategy.Descriptor().Subscriptions,
		Series:       []strategymodel.CandleSeries{series}, MasterVersion: "master/v1",
		CalendarVersion: "calendar/v1", DatasetRevision: "dataset/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return strategymodel.EvaluationContext{
		Configuration: configuration, PriorState: state, Frame: frame,
		LogicalTime: frame.LogicalTime(),
	}
}

func integer(value int) string {
	if value == 0 {
		return "0"
	}
	result := ""
	for value > 0 {
		result = string(rune('0'+value%10)) + result
		value /= 10
	}
	return result
}
