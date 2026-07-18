package opshttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	strategymemory "github.com/bibhuyash/tradeedge/internal/adapters/strategy/memory"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
	"github.com/bibhuyash/tradeedge/internal/strategy/runner"
	strategystorage "github.com/bibhuyash/tradeedge/internal/strategy/storage"
)

type fixedHealth struct{}

func (fixedHealth) Health() (bool, int, int, []runner.Failure) {
	return false, 1, 1, nil
}

func TestOperationalAPIIsGETOnlyAndBounded(t *testing.T) {
	t.Parallel()
	store := strategymemory.NewStore()
	for index := 0; index < 3; index++ {
		id, _ := strategymodel.NewDefinitionID(fmt.Sprintf("ops-fixture-%d", index))
		record, _ := strategystorage.NewDefinitionRecord(
			id, "definition/v1", fmt.Sprintf("Fixture %d", index), "bounded fixture",
		)
		if _, err := store.RegisterDefinition(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	handler := New(store, fixedHealth{}, time.Second)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/strategy/definitions?limit=2", nil,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body struct {
		Items []json.RawMessage `json:"items"`
		Limit int               `json:"limit"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || body.Limit != 2 {
		t.Fatalf("body = %#v", body)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodPost, "/api/v1/strategy/definitions", nil,
	))
	if response.Code != http.StatusMethodNotAllowed ||
		response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST response = %d %#v", response.Code, response.Header())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(
		http.MethodGet, "/api/v1/strategy/definitions?limit=101", nil,
	))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status = %d", response.Code)
	}
}
