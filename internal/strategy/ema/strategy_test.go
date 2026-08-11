package ema

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

func TestFixedPointEMAUsesDocumentedRounding(t *testing.T) {
	t.Parallel()
	candles := candles(t, instrument(t, "ema-known"), []int64{100, 200, 300}, 0)
	got, err := calculate(candles, 2)
	if err != nil || got != 255555556 {
		t.Fatalf("EMA = %d, %v; want 255555556", got, err)
	}
	negative, err := roundedFraction(-1, 2, 3)
	if err != nil || negative != -1 {
		t.Fatalf("negative rounding = %d, %v", negative, err)
	}
}

func TestDisabledWarmupCrossoverExitAndNoRepeat(t *testing.T) {
	t.Parallel()
	strategy, enabled, disabled, signal, execution := fixture(t)
	state, err := strategy.InitialState(disabled)
	if err != nil {
		t.Fatal(err)
	}
	result, err := strategy.Evaluate(context.Background(), input(t, strategy, disabled, state, signal, execution, descending(50), 0))
	assertNoAction(t, result, err, strategymodel.NoActionDisabled)

	state, _ = strategy.InitialState(enabled)
	result, err = strategy.Evaluate(context.Background(), input(t, strategy, enabled, state, signal, execution, descending(49), 0))
	assertNoAction(t, result, err, strategymodel.NoActionInsufficientHistory)
	state = result.NextState()

	result, err = strategy.Evaluate(context.Background(), input(t, strategy, enabled, state, signal, execution, descending(50), 60))
	assertNoAction(t, result, err, strategymodel.NoActionNoCrossover)
	state = result.NextState()

	result, err = strategy.Evaluate(context.Background(), input(t, strategy, enabled, state, signal, execution, ascending(50), 120))
	if err != nil || result.Kind() != strategymodel.ResultTradeProposal {
		t.Fatalf("bullish = %s, %v", result.Kind(), err)
	}
	proposal, _ := result.Proposal()
	if len(proposal.Legs) != 1 || proposal.Legs[0].InstrumentID != execution || proposal.Legs[0].Side != domain.SideBuy || proposal.Legs[0].Ratio != 1 {
		t.Fatalf("bullish proposal = %#v", proposal)
	}
	state = result.NextState()

	result, err = strategy.Evaluate(context.Background(), input(t, strategy, enabled, state, signal, execution, ascending(50), 180))
	assertNoAction(t, result, err, strategymodel.NoActionNoCrossover)
	state = result.NextState()

	result, err = strategy.Evaluate(context.Background(), input(t, strategy, enabled, state, signal, execution, descending(50), 240))
	if err != nil || result.Kind() != strategymodel.ResultTradeProposal {
		t.Fatalf("exit = %s, %v", result.Kind(), err)
	}
	proposal, _ = result.Proposal()
	if proposal.Legs[0].Side != domain.SideSell || proposal.RationaleCode != "EMA_BEARISH_EXIT" {
		t.Fatalf("exit proposal = %#v", proposal)
	}
}

func TestConfigurationFailsClosed(t *testing.T) {
	t.Parallel()
	strategy, enabled, _, _, _ := fixture(t)
	var raw map[string]any
	if err := json.Unmarshal(enabled.CanonicalJSON(), &raw); err != nil {
		t.Fatal(err)
	}
	checks := []func(map[string]any){
		func(v map[string]any) { v["execution_instrument"] = "" },
		func(v map[string]any) { v["fast_ema_period"] = 19 },
		func(v map[string]any) { v["quantity_lots"] = 2 },
		func(v map[string]any) { v["allowed_session_regimes"] = []string{"CAS_ACTIVE"} },
		func(v map[string]any) { v["calculation_policy"] = "float64" },
	}
	for index, mutate := range checks {
		copyMap := map[string]any{}
		for key, value := range raw {
			copyMap[key] = value
		}
		mutate(copyMap)
		encoded, _ := json.Marshal(copyMap)
		configuration, _ := strategymodel.NewStrategyConfiguration(ConfigurationSchema, encoded)
		if !errors.Is(strategy.ValidateConfiguration(configuration), ErrInvalidConfiguration) {
			t.Fatalf("invalid configuration %d accepted", index)
		}
	}
}

func TestCancellationAndOverflowFailClosed(t *testing.T) {
	t.Parallel()
	strategy, enabled, _, signal, execution := fixture(t)
	state, _ := strategy.InitialState(enabled)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := strategy.Evaluate(ctx, input(t, strategy, enabled, state, signal, execution, descending(50), 0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	values := make([]int64, 50)
	for index := range values {
		values[index] = math.MaxInt64/Scale + 1
	}
	_, err = calculate(candles(t, signal, values, 0), 20)
	if err == nil {
		t.Fatal("fixed-point overflow accepted")
	}
}

func TestDeterministicDatasetSecondReplayHasIdenticalProposalIDsAndState(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../../tests/testdata/strategy/phase8-ema-replay.json")
	if err != nil {
		t.Fatal(err)
	}
	var dataset struct {
		Schema string `json:"schema_version"`
		Steps  []struct {
			Name   string `json:"name"`
			Start  int64  `json:"start_minor"`
			Step   int64  `json:"step_minor"`
			Count  int    `json:"count"`
			Offset int    `json:"offset_seconds"`
		} `json:"steps"`
	}
	if json.Unmarshal(raw, &dataset) != nil || dataset.Schema != "phase-8-ema-replay/v1" || len(dataset.Steps) != 4 {
		t.Fatal("invalid replay dataset")
	}
	replay := func() []string {
		strategy, enabled, _, signal, execution := fixture(t)
		state, createErr := strategy.InitialState(enabled)
		if createErr != nil {
			t.Fatal(createErr)
		}
		instanceID, _ := domain.NewStrategyID("phase8-replay-instance")
		instance, createErr := strategymodel.NewStrategyInstance(instanceID, strategy.Descriptor(), enabled, 1, strategymodel.LifecycleCandidate)
		if createErr != nil {
			t.Fatal(createErr)
		}
		var evidence []string
		for _, step := range dataset.Steps {
			prices := make([]int64, step.Count)
			for index := range prices {
				prices[index] = step.Start + int64(index)*step.Step
			}
			inputValue := input(t, strategy, enabled, state, signal, execution, prices, step.Offset)
			result, evaluateErr := strategy.Evaluate(context.Background(), inputValue)
			if evaluateErr != nil {
				t.Fatal(evaluateErr)
			}
			state = result.NextState()
			entry := step.Name + "|" + string(result.Kind()) + "|" + state.Hash().String()
			if draft, ok := result.Proposal(); ok {
				evaluationID, _ := strategymodel.NewEvaluationID("phase8-replay|" + inputValue.Frame.ID().String())
				proposal, proposalErr := strategymodel.NewTradeProposal(strategymodel.ProposalMetadata{DefinitionID: instance.DefinitionID(), VersionID: instance.VersionID(), InstanceID: instance.ID(), InstanceRevisionID: instance.RevisionID(), EvaluationID: evaluationID, FrameID: inputValue.Frame.ID(), GeneratedAt: inputValue.LogicalTime, SourceEventIDs: inputValue.Frame.SourceEventIDs(), RequiredInstrumentIDs: []domain.InstrumentID{signal, execution}}, draft)
				if proposalErr != nil {
					t.Fatal(proposalErr)
				}
				entry += "|" + proposal.ID().String()
			} else {
				noAction, _ := result.NoAction()
				entry += "|" + string(noAction.Reason)
			}
			evidence = append(evidence, entry)
		}
		return evidence
	}
	first, second := replay(), replay()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("replays differ:\n%v\n%v", first, second)
	}
	if len(first) != 4 || first[1] == first[2] {
		t.Fatalf("unexpected replay evidence: %v", first)
	}
}

func TestCanonicalFrameRejectsReorderAndStaleExecutionSeries(t *testing.T) {
	t.Parallel()
	strategy, _, _, signal, execution := fixture(t)
	byRole := map[string]strategymodel.InputSubscription{}
	for _, subscription := range strategy.Descriptor().Subscriptions.Subscriptions() {
		byRole[subscription.Role] = subscription
	}
	reordered := candles(t, signal, []int64{100, 200}, 0)
	reordered[0], reordered[1] = reordered[1], reordered[0]
	if _, err := strategymodel.NewCandleSeries(byRole["signal"], reordered); err == nil {
		t.Fatal("out-of-order signal series accepted")
	}
	signalCandles := candles(t, signal, ascending(50), 0)
	executionCandles := candles(t, execution, []int64{25000}, 0)
	signalSeries, _ := strategymodel.NewCandleSeries(byRole["signal"], signalCandles)
	executionSeries, _ := strategymodel.NewCandleSeries(byRole["execution"], executionCandles)
	trigger, _ := strategymodel.NewTriggerID("stale-execution-frame")
	_, err := strategymodel.NewCandleFrame(strategymodel.CandleFrameSpec{TriggerID: trigger, LogicalTime: signalCandles[len(signalCandles)-1].CloseTime(), Subscription: strategy.Descriptor().Subscriptions, Series: []strategymodel.CandleSeries{signalSeries, executionSeries}, MasterVersion: "phase8-master/v1", CalendarVersion: "phase8-calendar/v1", DatasetRevision: "phase8-dataset/v1"})
	if err == nil {
		t.Fatal("stale execution series accepted")
	}
}

func fixture(t *testing.T) (*Strategy, strategymodel.StrategyConfiguration, strategymodel.StrategyConfiguration, domain.InstrumentID, domain.InstrumentID) {
	t.Helper()
	signal, execution := instrument(t, "nifty-signal"), instrument(t, "niftybees-execution")
	strategy, err := New(signal, execution, marketmodel.Interval1Minute, 64, 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	makeConfig := func(enabled bool) strategymodel.StrategyConfiguration {
		raw := []byte(fmt.Sprintf(`{"strategy_id":"%s","version":"1","enabled":%t,"signal_instrument":"%s","execution_instrument":"%s","timeframe":"1m","fast_ema_period":20,"slow_ema_period":50,"minimum_warmup_samples":50,"freshness_threshold_seconds":90,"allowed_session_regimes":["NORMAL_TRADING"],"cooldown_seconds":0,"max_simultaneous_position_intent":1,"quantity_lots":1,"sizing_bps":1000,"exit_rule":"BEARISH_CROSSOVER_OR_EOD_CLOSE","calculation_policy":"%s"}`, DefinitionName, enabled, signal, execution, CalculationPolicy))
		value, createErr := strategymodel.NewStrategyConfiguration(ConfigurationSchema, raw)
		if createErr != nil {
			t.Fatal(createErr)
		}
		return value
	}
	return strategy, makeConfig(true), makeConfig(false), signal, execution
}

func input(t *testing.T, strategy *Strategy, cfg strategymodel.StrategyConfiguration, state strategymodel.StrategyRuntimeState, signal, execution domain.InstrumentID, prices []int64, offsetSeconds int) strategymodel.EvaluationContext {
	t.Helper()
	signalCandles := candles(t, signal, prices, offsetSeconds)
	executionCandles := candles(t, execution, []int64{25000}, offsetSeconds+len(prices)*60-60)
	byRole := map[string]strategymodel.InputSubscription{}
	for _, subscription := range strategy.Descriptor().Subscriptions.Subscriptions() {
		byRole[subscription.Role] = subscription
	}
	signalSeries, err := strategymodel.NewCandleSeries(byRole["signal"], signalCandles)
	if err != nil {
		t.Fatal(err)
	}
	executionSeries, err := strategymodel.NewCandleSeries(byRole["execution"], executionCandles)
	if err != nil {
		t.Fatal(err)
	}
	logical := signalCandles[len(signalCandles)-1].CloseTime()
	trigger, _ := strategymodel.NewTriggerID("phase8-test|" + signalCandles[len(signalCandles)-1].ID().String())
	frame, err := strategymodel.NewCandleFrame(strategymodel.CandleFrameSpec{TriggerID: trigger, LogicalTime: logical, Subscription: strategy.Descriptor().Subscriptions, Series: []strategymodel.CandleSeries{signalSeries, executionSeries}, MasterVersion: "phase8-master/v1", CalendarVersion: "phase8-calendar/v1", DatasetRevision: "phase8-dataset/v1"})
	if err != nil {
		t.Fatal(err)
	}
	return strategymodel.EvaluationContext{Configuration: cfg, PriorState: state, Frame: frame, LogicalTime: logical}
}

func candles(t *testing.T, id domain.InstrumentID, values []int64, offsetSeconds int) []marketmodel.CompletedCandleEvent {
	t.Helper()
	start := time.Date(2026, 8, 10, 3, 45, offsetSeconds, 0, time.UTC)
	result := make([]marketmodel.CompletedCandleEvent, len(values))
	for index, value := range values {
		price, err := domain.NewPrice(value, "INR")
		if err != nil {
			t.Fatal(err)
		}
		open := start.Add(time.Duration(index) * time.Minute)
		result[index], err = marketmodel.NewCompletedCandleEvent(marketmodel.CandleSpec{InstrumentID: id, Interval: marketmodel.Interval1Minute, OpenTime: open, CloseTime: open.Add(time.Minute), Open: price, High: price, Low: price, Close: price, Volume: 1, EventCount: 1, IngestedAt: open.Add(time.Minute), Provenance: marketmodel.Provenance{Provider: "phase8-replay", ProviderToken: "fixture", MasterVersion: "phase8-master/v1", DatasetRevision: "phase8-dataset/v1"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}
func instrument(t *testing.T, key string) domain.InstrumentID {
	t.Helper()
	id, err := domain.InstrumentIDFromCanonicalKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func ascending(size int) []int64 {
	result := make([]int64, size)
	for i := range result {
		result[i] = int64(100 + i*10)
	}
	return result
}
func descending(size int) []int64 {
	result := make([]int64, size)
	for i := range result {
		result[i] = int64(1000 - i*10)
	}
	return result
}
func assertNoAction(t *testing.T, result strategymodel.EvaluationResult, err error, want strategymodel.NoActionReason) {
	t.Helper()
	if err != nil || result.Kind() != strategymodel.ResultNoAction {
		t.Fatalf("result = %s, %v", result.Kind(), err)
	}
	value, _ := result.NoAction()
	if value.Reason != want {
		t.Fatalf("reason = %s, want %s", value.Reason, want)
	}
}
