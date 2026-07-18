package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestHandlerRejectsUnsupportedMethodAndPath(t *testing.T) {
	handler := NewHandler(&Readiness{})
	assertStatus(t, handler, http.MethodPost, "/healthz", http.StatusMethodNotAllowed, "method_not_allowed")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func assertStatus(t *testing.T, handler http.Handler, method, path string, wantCode int, wantStatus string) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
	if response.Code != wantCode {
		t.Fatalf("%s %s status = %d, want %d", method, path, response.Code, wantCode)
	}
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != wantStatus {
		t.Fatalf("status body = %q, want %q", body["status"], wantStatus)
	}
}
