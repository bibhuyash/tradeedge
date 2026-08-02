package telemetry

import (
	"testing"
	"time"

	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
)

func TestMemoryRecorderUsesOnlyBoundedDimensions(t *testing.T) {
	recorder := NewMemoryRecorder()
	recorder.Record(Event{Outcome: "COMMITTED_REJECTED", InFlight: 2, Duration: time.Millisecond})
	recorder.Record(Event{RuleID: "DAILY_LOSS_LIMIT", Status: riskmodel.RuleViolation,
		Effect: riskmodel.EffectReject, Severity: riskmodel.SeverityBlocking, InFlight: 1})
	snapshot := recorder.Snapshot()
	if snapshot.Counts["COMMITTED_REJECTED"] != 1 ||
		snapshot.RuleCounts["DAILY_LOSS_LIMIT|VIOLATION|REJECT|BLOCKING"] != 1 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.Maximum != 2 || snapshot.InFlight != 1 {
		t.Fatalf("in-flight telemetry = %+v", snapshot)
	}
}
