package opshttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bibhuyash/tradeedge/internal/qualification"
)

func TestQualificationAPIsAreGETOnlyAndBounded(t *testing.T) {
	engine, _ := qualification.New(qualification.DefaultPolicy(), nil)
	handler, err := New(engine)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/qualification/strategies", "/api/v1/qualification/strategies/EMA_REFERENCE_V1/NIFTY", "/api/v1/qualification/signals/recent?limit=1000", "/api/v1/qualification/scorecards"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/qualification/scorecards", nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("mutation method accepted: %d", response.Code)
	}
}

func TestParseLimit(t *testing.T) {
	for input, want := range map[string]int{"": 20, "-1": 20, "1000": 100, "7": 7, "bad": 20} {
		if got := parseLimit(input); got != want {
			t.Fatalf("%q: got %d want %d", input, got, want)
		}
	}
}
