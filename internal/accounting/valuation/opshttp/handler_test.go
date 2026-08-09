package opshttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGETOnlyAndBounds(t *testing.T) {
	handler := New(nil, nil, time.Second)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		request := httptest.NewRequest(method, "/api/v1/financial/snapshot", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET" {
			t.Fatalf("%s code=%d", method, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/financial/positions?portfolio_id=bad&limit=101", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unbounded request code=%d", response.Code)
	}
}
