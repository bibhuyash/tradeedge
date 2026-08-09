package opshttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bibhuyash/tradeedge/internal/tradingruntime"
)

type source struct{}

func (source) Snapshot() tradingruntime.RuntimeSnapshot {
	return tradingruntime.RuntimeSnapshot{Mode: tradingruntime.ModePaper, State: tradingruntime.RuntimeRunning}
}

func TestStatusIsGETOnly(t *testing.T) {
	handler := New(source{})
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/runtime/status", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d", get.Code)
	}
	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/v1/runtime/status", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", post.Code)
	}
}
