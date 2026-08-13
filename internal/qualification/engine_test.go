package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/notification"
)

var qualificationAt = time.Date(2026, 8, 17, 4, 30, 0, 0, time.UTC)

func TestIndependentNIFTYBANKNIFTYReplayScorecardsAndNoExecutionState(t *testing.T) {
	first := replayBoth(t)
	second := replayBoth(t)
	left, _ := json.Marshal(first.Snapshot())
	right, _ := json.Marshal(second.Snapshot())
	if !bytes.Equal(left, right) {
		t.Fatal("qualification replay diverged")
	}
	cards := first.Scorecards()
	if len(cards) != 2 || cards[0].Underlying != NIFTY || cards[1].Underlying != BANKNIFTY {
		t.Fatalf("scorecards not independently ordered: %#v", cards)
	}
	for _, card := range cards {
		if card.Signals != 1 || card.CompletedTrades != 1 || card.Wins != 1 || card.GrossPnLMinor <= 0 || card.State != StateEligibleForReview || card.NetPnLAvailable {
			t.Fatalf("bad scorecard: %#v", card)
		}
		if card.DirectionalOutcomeBPS["+1m"] != 10_000 || card.DirectionalOutcomeBPS["+30m"] != 10_000 {
			t.Fatalf("missing horizons: %#v", card.DirectionalOutcomeBPS)
		}
	}
	if first.Snapshot().Series[0].Open != nil || first.Snapshot().Series[1].Open != nil {
		t.Fatal("shadow evidence exposed execution position")
	}
}

func TestCheckedInReplayDatasetsAreExact(t *testing.T) {
	type dataset struct {
		SchemaVersion    string     `json:"schema_version"`
		Underlying       Underlying `json:"underlying"`
		SignalID         string     `json:"signal_id"`
		SignalTime       time.Time  `json:"signal_time"`
		OptionMarksMinor []int64    `json:"option_marks_minor"`
		ExitMarkMinor    int64      `json:"exit_mark_minor"`
	}
	for _, path := range []string{"testdata/nifty-replay-v1.json", "testdata/banknifty-replay-v1.json"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fixture dataset
		if json.Unmarshal(raw, &fixture) != nil || fixture.SchemaVersion != "phase8-m3-qualification-replay/v1" || len(fixture.OptionMarksMinor) != 4 {
			t.Fatalf("invalid replay fixture %s", path)
		}
		var snapshots [][]byte
		for run := 0; run < 2; run++ {
			policy := DefaultPolicy()
			policy.MinimumCompletedTrades, policy.MinimumSessions = 1, 1
			engine, _ := New(policy, nil)
			input := signal(fixture.Underlying, fixture.SignalID, fixture.SignalTime)
			record, recordErr := engine.RecordSignal(input)
			if recordErr != nil {
				t.Fatal(recordErr)
			}
			for index, horizon := range policy.Horizons {
				if err = engine.Observe(observation(input, record, horizon, fixture.OptionMarksMinor[index])); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = engine.Close(ExitInput{Underlying: fixture.Underlying, QualificationID: record.QualificationID, OptionID: input.OptionID, SignalTime: fixture.SignalTime.Add(31 * time.Minute), OptionQuote: quote(input.OptionID, fixture.ExitMarkMinor, fixture.SignalTime.Add(31*time.Minute))}); err != nil {
				t.Fatal(err)
			}
			snapshot, _ := json.Marshal(engine.Snapshot())
			snapshots = append(snapshots, snapshot)
		}
		if !bytes.Equal(snapshots[0], snapshots[1]) {
			t.Fatalf("%s diverged", path)
		}
	}
}

func TestForwardHorizonMFE_MAEAndSameOptionExit(t *testing.T) {
	policy := DefaultPolicy()
	policy.MinimumCompletedTrades = 1
	policy.MinimumSessions = 1
	engine, _ := New(policy, nil)
	input := signal(BANKNIFTY, "bank-1", qualificationAt)
	record, err := engine.RecordSignal(input)
	if err != nil {
		t.Fatal(err)
	}
	prices := []int64{10_300, 9_800, 10_500, 10_400}
	for index, horizon := range policy.Horizons {
		at := qualificationAt.Add(horizon)
		err = engine.Observe(Observation{Underlying: BANKNIFTY, QualificationID: record.QualificationID, ObservedAt: at, SpotMinor: input.SpotMinor + int64(index+1)*100, FutureMinor: input.FutureMinor + int64(index+1)*120, OptionQuote: quote(input.OptionID, prices[index], at), Quality: QualityComplete})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = engine.Close(ExitInput{Underlying: BANKNIFTY, QualificationID: record.QualificationID, OptionID: "different-option", SignalTime: qualificationAt.Add(31 * time.Minute), OptionQuote: quote("different-option", 10_400, qualificationAt.Add(31*time.Minute))}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("different strike exit accepted: %v", err)
	}
	trade, err := engine.Close(ExitInput{Underlying: BANKNIFTY, QualificationID: record.QualificationID, OptionID: input.OptionID, SignalTime: qualificationAt.Add(31 * time.Minute), OptionQuote: quote(input.OptionID, 10_400, qualificationAt.Add(31*time.Minute))})
	if err != nil {
		t.Fatal(err)
	}
	if trade.MFEChangeMinor != 400 || trade.MAEChangeMinor != -300 || trade.GrossPnLMinor != 4_500 {
		t.Fatalf("bad excursions or pnl: %#v", trade)
	}
}

func TestCheckpointRestartContinuationIsExact(t *testing.T) {
	policy := DefaultPolicy()
	policy.MinimumCompletedTrades = 1
	policy.MinimumSessions = 1
	control, _ := New(policy, nil)
	input := signal(NIFTY, "nifty-restart", qualificationAt)
	record, _ := control.RecordSignal(input)
	_ = control.Observe(observation(input, record, policy.Horizons[0], 10_300))
	snapshot := control.Snapshot()
	restarted, _ := New(policy, nil)
	if err := restarted.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	var err error
	for _, engine := range []*Engine{control, restarted} {
		for index := 1; index < len(policy.Horizons); index++ {
			if err = engine.Observe(observation(input, record, policy.Horizons[index], 10_300+int64(index)*100)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = engine.Close(ExitInput{Underlying: NIFTY, QualificationID: record.QualificationID, OptionID: input.OptionID, SignalTime: qualificationAt.Add(31 * time.Minute), OptionQuote: quote(input.OptionID, 10_600, qualificationAt.Add(31*time.Minute))}); err != nil {
			t.Fatal(err)
		}
	}
	left, _ := json.Marshal(control.Snapshot())
	right, _ := json.Marshal(restarted.Snapshot())
	if !bytes.Equal(left, right) {
		t.Fatal("restart continuation diverged")
	}
}

func TestAdversarialBlocksDuplicatesUnavailableAndNotificationOutage(t *testing.T) {
	policy := DefaultPolicy()
	engine, _ := New(policy, panicQualificationObserver{})
	input := signal(NIFTY, "duplicate", qualificationAt)
	record, err := engine.RecordSignal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.RecordSignal(input); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate got %v", err)
	}
	conflict := input
	conflict.OptionQuote.AskMinor++
	if _, err = engine.RecordSignal(conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("identity conflict got %v", err)
	}
	if err = engine.Observe(Observation{Underlying: NIFTY, QualificationID: record.QualificationID, ObservedAt: qualificationAt.Add(time.Minute), SpotMinor: input.SpotMinor, FutureMinor: input.FutureMinor, OptionQuote: quote("wrong-option", 10_100, qualificationAt.Add(time.Minute)), Quality: QualityComplete}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong quote got %v", err)
	}
	valid := observation(input, record, time.Minute, 10_200)
	if err = engine.Observe(valid); err != nil {
		t.Fatal(err)
	}
	if err = engine.Observe(valid); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate quote got %v", err)
	}
	outOfOrder := valid
	outOfOrder.ObservedAt = valid.ObservedAt.Add(-time.Second)
	outOfOrder.OptionQuote.ObservedAt = outOfOrder.ObservedAt
	if err = engine.Observe(outOfOrder); !errors.Is(err, ErrConflict) {
		t.Fatalf("out-of-order quote got %v", err)
	}
	if err = engine.MarkHorizonUnavailable(NIFTY, record.QualificationID, 5*time.Minute, qualificationAt.Add(6*time.Minute), ReasonRestartGap); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []UnavailableReason{ReasonStaleSpot, ReasonMappingBlocked, ReasonCASBlocked, ReasonSessionBlocked, ReasonControlBlocked, ReasonMissingOption} {
		if err = engine.RecordBlock(BlockInput{NIFTY, reason}); err != nil {
			t.Fatal(err)
		}
	}
	card := engine.Scorecards()[0]
	if card.StaleDataBlocks != 1 || card.MappingBlocks != 1 || card.CASBlocks != 1 || card.SessionBlocks != 1 || card.ControlBlocks != 1 || card.DataQualityFailures != 1 {
		t.Fatalf("bad block counters: %#v", card.BlockCounters)
	}
}

func TestQualificationRequiresExplicitEligibleReview(t *testing.T) {
	engine, _ := New(DefaultPolicy(), nil)
	decision := ReviewDecision{Underlying: NIFTY, Approved: true, Operator: "release-owner", Reference: "approval-1", At: qualificationAt}
	if err := engine.ApplyReview(decision); !errors.Is(err, ErrInvalid) {
		t.Fatalf("premature qualification accepted: %v", err)
	}
	qualified := replayBoth(t)
	if err := qualified.ApplyReview(decision); err != nil {
		t.Fatal(err)
	}
	_, card, _ := qualified.Strategy(StrategyID, NIFTY)
	if card.State != StateQualified {
		t.Fatalf("review transition missing: %s", card.State)
	}
}

func TestConfiguredCostBoundaryReportsNetWithoutFloatingPoint(t *testing.T) {
	policy := DefaultPolicy()
	policy.MinimumCompletedTrades, policy.MinimumSessions = 1, 1
	policy.Cost = CostPolicy{Version: "test-sourced-cost/v1", Source: "test fixture", Configured: true, BrokerageMinor: 100, ExchangeChargesMinor: 20, TaxesAndLeviesMinor: 30, GSTMinor: 10, StampDutyMinor: 5, DeterministicSlippageMinor: 35}
	engine, err := New(policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := signal(NIFTY, "costed", qualificationAt)
	record, _ := engine.RecordSignal(input)
	trade, err := engine.Close(ExitInput{Underlying: NIFTY, QualificationID: record.QualificationID, OptionID: input.OptionID, SignalTime: qualificationAt.Add(time.Minute), OptionQuote: quote(input.OptionID, 10_500, qualificationAt.Add(time.Minute))})
	if err != nil {
		t.Fatal(err)
	}
	if !trade.NetPnLAvailable || trade.NetPnLMinor != trade.GrossPnLMinor-200 {
		t.Fatalf("bad net P&L: %#v", trade)
	}
}

func TestLTPApproximationAndRiskRejectedDoNotOpenShadowPosition(t *testing.T) {
	engine, _ := New(DefaultPolicy(), nil)
	input := signal(BANKNIFTY, "rejected", qualificationAt)
	input.Risk = RiskRejected
	input.RiskReason = "KILL_SWITCH_BLOCKING"
	input.OptionQuote.BidMinor = 0
	input.OptionQuote.AskMinor = 0
	record, err := engine.RecordSignal(input)
	if err != nil {
		t.Fatal(err)
	}
	series, card, _ := engine.Strategy(StrategyID, BANKNIFTY)
	if record.Entry.Source != "LTP_APPROXIMATION" || record.Quality != QualityPartial || series.Open != nil || card.RiskRejectedSignals != 1 {
		t.Fatalf("rejected shadow state: %#v %#v", record, card)
	}
}

func replayBoth(t *testing.T) *Engine {
	t.Helper()
	policy := DefaultPolicy()
	policy.MinimumCompletedTrades = 1
	policy.MinimumSessions = 1
	engine, _ := New(policy, nil)
	for index, underlying := range []Underlying{NIFTY, BANKNIFTY} {
		at := qualificationAt.Add(time.Duration(index) * time.Hour)
		input := signal(underlying, "signal-"+string(underlying), at)
		record, err := engine.RecordSignal(input)
		if err != nil {
			t.Fatal(err)
		}
		for hIndex, horizon := range policy.Horizons {
			if err = engine.Observe(observation(input, record, horizon, 10_200+int64(hIndex)*100)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = engine.Close(ExitInput{Underlying: underlying, QualificationID: record.QualificationID, OptionID: input.OptionID, SignalTime: at.Add(31 * time.Minute), OptionQuote: quote(input.OptionID, 10_500, at.Add(31*time.Minute))}); err != nil {
			t.Fatal(err)
		}
	}
	return engine
}

func signal(underlying Underlying, id string, at time.Time) SignalInput {
	spot := int64(2_479_000)
	future := int64(2_481_200)
	strike := int64(2_480_000)
	lot := int64(65)
	if underlying == BANKNIFTY {
		spot = 5_210_000
		future = 5_214_000
		strike = 5_210_000
		lot = 15
	}
	optionID := "option-" + string(underlying)
	return SignalInput{StrategyID: StrategyID, StrategyVersion: StrategyVersion, Underlying: underlying, SignalID: id, SignalTime: at, MarketSession: "NORMAL_TRADING", CASState: "PERMITTED", SpotMinor: spot, SpotTime: at.Add(-time.Second), FutureID: "future-" + string(underlying), FutureExpiry: "2026-08-25", FutureMinor: future, FutureTime: at.Add(-time.Second), OptionID: optionID, OptionExpiry: "2026-08-18", StrikeMinor: strike, OptionType: "CALL", OptionQuote: Quote{InstrumentID: optionID, BidMinor: 9_900, AskMinor: 10_100, LTPMinor: 10_000, ObservedAt: at.Add(-time.Second)}, EMA20Scaled: spot*1_000_000 + 20_000_000, EMA50Scaled: spot * 1_000_000, WarmupComplete: true, Fresh: true, Direction: DirectionLong, Risk: RiskApproved, RiskReason: "ALL_RULES_PASSED", Quantity: lot, RegimeInput: RegimeInput{SpotMinor: spot, EMA20Scaled: spot*1_000_000 + 20_000_000, EMA50Scaled: spot * 1_000_000, RecentRangeMinor: 20_000, BaselineRangeMinor: 10_000}}
}
func quote(option string, mark int64, at time.Time) Quote {
	return Quote{InstrumentID: option, BidMinor: mark, AskMinor: mark + 100, LTPMinor: mark + 50, ObservedAt: at}
}
func observation(input SignalInput, record SignalRecord, horizon time.Duration, mark int64) Observation {
	at := input.SignalTime.Add(horizon)
	return Observation{Underlying: input.Underlying, QualificationID: record.QualificationID, ObservedAt: at, SpotMinor: input.SpotMinor + 100, FutureMinor: input.FutureMinor + 120, OptionQuote: quote(input.OptionID, mark, at), Quality: QualityComplete}
}

type panicQualificationObserver struct{}

func (panicQualificationObserver) Observe(notification.Event) { panic("telegram unavailable") }
