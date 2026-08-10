package marketvalidation

import (
	"encoding/json"
	"testing"
	"time"
)

func validDay0Evidence() (Day0Evidence, AuthorizationManifest) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	auth := AuthorizationManifest{Checksum: "authorization", TradingDate: "2026-08-10", Scope: ScopeOperationsOnly, Strategy: AuthorizedStrategy{Name: "NONE"}}
	evidence := Day0Evidence{SchemaVersion: Day0EvidenceSchemaVersion, TradingDate: auth.TradingDate, AuthorizationChecksum: auth.Checksum, CollectedAt: now, SessionVerified: true, MarketDataVerified: true, ReadinessVerified: true, WebSocketVerified: true, MappingsVerified: true, CASVerified: true, TelegramVerified: true, CheckpointsVerified: true, EODDrainVerified: true, ShutdownVerified: true, ReadinessBasisPoints: 9950, MaximumDataGapSeconds: 60}
	return evidence, auth
}

func TestDay0GateRequiresZeroActivityAndAllEvidence(t *testing.T) {
	evidence, auth := validDay0Evidence()
	raw, _ := json.Marshal(evidence)
	report, err := EvaluateDay0(raw, auth, time.Now())
	if err != nil || !report.Passed || report.LiveTradingAuthorized {
		t.Fatalf("valid Day-0 rejected: %#v %v", report, err)
	}
	evidence.Orders = 1
	raw, _ = json.Marshal(evidence)
	report, _ = EvaluateDay0(raw, auth, time.Now())
	if report.Passed || !contains(report.Reasons, "ORDER_OBSERVED") {
		t.Fatal("non-zero order count passed")
	}
}

func TestDay1GateIsStrategyBlockedForApprovedNonePlan(t *testing.T) {
	_, auth := validDay0Evidence()
	day0 := GateReport{SchemaVersion: Day0GateSchemaVersion, TradingDate: auth.TradingDate, AuthorizationChecksum: auth.Checksum, Passed: true}
	report := EvaluateDay1(day0, auth, time.Now())
	if report.Passed || !contains(report.Reasons, "STRATEGY_BLOCKED") {
		t.Fatal("NONE strategy authorized for Day-1")
	}
}
