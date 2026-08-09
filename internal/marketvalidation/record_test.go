package marketvalidation

import (
	"strings"
	"testing"
	"time"
)

func TestFinalizeDerivesValidityWithoutUsingProfit(t *testing.T) {
	value := validRecord("2026-08-10", "PAPER")
	value.ReleaseCommit = strings.Repeat("b", 40)
	value.Financial.TotalPnL = Money{Availability: "KNOWN", Minor: 999999, Currency: "INR"}
	value.Operations.CleanCheckpoint = false
	got, err := Finalize(value)
	if err != nil {
		t.Fatal(err)
	}
	if got.FinalStatus != StatusInvalid || !contains(got.StatusReasons, "CLEAN_CHECKPOINT_MISSING") {
		t.Fatalf("status = %s, reasons = %v", got.FinalStatus, got.StatusReasons)
	}
	if Verify(got) != nil {
		t.Fatal("finalized record did not verify")
	}
}

func TestFinalizeRejectsMissingMandatoryEvidenceAndSensitivePaths(t *testing.T) {
	value := validRecord("2026-08-10", "PAPER")
	value.Operations.MandatoryEvidenceComplete = false
	got, err := Finalize(value)
	if err != nil || got.FinalStatus != StatusInvalid || !contains(got.StatusReasons, "MANDATORY_EVIDENCE_MISSING") {
		t.Fatalf("Finalize() = %#v, %v", got, err)
	}
	value = validRecord("2026-08-10", "PAPER")
	value.Evidence[0].Path = "access_token.json"
	if _, err := Finalize(value); err == nil {
		t.Fatal("sensitive evidence path accepted")
	}
}

func TestFinalizeClassifiesBoundedIncidents(t *testing.T) {
	value := validRecord("2026-08-10", "PAPER")
	value.MarketData.Disconnects = 1
	value.MarketData.Reconnects = 1
	got, err := Finalize(value)
	if err != nil || got.FinalStatus != StatusValidWithIncidents {
		t.Fatalf("Finalize() status = %s, err = %v", got.FinalStatus, err)
	}
}

func TestFinalizeRequiresCompleteCASSequence(t *testing.T) {
	value := validRecord("2026-08-10", "PAPER")
	value.CAS.Expected = true
	value.CAS.EvidenceRecords = 1
	value.CAS.RegimesObserved = []string{"CAS_ACTIVE"}
	got, err := Finalize(value)
	if err != nil || got.FinalStatus != StatusInvalid || !contains(got.StatusReasons, "CAS_EVIDENCE_INCOMPLETE") {
		t.Fatalf("Finalize() = %#v, %v", got, err)
	}
}

func TestBuildScorecardSeparatesValidityFromPnL(t *testing.T) {
	var records []Record
	for i := 0; i < 10; i++ {
		mode := "PAPER"
		if i >= 5 {
			mode = "SHADOW"
		}
		value := validRecord(time.Date(2026, 8, 10+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), mode)
		value.Trading.PaperExecutions = 3
		if mode == "SHADOW" {
			value.Trading.PaperExecutions = 0
			value.Trading.ShadowExecutions = 3
		}
		final, err := Finalize(value)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, final)
	}
	card, err := BuildScorecard(records)
	if err != nil {
		t.Fatal(err)
	}
	if !card.MinimumProgramComplete || !card.ExecutionSampleSufficient || card.LiveTradingAuthorized {
		t.Fatalf("scorecard gates = %#v", card)
	}
	invalid := validRecord("2026-08-20", "PAPER")
	invalid.Financial.TotalPnL.Minor = 1000000
	invalid.Execution.Unknown = 1
	final, _ := Finalize(invalid)
	if final.FinalStatus != StatusInvalid {
		t.Fatalf("positive-PnL invalid record became %s", final.FinalStatus)
	}
}

func validRecord(date, mode string) Record {
	digest := strings.Repeat("a", 64)
	return Record{
		Date: date, Mode: mode, Scope: ScopeFullPipeline, ReleaseCommit: digest,
		Versions:   Versions{CalendarVersion: "calendar/v1", MappingVersion: "mapping/v1", WatchlistVersion: "watchlist/v1", StrategyVersion: "candidate/v1", PortfolioConfigHash: digest, RiskConfigurationHash: digest},
		Operations: Operations{StartupResult: "PASS", ReadyAt: time.Date(2026, 8, 10, 3, 45, 0, 0, time.UTC), ShutdownResult: "PASS", CleanCheckpoint: true, CalendarAndSessionVerified: true, KillSwitchVerified: true, MandatoryEvidenceComplete: true},
		Trading:    Trading{StrategiesActive: 1},
		Financial:  Financial{Status: "COMPLETE", RealizedPnL: Money{Availability: "KNOWN", Currency: "INR"}, UnrealizedPnL: Money{Availability: "KNOWN", Currency: "INR"}, TotalPnL: Money{Availability: "KNOWN", Currency: "INR"}, MaxDrawdown: Money{Availability: "KNOWN", Currency: "INR"}, ByStrategy: []StrategyPnL{}},
		Execution:  Execution{SlippageModel: "OBSERVED_BEST_PRICE_NO_ADDITIONAL_SLIPPAGE"},
		MarketData: MarketData{ReadinessAvailability: 10000},
		Evidence:   []EvidenceReference{{Kind: "runtime", Path: "raw/runtime.json", SHA256: digest}},
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
