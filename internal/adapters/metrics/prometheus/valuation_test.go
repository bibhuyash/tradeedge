package prometheus

import (
	"github.com/bibhuyash/tradeedge/internal/accounting/valuation"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"testing"
)

func TestValuationMetricsUseBoundedLabels(t *testing.T) {
	registry := prom.NewRegistry()
	recorder, err := NewValuationRecorder(registry)
	if err != nil {
		t.Fatal(err)
	}
	recorder.Record(valuation.Event{Operation: "portfolio-secret", Outcome: "instrument-secret", Status: "account-secret", Reason: "order-secret"})
	if count := testutil.CollectAndCount(recorder.attempts); count != 1 {
		t.Fatalf("metric count=%d", count)
	}
	if valuation.BoundedLabel("portfolio-secret") != "invalid" {
		t.Fatal("identity escaped bounded vocabulary")
	}
}
