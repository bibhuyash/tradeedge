package cas

import (
	"testing"
	"time"
)

func TestDeterministicEvidencePreservesUnavailableProvenance(t *testing.T) {
	at := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	spec := Spec{TradingDate: "2026-08-10", InstrumentID: "NSE|INDEX|NIFTY", StrategyID: "fixture", RuntimeMode: "SHADOW", Regime: "CAS_ACTIVE", PreCAS: KnownPrice(10000, "INR", "LAST_TRADED_PRICE", "event", at), LatestEligibleLTP: KnownPrice(10000, "INR", "LAST_TRADED_PRICE", "event", at), CASIndicative: UnavailablePrice("SOURCE_UNAVAILABLE"), CASReference: UnavailablePrice("SOURCE_UNAVAILABLE"), CASEquilibrium: UnavailablePrice("SOURCE_UNAVAILABLE"), OfficialClose: UnavailablePrice("NOT_PUBLISHED"), StrategyEligibility: KnownValue("RESTRICTED", "strategy", at), Proposal: UnavailableValue("CAS_RESTRICTED"), RiskOutcome: UnavailableValue("NO_PROPOSAL"), ShadowDecision: UnavailableValue("NO_PROPOSAL"), PaperExecution: UnavailableValue("MODE_SHADOW"), PositionBefore: UnavailableValue("NOT_CAPTURED"), PositionAfter: UnavailableValue("SESSION_OPEN"), BlockedReason: KnownValue("CAS_RESTRICTED", "strategy", at), Readiness: KnownValue("READY", "readiness", at), ConfigurationVersion: "v1", ConfigurationChecksum: "config", CalendarVersion: "calendar", UpdatedAt: at}
	a, err := NewRecord(spec)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewRecord(spec)
	if a.Checksum != b.Checksum || a.CASIndicative.Availability != Unavailable || a.PreCAS.Provenance != "LAST_TRADED_PRICE" {
		t.Fatalf("evidence changed: %+v", a)
	}
	store, _ := NewStore(1)
	_ = store.Put(a)
	spec.InstrumentID = "NSE|INDEX|BANKNIFTY"
	c, _ := NewRecord(spec)
	_ = store.Put(c)
	if got := store.Recent(100); len(got) != 1 || got[0].ID != c.ID {
		t.Fatal("store not bounded")
	}
}
