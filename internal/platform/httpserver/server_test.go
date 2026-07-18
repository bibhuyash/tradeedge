package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
)

type fixedMarketReadiness struct{ snapshot readiness.Snapshot }

func (f fixedMarketReadiness) Snapshot(context.Context) readiness.Snapshot { return f.snapshot }

func TestHealthAndReadinessHandlers(t *testing.T) {
	readiness := &Readiness{}
	handler := NewHandler(readiness)

	assertStatus(t, handler, http.MethodGet, "/healthz", http.StatusOK, "ok")
	assertStatus(t, handler, http.MethodGet, "/readyz", http.StatusServiceUnavailable, "not_ready")

	readiness.Set(true)
	assertStatus(t, handler, http.MethodGet, "/readyz", http.StatusOK, "ready")

	readiness.Set(false)
	assertStatus(t, handler, http.MethodGet, "/readyz", http.StatusServiceUnavailable, "not_ready")
}

func TestDetailedReadinessFailsClosedAndNeverInfersTradingPermission(t *testing.T) {
	process := &Readiness{}
	process.Set(true)
	handler := NewHandlerWithOptions(process, Options{MarketReadiness: fixedMarketReadiness{
		snapshot: readiness.Snapshot{
			EvaluatedAt: time.Unix(1, 0), State: readiness.StateStale,
			Reasons: []readiness.ReasonCode{readiness.ReasonTransportLagExceeded},
		},
	}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["trading_permitted"] != false || body["market_data_state"] != "STALE" {
		t.Fatalf("body = %#v", body)
	}
}

func TestHandlerRejectsUnsupportedMethodAndPath(t *testing.T) {
	handler := NewHandler(&Readiness{})
	assertStatus(t, handler, http.MethodPost, "/healthz", http.StatusMethodNotAllowed, "method_not_allowed")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestMetricsEndpointIsGETOnly(t *testing.T) {
	metrics := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := NewHandlerWithOptions(&Readiness{}, Options{Metrics: metrics})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("response = %d %#v", response.Code, response.Header())
	}
}

func assertStatus(t *testing.T, handler http.Handler, method, path string, wantCode int, wantStatus string) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
	if response.Code != wantCode {
		t.Fatalf("%s %s status = %d, want %d", method, path, response.Code, wantCode)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != wantStatus {
		t.Fatalf("status body = %q, want %q", body["status"], wantStatus)
	}
}
