package prometheus

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	risktelemetry "github.com/bibhuyash/tradeedge/internal/risk/telemetry"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/telemetry"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
	strategytelemetry "github.com/bibhuyash/tradeedge/internal/strategy/telemetry"
)

func TestRecorderExposesCatalogWithoutHighCardinalityLabels(t *testing.T) {
	recorder := New()
	dimensions := telemetry.Dimensions{
		Provider: "fixture", Exchange: domain.ExchangeNSE, Segment: domain.SegmentIndex,
		Kind: model.EventKindQuote,
	}
	recorder.Observation(dimensions, "accepted")
	recorder.Quality(dimensions, model.QualityDuplicate, model.DispositionSuppressed)
	recorder.Normalization(dimensions, time.Millisecond)
	recorder.TransportLag(dimensions, time.Second)
	recorder.DatasetCommit("committed", time.Second, 100)
	recorder.Readiness("watchlist", "", "primary", "READY", "NONE", true, 1)
	definitionID, _ := strategymodel.NewDefinitionID("metric-fixture")
	recorder.Record(strategytelemetry.Event{
		Definition: definitionID, Outcome: "COMMITTED_NO_ACTION",
		Duration: time.Millisecond, Publish: time.Millisecond, StateBytes: 12, InFlight: 1,
	})
	response := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	for _, name := range []string{
		"tradeedge_marketdata_observations_total",
		"tradeedge_marketdata_quality_total",
		"tradeedge_marketdata_normalization_duration_seconds",
		"tradeedge_marketdata_ready",
		"tradeedge_strategy_evaluations_total",
		"tradeedge_strategy_evaluation_duration_seconds",
		"tradeedge_strategy_publication_duration_seconds",
		"tradeedge_strategy_state_bytes",
		"tradeedge_strategy_in_flight",
	} {
		if !strings.Contains(body, name) {
			t.Fatalf("metrics output missing %s", name)
		}
	}
	for _, prohibited := range []string{"instrument_id=", "provider_token=", "dataset_id=", "error="} {
		if strings.Contains(body, prohibited) {
			t.Fatalf("metrics output contains prohibited label %q", prohibited)
		}
	}
	families, err := recorder.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	buckets := map[string]int{
		"tradeedge_marketdata_normalization_duration_seconds":  11,
		"tradeedge_marketdata_transport_lag_seconds":           12,
		"tradeedge_marketdata_dataset_commit_duration_seconds": 12,
	}
	for _, family := range families {
		want, found := buckets[family.GetName()]
		if !found {
			continue
		}
		if len(family.Metric) == 0 || family.Metric[0].Histogram == nil ||
			len(family.Metric[0].Histogram.Bucket) != want {
			t.Fatalf("%s bucket count is not %d", family.GetName(), want)
		}
		delete(buckets, family.GetName())
	}
	if len(buckets) != 0 {
		t.Fatalf("histogram families not gathered: %#v", buckets)
	}
}

func TestRiskMetricsUseOnlyBoundedLabels(t *testing.T) {
	recorder := New()
	recorder.Risk().Record(risktelemetry.Event{Outcome: "COMMITTED_REJECTED", RuleID: "DAILY_LOSS_LIMIT",
		Status: riskmodel.RuleViolation, Effect: riskmodel.EffectReject, Severity: riskmodel.SeverityBlocking})
	response := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{"tradeedge_risk_decisions_total", "tradeedge_risk_rule_results_total", `rule_id="DAILY_LOSS_LIMIT"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q", expected)
		}
	}
	for _, forbidden := range []string{"portfolio_id=", "strategy_id=", "instrument_id=", "decision_id="} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("forbidden label %q", forbidden)
		}
	}
}

func TestRecorderSupportsConcurrentUpdates(t *testing.T) {
	recorder := New()
	dimensions := telemetry.Dimensions{Provider: "fixture", Kind: model.EventKindQuote}
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				recorder.Observation(dimensions, "accepted")
				recorder.Readiness("watchlist", "", "primary", "READY", "NONE", true, 1)
			}
		}()
	}
	wait.Wait()
	if _, err := recorder.Registry().Gather(); err != nil {
		t.Fatal(err)
	}
}
