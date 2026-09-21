package controlplane

import (
	"testing"
	"time"
)

func TestMarketTradingDateReadsClockOnEveryEvaluation(t *testing.T) {
	now := time.Date(2026, 9, 20, 5, 30, 0, 0, time.UTC)
	source := marketSource{now: func() time.Time { return now }}
	if got := source.TradingDate(); got != "2026-09-20" {
		t.Fatalf("date=%s", got)
	}
	now = time.Date(2026, 9, 21, 5, 0, 0, 0, time.UTC)
	if got := source.TradingDate(); got != "2026-09-21" {
		t.Fatalf("date remained cached: %s", got)
	}
}
