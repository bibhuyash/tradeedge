package checkpointfile

import (
	"context"
	"errors"
	"testing"
	"time"

	platform "github.com/bibhuyash/tradeedge/internal/platform/checkpointfile"
	"github.com/bibhuyash/tradeedge/internal/qualification"
)

func TestAtomicShadowCheckpointRoundTripAndCAS(t *testing.T) {
	at := time.Date(2026, 8, 17, 4, 30, 0, 0, time.UTC)
	engine, _ := qualification.New(qualification.DefaultPolicy(), nil)
	_, err := engine.RecordSignal(qualification.SignalInput{
		StrategyID: qualification.StrategyID, StrategyVersion: qualification.StrategyVersion,
		Underlying: qualification.NIFTY, SignalID: "checkpoint-1", SignalTime: at,
		MarketSession: "NORMAL_TRADING", CASState: "PERMITTED", SpotMinor: 2_480_000, SpotTime: at.Add(-time.Second),
		FutureID: "nifty-future", FutureExpiry: "2026-08-25", FutureMinor: 2_482_000, FutureTime: at.Add(-time.Second),
		OptionID: "nifty-option", OptionExpiry: "2026-08-18", StrikeMinor: 2_480_000, OptionType: "CALL",
		OptionQuote: qualification.Quote{InstrumentID: "nifty-option", BidMinor: 9_900, AskMinor: 10_100, LTPMinor: 10_000, ObservedAt: at.Add(-time.Second)},
		EMA20Scaled: 2_481_000_000_000, EMA50Scaled: 2_479_000_000_000, WarmupComplete: true, Fresh: true,
		Direction: qualification.DirectionLong, Risk: qualification.RiskApproved, RiskReason: "ALL_RULES_PASSED", Quantity: 65,
		RegimeInput: qualification.RegimeInput{SpotMinor: 2_480_000, EMA20Scaled: 2_481_000_000_000, EMA50Scaled: 2_479_000_000_000, RecentRangeMinor: 20_000, BaselineRangeMinor: 10_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configChecksum := "66e4dcb9c1ab337d68c543ab6a41b1f44e22a6a335213913771e3f25ed2b545d"
	written, err := store.Publish(context.Background(), engine.Snapshot(), 0, "nse-calendar/v1", configChecksum, at, false)
	if err != nil {
		t.Fatal(err)
	}
	loaded, generation, err := store.Load(context.Background())
	if err != nil || generation.Mode != "SHADOW" || loaded.Checksum != engine.Snapshot().Checksum {
		t.Fatalf("load: %#v %v", generation, err)
	}
	if _, err = store.Publish(context.Background(), engine.Snapshot(), 0, "nse-calendar/v1", configChecksum, at, false); !errors.Is(err, platform.ErrConflict) {
		t.Fatalf("stale CAS accepted after %#v: %v", written, err)
	}
}
