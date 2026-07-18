package opshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
	"github.com/bibhuyash/tradeedge/internal/marketdata/storage"
)

type snapshotSource struct{ snapshot readiness.Snapshot }

func (s snapshotSource) Snapshot(context.Context) readiness.Snapshot { return s.snapshot }

func TestReadinessAndPaginationContracts(t *testing.T) {
	handler := New(Dependencies{Readiness: snapshotSource{snapshot: readiness.Snapshot{
		EvaluatedAt: time.Unix(1, 0), State: readiness.StateReady, TradingPermitted: true,
		Diagnostics: []readiness.Diagnostic{{WatchlistID: "primary", State: readiness.StateReady}},
	}}})
	response := request(handler, http.MethodGet, "/api/v1/market-data/readiness")
	if response.Code != http.StatusOK || string(response.Body.Bytes()) == "" {
		t.Fatalf("readiness response = %d %s", response.Code, response.Body.String())
	}
	response = request(handler, http.MethodGet, "/api/v1/market-data/readiness/instruments?limit=251")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("pagination status = %d", response.Code)
	}
	response = request(handler, http.MethodPost, "/api/v1/market-data/readiness")
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("method response = %d %#v", response.Code, response.Header())
	}
}

func TestDatasetAndPublicationEndpoints(t *testing.T) {
	ctx := context.Background()
	repository := storage.NewMemoryRepository()
	writer, err := repository.Create(ctx, storage.DraftManifest{
		MasterVersion: "master", CalendarVersion: "calendar", Source: "fixture",
		OrderingVersion: "ordering", CreatedAt: time.Unix(1, 0), Series: "series",
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := writer.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Publish(ctx, storage.PublicationRequest{
		Series: "series", DatasetID: manifest.ID, Action: storage.PublicationPublish,
		Reason: "initial", RequestID: "publication-1", PublishedAt: time.Unix(2, 0),
	}); err != nil {
		t.Fatal(err)
	}
	handler := New(Dependencies{Datasets: repository})
	for _, target := range []string{
		"/api/v1/market-data/datasets/" + string(manifest.ID),
		"/api/v1/market-data/datasets/" + string(manifest.ID) + "/lineage",
		"/api/v1/market-data/datasets/current?series=series",
		"/api/v1/market-data/quality",
	} {
		if response := request(handler, http.MethodGet, target); response.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", target, response.Code, response.Body.String())
		}
	}
	if response := request(handler, http.MethodGet, "/api/v1/market-data/datasets/not-an-id"); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid ID status = %d", response.Code)
	}
}

func TestCalendarEndpointDistinguishesHolidayAndMissingCoverage(t *testing.T) {
	date, _ := domain.NewCivilDate(2026, time.July, 18)
	schedule, err := calendar.New(calendar.Spec{
		Source:   calendar.Source{Name: "fixture", PublishedAt: time.Unix(1, 0)},
		Timezone: "Asia/Kolkata", EffectiveFrom: date, EffectiveTo: date,
		Days: []calendar.TradingDay{{Exchange: domain.ExchangeNSE, Date: date, Status: calendar.DayHoliday}},
	})
	if err != nil {
		t.Fatalf("calendar.New() error = %v", err)
	}
	handler := New(Dependencies{Calendar: schedule})
	response := request(handler, http.MethodGet, "/api/v1/market-data/calendar?exchange=NSE&date=2026-07-18")
	if response.Code != http.StatusOK {
		t.Fatalf("holiday status = %d: %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	response = request(handler, http.MethodGet, "/api/v1/market-data/calendar?exchange=NSE&date=2026-07-19")
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing date status = %d", response.Code)
	}
}

func request(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, target, nil))
	return response
}
