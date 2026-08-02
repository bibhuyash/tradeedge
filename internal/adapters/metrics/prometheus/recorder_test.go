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
	executiontelemetry "github.com/bibhuyash/tradeedge/internal/execution/telemetry"
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

func TestExecutionMetricsUseOnlyBoundedLabels(t *testing.T) {
	recorder := New()
	recorder.Execution().Record(executiontelemetry.Event{Operation: executiontelemetry.OperationPlan, Outcome: executiontelemetry.OutcomeCompleted, PlanID: "high-cardinality-plan", Duration: time.Millisecond, InFlight: 1})
	recorder.Execution().Record(executiontelemetry.Event{Operation: executiontelemetry.OperationOrderEvent, Outcome: executiontelemetry.OutcomeUnknown, Detail: "UNKNOWN", OrderID: "high-cardinality-order", HasUnknownOrders: true, UnknownOrders: 1})
	recorder.Execution().Record(executiontelemetry.Event{Operation: executiontelemetry.OperationMismatch, Outcome: executiontelemetry.OutcomeBlocked, Detail: "TERMS_MISMATCH"})
	recorder.Execution().Record(executiontelemetry.Event{Operation: executiontelemetry.OperationPaperScenario, Outcome: executiontelemetry.OutcomeCompleted, Detail: "IMMEDIATE_FILL"})
	recorder.Execution().Record(executiontelemetry.Event{Operation: "attacker-controlled", Outcome: "raw-error", Detail: "instrument-id"})
	response := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, expected := range []string{"tradeedge_execution_plans_total", "tradeedge_execution_order_events_total", "tradeedge_execution_reconciliation_issues_total", "tradeedge_paper_broker_scenarios_total", "tradeedge_execution_unknown_orders"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q", expected)
		}
	}
	for _, forbidden := range []string{"plan_id=", "order_id=", "client_order_id=", "instrument_id=", "broker_order_id=", "high-cardinality", "raw-error"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("forbidden execution metric value %q", forbidden)
		}
	}
}

func TestExecutionMetricCatalogHasFiniteSeriesBound(t *testing.T) {
	recorder := New()
	operations := []executiontelemetry.Operation{executiontelemetry.OperationPlan, executiontelemetry.OperationSubmission, executiontelemetry.OperationOrderEvent, executiontelemetry.OperationPublication, executiontelemetry.OperationCancellation, executiontelemetry.OperationReconciliation, executiontelemetry.OperationMismatch, executiontelemetry.OperationRepair, executiontelemetry.OperationPaperScenario, executiontelemetry.OperationShutdown, executiontelemetry.OperationHealth}
	outcomes := []executiontelemetry.Outcome{executiontelemetry.OutcomeCreated, executiontelemetry.OutcomeCompleted, executiontelemetry.OutcomeFailed, executiontelemetry.OutcomePending, executiontelemetry.OutcomeAccepted, executiontelemetry.OutcomeAcknowledged, executiontelemetry.OutcomePartialFill, executiontelemetry.OutcomeFilled, executiontelemetry.OutcomeCancelled, executiontelemetry.OutcomeRejected, executiontelemetry.OutcomeUnknown, executiontelemetry.OutcomeDuplicate, executiontelemetry.OutcomeInvalid, executiontelemetry.OutcomeUnavailable, executiontelemetry.OutcomeBlocked, executiontelemetry.OutcomeRepaired, executiontelemetry.OutcomeClean, executiontelemetry.OutcomeShutdown}
	for _, operation := range operations {
		for _, outcome := range outcomes {
			recorder.Execution().Record(executiontelemetry.Event{Operation: operation, Outcome: outcome, Detail: "attacker-controlled-cardinality", Duration: time.Millisecond})
		}
	}
	families, err := recorder.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	series := 0
	for _, family := range families {
		if strings.HasPrefix(family.GetName(), "tradeedge_execution_") || family.GetName() == "tradeedge_paper_broker_scenarios_total" {
			series += len(family.Metric)
		}
	}
	if series > 384 {
		t.Fatalf("execution metric series = %d, want <= 384", series)
	}
}
