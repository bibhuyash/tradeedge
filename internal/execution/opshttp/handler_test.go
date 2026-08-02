package opshttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/adapters/broker/paper"
	executionmemory "github.com/bibhuyash/tradeedge/internal/adapters/execution/memory"
	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	"github.com/bibhuyash/tradeedge/internal/execution/coordinator"
	"github.com/bibhuyash/tradeedge/internal/execution/opshttp"
	"github.com/bibhuyash/tradeedge/internal/execution/reconciliation"
	executiontelemetry "github.com/bibhuyash/tradeedge/internal/execution/telemetry"
	executionfixture "github.com/bibhuyash/tradeedge/internal/execution/testfixture"
)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

func runtime(t *testing.T, scenario paper.Scenario) (http.Handler, *executionmemory.Store, executionfixture.Fixture) {
	t.Helper()
	fixture, err := executionfixture.New(false)
	if err != nil {
		t.Fatal(err)
	}
	journal := executiontelemetry.NewMemoryRecorder(1000)
	store := executionmemory.NewStoreInstrumented(executionmemory.DefaultLimits(), journal)
	_, _ = store.RegisterPlan(context.Background(), fixture.Intent, fixture.Plan, fixture.Orders)
	broker, _ := paper.NewScriptedInstrumented(fixedClock{fixture.Plan.Spec().CreatedAt}, []paper.Scenario{scenario}, journal)
	runner, _ := coordinator.NewInstrumented(store, broker, coordinator.DefaultConfig(), journal)
	_, _ = runner.ExecutePlan(context.Background(), fixture.Plan.ID(), fixture.Plan.Spec().CreatedAt)
	reconciler, _ := reconciliation.NewInstrumented(store, broker, func(ctx context.Context, event executionbroker.Event, at time.Time) error {
		_, publishErr := runner.PublishBrokerEvent(ctx, event, at)
		return publishErr
	}, journal)
	_, _ = reconciler.Run(context.Background(), fixture.Plan.Spec().CreatedAt)
	handler := opshttp.New(opshttp.Dependencies{Repository: store, OMS: store, Coordinator: runner, PaperBroker: broker, Reconciliation: reconciler, Audit: journal, Timeout: time.Second})
	return handler, store, fixture
}

func TestOperationalAPIIsGETOnlyBoundedAndInspectable(t *testing.T) {
	handler, store, fixture := runtime(t, paper.Scenario{Behavior: paper.BehaviorImmediateFill})
	paths := []string{"health", "plans", "orders", "reports", "fills", "unknown", "reconciliation", "paper-broker", "coordinator", "audit"}
	for _, path := range paths {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/execution/"+path+"?limit=10", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", path, response.Code, response.Body.String())
		}
	}
	for _, path := range []string{"plans", "orders", "reports", "fills", "audit"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/execution/"+path+"?limit=101", nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("unbounded %s status = %d", path, response.Code)
		}
	}
	invalidID := httptest.NewRecorder()
	handler.ServeHTTP(invalidID, httptest.NewRequest(http.MethodGet, "/api/v1/execution/reports?order_id=unbounded", nil))
	if invalidID.Code != http.StatusBadRequest {
		t.Fatalf("invalid order ID status = %d", invalidID.Code)
	}
	before, _ := store.Order(context.Background(), fixture.Orders[0].ID())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/execution/orders", strings.NewReader(`{"state":"CANCELLED"}`)))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("mutation status = %d", response.Code)
	}
	after, _ := store.Order(context.Background(), fixture.Orders[0].ID())
	if before.Spec().Revision != after.Spec().Revision || before.Spec().State != after.Spec().State {
		t.Fatal("operational API mutated OMS state")
	}
	orders := httptest.NewRecorder()
	handler.ServeHTTP(orders, httptest.NewRequest(http.MethodGet, "/api/v1/execution/orders?plan_id="+fixture.Plan.ID().String(), nil))
	if !strings.Contains(orders.Body.String(), fixture.Orders[0].ID().String()) || !strings.Contains(orders.Body.String(), `"filled_quantity"`) {
		t.Fatal("order lifecycle view incomplete")
	}
	var audit map[string]any
	auditResponse := httptest.NewRecorder()
	handler.ServeHTTP(auditResponse, httptest.NewRequest(http.MethodGet, "/api/v1/execution/audit", nil))
	if err := json.Unmarshal(auditResponse.Body.Bytes(), &audit); err != nil || audit["recent"] == nil {
		t.Fatalf("audit response invalid: %v %s", err, auditResponse.Body.String())
	}
}

func TestExecutionHealthBlocksOnUnknownAndReconciliationMismatch(t *testing.T) {
	handler, _, _ := runtime(t, paper.Scenario{Behavior: paper.BehaviorImmediateFill, UnavailableAttempts: 100})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/execution/health", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "UNKNOWN_ORDERS") || !strings.Contains(response.Body.String(), "RECONCILIATION_BLOCKED") {
		t.Fatalf("health = %d %s", response.Code, response.Body.String())
	}
}

func TestOperationalAPIRejectsInvalidStateAndUnavailableDependencies(t *testing.T) {
	handler := opshttp.New(opshttp.Dependencies{})
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/v1/execution/orders?state=GUESSED", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid state = %d", invalid.Code)
	}
	unavailable := httptest.NewRecorder()
	handler.ServeHTTP(unavailable, httptest.NewRequest(http.MethodGet, "/api/v1/execution/health", nil))
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing health = %d", unavailable.Code)
	}
}
