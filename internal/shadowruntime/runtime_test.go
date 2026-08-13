package shadowruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"context"
	"github.com/bibhuyash/tradeedge/internal/derivatives"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/qualification"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
)

var fixtureAt = time.Date(2026, 8, 18, 3, 45, 0, 0, time.UTC)

func TestCompletedCandlesArePartitionedDeterministicAndRestartEquivalent(t *testing.T) {
	nifty := testIndex(t, "NIFTY", "NIFTY 50")
	bank := testIndex(t, "BANKNIFTY", "NIFTY BANK")
	spots := map[qualification.Underlying]domain.InstrumentID{qualification.NIFTY: nifty.ID(), qualification.BANKNIFTY: bank.ID()}
	continuous, _ := NewCandleAggregator(spots)
	restarted, _ := NewCandleAggregator(spots)
	quotes := []struct {
		underlying qualification.Underlying
		instrument domain.Instrument
		at         time.Time
		price      int64
	}{
		{qualification.NIFTY, nifty, fixtureAt, 2_480_000},
		{qualification.BANKNIFTY, bank, fixtureAt.Add(10 * time.Second), 5_210_000},
		{qualification.NIFTY, nifty, fixtureAt.Add(30 * time.Second), 2_481_000},
		{qualification.BANKNIFTY, bank, fixtureAt.Add(time.Minute + 10*time.Second), 5_211_000},
		{qualification.NIFTY, nifty, fixtureAt.Add(time.Minute), 2_482_000},
	}
	for index, value := range quotes {
		quote := testQuote(t, value.instrument, value.at, value.price)
		_, err := continuous.Accept(value.underlying, quote)
		if err != nil {
			t.Fatal(err)
		}
		if index == 2 {
			snapshot := continuous.Snapshot()
			if err = restarted.Restore(snapshot); err != nil {
				t.Fatal(err)
			}
		} else if index > 2 {
			if _, err = restarted.Accept(value.underlying, quote); err != nil {
				t.Fatal(err)
			}
		}
	}
	left, _ := json.Marshal(continuous.Snapshot())
	right, _ := json.Marshal(restarted.Snapshot())
	if !bytes.Equal(left, right) {
		t.Fatalf("restart diverged\n%s\n%s", left, right)
	}
	if got := continuous.series[qualification.NIFTY].Completed[0]; got.CloseMinor != 2_481_000 || got.CloseTime != fixtureAt.Add(time.Minute) {
		t.Fatalf("wrong completed candle: %#v", got)
	}
	duplicate := testQuote(t, nifty, fixtureAt.Add(time.Minute), 2_482_000)
	if _, err := continuous.Accept(qualification.NIFTY, duplicate); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate = %v", err)
	}
	late := testQuote(t, nifty, fixtureAt.Add(45*time.Second), 2_470_000)
	if _, err := continuous.Accept(qualification.NIFTY, late); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("late = %v", err)
	}
}

func TestEMAReferenceWarmupCompletedOnlyBothUnderlyingsAndReplay(t *testing.T) {
	for _, underlying := range []qualification.Underlying{qualification.NIFTY, qualification.BANKNIFTY} {
		candles := make([]CandlePoint, 0, 52)
		state := EMAState{Underlying: underlying}
		for index := 0; index < 50; index++ {
			candles = append(candles, candle(underlying, index, 10_000_00-int64(index)*100))
			var signal *EMASignal
			var err error
			state, signal, err = EvaluateEMA(state, candles)
			if err != nil || signal != nil {
				t.Fatalf("warmup %s/%d = %#v %v", underlying, index, signal, err)
			}
		}
		candles = append(candles, candle(underlying, 50, 20_000_00))
		state, signal, err := EvaluateEMA(state, candles)
		if err != nil || signal == nil || signal.Direction != qualification.DirectionLong {
			t.Fatalf("long crossover = %#v %v", signal, err)
		}
		replayed := EMAState{Underlying: underlying}
		for index := 0; index < len(candles); index++ {
			replayed, _, err = EvaluateEMA(replayed, candles[:index+1])
			if err != nil {
				t.Fatal(err)
			}
		}
		if replayed != state {
			t.Fatalf("EMA replay diverged: %#v %#v", replayed, state)
		}
	}
}

func TestSessionScorecardsNeverMergeUnderlyingsAndNetPnLUnavailable(t *testing.T) {
	engine, _ := qualification.New(qualification.DefaultPolicy(), nil)
	tracker, err := NewSessionTracker("2026-08-18", fixtureAt, engine.Scorecards(), false)
	if err != nil {
		t.Fatal(err)
	}
	tracker.AddReason(qualification.BANKNIFTY, ReasonTelegramOutage)
	if err = tracker.Close(fixtureAt.Add(6*time.Hour), engine.Scorecards(), engine.Snapshot(), true); err != nil {
		t.Fatal(err)
	}
	if len(tracker.Closed) != 2 || tracker.Closed[0].Underlying == tracker.Closed[1].Underlying {
		t.Fatalf("partition failure: %#v", tracker.Closed)
	}
	for _, card := range tracker.Closed {
		if card.NetPnL != "NOT_AVAILABLE" || card.Checksum == "" {
			t.Fatalf("invalid immutable scorecard: %#v", card)
		}
	}
	multi := tracker.Multi(engine.Scorecards())
	if len(multi) != 2 || multi[0].Underlying == multi[1].Underlying {
		t.Fatalf("multi-session merge: %#v", multi)
	}
}

type unusedRisk struct{}

func (unusedRisk) Evaluate(context.Context, derivatives.ConnectedRequest) (riskmodel.PortfolioRiskDecision, error) {
	return riskmodel.PortfolioRiskDecision{}, errors.New("unexpected risk")
}

func TestRuntimeFullCheckpointRestoresWarmupWithoutDuplicate(t *testing.T) {
	nifty := testIndex(t, "NIFTY", "NIFTY 50")
	bank := testIndex(t, "BANKNIFTY", "NIFTY BANK")
	master, err := instrumentmaster.New(fixtureAt.Add(-time.Hour), []domain.Instrument{nifty, bank}, nil)
	if err != nil {
		t.Fatal(err)
	}
	spots := map[qualification.Underlying]domain.InstrumentID{qualification.NIFTY: nifty.ID(), qualification.BANKNIFTY: bank.ID()}
	niftyPolicy, _ := derivatives.PolicyFor("NIFTY")
	bankPolicy, _ := derivatives.PolicyFor("BANKNIFTY")
	policies := map[qualification.Underlying]derivatives.Policy{qualification.NIFTY: niftyPolicy, qualification.BANKNIFTY: bankPolicy}
	engine, _ := qualification.New(qualification.DefaultPolicy(), nil)
	runtime, err := New(RuntimeConfig{Master: master, SpotIDs: spots, Policies: policies, Qualification: engine, Risk: unusedRisk{}, TradingDate: "2026-08-18", StartedAt: fixtureAt})
	if err != nil {
		t.Fatal(err)
	}
	quote := testQuote(t, nifty, fixtureAt, 2_480_000)
	if err = runtime.Process(context.Background(), quote, "NORMAL_TRADING", false, false); err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.Snapshot()
	restartedEngine, _ := qualification.New(qualification.DefaultPolicy(), nil)
	restarted, err := New(RuntimeConfig{Master: master, SpotIDs: spots, Policies: policies, Qualification: restartedEngine, Risk: unusedRisk{}, TradingDate: "2026-08-18", StartedAt: fixtureAt})
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Restore(snapshot); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Snapshot(); got.Revision != snapshot.Revision || got.Candles[0].LastEventID != snapshot.Candles[0].LastEventID {
		t.Fatalf("restore mismatch: %#v %#v", got, snapshot)
	}
	if err = restarted.Process(context.Background(), quote, "NORMAL_TRADING", false, false); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("restart duplicate = %v", err)
	}
}

func candle(underlying qualification.Underlying, index int, price int64) CandlePoint {
	open := fixtureAt.Add(time.Duration(index) * time.Minute)
	return CandlePoint{EventID: identity(string(underlying), open.String()), InstrumentID: string(underlying), OpenTime: open, CloseTime: open.Add(time.Minute), OpenMinor: price, HighMinor: price + 100, LowMinor: price - 100, CloseMinor: price, Ticks: 1, LastExchangeAt: open.Add(30 * time.Second), Underlying: underlying}
}

func testIndex(t *testing.T, underlying, symbol string) domain.Instrument {
	t.Helper()
	u, _ := domain.NewUnderlyingID(underlying)
	lot, _ := domain.NewQuantity(1)
	tick, _ := domain.NewPrice(5, "INR")
	value, err := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentIndex, UnderlyingID: u, Type: domain.InstrumentIndex, ExchangeSymbol: symbol, LotSize: lot, TickSize: tick, Currency: "INR"})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func testQuote(t *testing.T, instrument domain.Instrument, at time.Time, minor int64) marketmodel.QuoteEvent {
	t.Helper()
	price, _ := domain.NewPrice(minor, "INR")
	value, err := marketmodel.NewQuoteEvent(marketmodel.QuoteSpec{InstrumentID: instrument.ID(), LastPrice: price, Volume: 1, ExchangeTime: at, IngestedAt: at.Add(time.Millisecond), Provenance: marketmodel.Provenance{Provider: "zerodha", ProviderToken: instrument.ID().String()[:8], MasterVersion: "test-master"}})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
