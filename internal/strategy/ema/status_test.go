package ema

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidationStatusIsBoundedAndGETOnly(t *testing.T) {
	t.Parallel()
	recorder := NewStatusRecorder(false)
	value := recorder.Snapshot()
	value.WarmupSamples = 100
	value.WarmupRequired = 50
	value.CurrentAuthoritativePositionState = "FLAT"
	recorder.Replace(value)

	get := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/strategy/validation", nil))
	if get.Code != http.StatusOK || get.Body.Len() > 4096 || recorder.Snapshot().WarmupSamples != 50 {
		t.Fatalf("GET = %d bytes=%d snapshot=%#v", get.Code, get.Body.Len(), recorder.Snapshot())
	}
	post := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/v1/strategy/validation", nil))
	if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST = %d allow=%q", post.Code, post.Header().Get("Allow"))
	}
}
