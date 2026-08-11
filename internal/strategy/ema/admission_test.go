package ema

import (
	"testing"

	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

func TestAdmissionFailsClosedAndPreservesExit(t *testing.T) {
	t.Parallel()
	base := AdmissionInput{Enabled: true, ExecutionMappingAvailable: true, MarketDataFresh: true, MarketDataReady: true, SessionRegime: "NORMAL_TRADING", InitialCASRevision: "cas/1", CurrentCASRevision: "cas/1", Position: PositionFlat, Effect: EffectIncrease}
	tests := []struct {
		name   string
		mutate func(*AdmissionInput)
		reason strategymodel.NoActionReason
	}{
		{"disabled", func(v *AdmissionInput) { v.Enabled = false }, strategymodel.NoActionDisabled},
		{"mapping", func(v *AdmissionInput) { v.ExecutionMappingAvailable = false }, strategymodel.NoActionMappingUnavailable},
		{"stale", func(v *AdmissionInput) { v.MarketDataFresh = false }, strategymodel.NoActionStaleMarketData},
		{"readiness-loss", func(v *AdmissionInput) { v.MarketDataReady = false }, strategymodel.NoActionStaleMarketData},
		{"cas", func(v *AdmissionInput) { v.CASRestricted = true }, strategymodel.NoActionCASRestricted},
		{"cas-mid-signal", func(v *AdmissionInput) { v.CurrentCASRevision = "cas/2" }, strategymodel.NoActionAuthoritativeConflict},
		{"closed", func(v *AdmissionInput) { v.SessionRegime = "CLOSED" }, strategymodel.NoActionSessionNotAllowed},
		{"position-open", func(v *AdmissionInput) { v.Position = PositionOpen }, strategymodel.NoActionPositionAlreadyOpen},
		{"position-unknown", func(v *AdmissionInput) { v.Position = PositionUnknown }, strategymodel.NoActionAuthoritativeConflict},
		{"cooldown", func(v *AdmissionInput) { v.CooldownActive = true }, strategymodel.NoActionCooldownActive},
		{"stop-new", func(v *AdmissionInput) { v.StopNewExposure = true }, strategymodel.NoActionAuthoritativeConflict},
		{"risk-circuit", func(v *AdmissionInput) { v.RiskCircuitOpen = true }, strategymodel.NoActionAuthoritativeConflict},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			got := EvaluateAdmission(value)
			if got.Allowed || got.Reason != test.reason {
				t.Fatalf("decision=%#v want=%s", got, test.reason)
			}
		})
	}
	if got := EvaluateAdmission(base); !got.Allowed {
		t.Fatalf("entry blocked: %#v", got)
	}
	exit := base
	exit.Effect = EffectReduce
	exit.Position = PositionOpen
	exit.SessionRegime = "EOD_CLOSE"
	exit.StopNewExposure = true
	if got := EvaluateAdmission(exit); !got.Allowed {
		t.Fatalf("EOD exit blocked: %#v", got)
	}
}
