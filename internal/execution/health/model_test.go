package health_test

import (
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/execution/health"
)

func TestHealthAggregationIsExplicitAndFailClosed(t *testing.T) {
	now := time.Date(2026, 8, 2, 5, 0, 0, 0, time.UTC)
	coordinator := health.Coordinator{Available: true}
	oms := health.OMS{Available: true}
	paper := health.PaperBroker{Available: true}
	reconciliation := health.Reconciliation{Available: true, LastAttempt: now, LastSuccess: now, IssueCounts: map[string]int{}}
	if value := health.Aggregate(coordinator, oms, paper, reconciliation, 0); value.State != health.StateHealthy {
		t.Fatalf("healthy = %+v", value)
	}
	if value := health.Aggregate(coordinator, oms, paper, reconciliation, 1); value.State != health.StateBlocked || len(value.ReasonCodes) != 1 {
		t.Fatalf("unknown = %+v", value)
	}
	reconciliation.Blocked = true
	reconciliation.IssueCounts["TERMS_MISMATCH"] = 1
	if value := health.Aggregate(coordinator, oms, paper, reconciliation, 0); value.State != health.StateBlocked {
		t.Fatalf("mismatch = %+v", value)
	}
	reconciliation = health.Reconciliation{Available: true}
	if value := health.Aggregate(coordinator, oms, paper, reconciliation, 0); value.State != health.StateDegraded {
		t.Fatalf("not reconciled = %+v", value)
	}
	paper.Available = false
	if value := health.Aggregate(coordinator, oms, paper, reconciliation, 0); value.State != health.StateUnavailable {
		t.Fatalf("unavailable = %+v", value)
	}
}
