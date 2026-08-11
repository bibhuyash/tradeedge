package opshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
	"github.com/bibhuyash/tradeedge/internal/marketdata/latest"
	"github.com/bibhuyash/tradeedge/internal/marketdata/quality"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
	"github.com/bibhuyash/tradeedge/internal/marketdata/storage"
)

type ReadinessSource interface {
	Snapshot(context.Context) readiness.Snapshot
}

type QualitySource interface {
	MissingIntervals(context.Context) ([]quality.MissingInterval, error)
}

type LatestSource interface {
	Snapshot(context.Context) []latest.Observation
}

type Dependencies struct {
	Readiness ReadinessSource
	Calendar  calendar.Calendar
	Datasets  storage.RevisionRepository
	Quality   QualitySource
	Latest    LatestSource
	Timeout   time.Duration
}

type Handler struct{ dependencies Dependencies }

func New(dependencies Dependencies) http.Handler {
	if dependencies.Timeout <= 0 {
		dependencies.Timeout = 2 * time.Second
	}
	return Handler{dependencies: dependencies}
}

func (h Handler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), h.dependencies.Timeout)
	defer cancel()
	path := strings.TrimSuffix(request.URL.Path, "/")
	switch {
	case path == "/api/v1/market-data/readiness":
		h.readiness(w, ctx, false, request)
	case path == "/api/v1/market-data/readiness/instruments":
		h.readiness(w, ctx, true, request)
	case path == "/api/v1/market-data/observations/latest":
		h.latest(w, ctx)
	case path == "/api/v1/market-data/quality":
		h.quality(w, ctx)
	case path == "/api/v1/market-data/calendar":
		h.calendar(w, ctx, request)
	case path == "/api/v1/market-data/datasets/current":
		h.current(w, ctx, request)
	case strings.HasPrefix(path, "/api/v1/market-data/datasets/"):
		h.dataset(w, ctx, strings.TrimPrefix(path, "/api/v1/market-data/datasets/"))
	default:
		writeError(w, http.StatusNotFound, "not_found")
	}
}

type latestObservation struct {
	latest.Observation
	FreshnessState  readiness.State      `json:"freshness_state"`
	FreshnessReason readiness.ReasonCode `json:"freshness_reason"`
}

func (h Handler) latest(w http.ResponseWriter, ctx context.Context) {
	if h.dependencies.Latest == nil || h.dependencies.Readiness == nil {
		writeError(w, http.StatusServiceUnavailable, "latest_observations_unavailable")
		return
	}
	diagnostics := h.dependencies.Readiness.Snapshot(ctx).Diagnostics
	freshness := make(map[string]readiness.Diagnostic, len(diagnostics))
	for _, diagnostic := range diagnostics {
		freshness[diagnostic.Instrument] = diagnostic
	}
	values := h.dependencies.Latest.Snapshot(ctx)
	items := make([]latestObservation, 0, len(values))
	for _, value := range values {
		diagnostic, found := freshness[value.InstrumentID]
		state, reason := readiness.StateUnknown, readiness.ReasonNoAcceptedEvent
		if found {
			state, reason = diagnostic.State, diagnostic.Reason
		}
		items = append(items, latestObservation{Observation: value, FreshnessState: state, FreshnessReason: reason})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

func (h Handler) readiness(w http.ResponseWriter, ctx context.Context, instruments bool, request *http.Request) {
	if h.dependencies.Readiness == nil {
		writeError(w, http.StatusServiceUnavailable, "readiness_unavailable")
		return
	}
	snapshot := h.dependencies.Readiness.Snapshot(ctx)
	if !instruments {
		snapshot.Diagnostics = nil
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	limit, cursor, ok := parsePage(request)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_pagination")
		return
	}
	watchlist, state := request.URL.Query().Get("watchlist"), request.URL.Query().Get("state")
	filtered := make([]readiness.Diagnostic, 0, len(snapshot.Diagnostics))
	for _, diagnostic := range snapshot.Diagnostics {
		if watchlist != "" && diagnostic.WatchlistID != watchlist {
			continue
		}
		if state != "" && string(diagnostic.State) != state {
			continue
		}
		filtered = append(filtered, diagnostic)
	}
	if cursor > len(filtered) {
		writeError(w, http.StatusBadRequest, "invalid_cursor")
		return
	}
	end := cursor + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	next := ""
	if end < len(filtered) {
		next = strconv.Itoa(end)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"evaluated_at": snapshot.EvaluatedAt, "items": filtered[cursor:end], "next_cursor": next,
	})
}

func (h Handler) quality(w http.ResponseWriter, ctx context.Context) {
	if h.dependencies.Quality == nil {
		writeJSON(w, http.StatusOK, map[string]any{"missing_count": 0, "missing_ranges": []any{}})
		return
	}
	intervals, err := h.dependencies.Quality.MissingIntervals(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "quality_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"missing_count": len(intervals), "missing_ranges": quality.CoalesceMissing(intervals),
	})
}

func (h Handler) calendar(w http.ResponseWriter, ctx context.Context, request *http.Request) {
	if h.dependencies.Calendar == nil {
		writeError(w, http.StatusServiceUnavailable, "calendar_unavailable")
		return
	}
	if request.URL.Query().Get("exchange") != string(domain.ExchangeNSE) {
		writeError(w, http.StatusBadRequest, "invalid_exchange")
		return
	}
	parsed, err := time.Parse("2006-01-02", request.URL.Query().Get("date"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_date")
		return
	}
	date, _ := domain.NewCivilDate(parsed.Year(), parsed.Month(), parsed.Day())
	day, err := h.dependencies.Calendar.Day(ctx, domain.ExchangeNSE, date)
	if errors.Is(err, calendar.ErrCalendarOutOfRange) || errors.Is(err, calendar.ErrTradingDayNotFound) {
		writeError(w, http.StatusNotFound, "calendar_date_not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "calendar_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"calendar_version": h.dependencies.Calendar.Version(), "source": h.dependencies.Calendar.Source(),
		"day": day,
	})
}

func (h Handler) current(w http.ResponseWriter, ctx context.Context, request *http.Request) {
	if h.dependencies.Datasets == nil {
		writeError(w, http.StatusServiceUnavailable, "repository_unavailable")
		return
	}
	series := request.URL.Query().Get("series")
	if series == "" {
		writeError(w, http.StatusBadRequest, "invalid_series")
		return
	}
	publication, err := h.dependencies.Datasets.CurrentPublication(ctx, series)
	if errors.Is(err, storage.ErrDatasetNotFound) {
		writeError(w, http.StatusNotFound, "publication_not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "repository_unavailable")
		return
	}
	publication.Reason = sanitizeText(publication.Reason)
	writeJSON(w, http.StatusOK, publication)
}

func (h Handler) dataset(w http.ResponseWriter, ctx context.Context, suffix string) {
	if h.dependencies.Datasets == nil {
		writeError(w, http.StatusServiceUnavailable, "repository_unavailable")
		return
	}
	lineage := strings.HasSuffix(suffix, "/lineage")
	idText := strings.TrimSuffix(suffix, "/lineage")
	if len(idText) != 64 || strings.Contains(idText, "/") {
		writeError(w, http.StatusBadRequest, "invalid_dataset_id")
		return
	}
	id := storage.DatasetID(idText)
	if lineage {
		manifests, err := h.dependencies.Datasets.Lineage(ctx, id, 100)
		if errors.Is(err, storage.ErrDatasetNotFound) {
			writeError(w, http.StatusNotFound, "dataset_not_found")
			return
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "repository_unavailable")
			return
		}
		publications := []storage.Publication{}
		if len(manifests) > 0 && manifests[0].Series != "" {
			publications, _ = h.dependencies.Datasets.Publications(ctx, manifests[0].Series, 100)
			for index := range publications {
				publications[index].Reason = sanitizeText(publications[index].Reason)
			}
		}
		for index := range manifests {
			manifests[index] = sanitizeManifest(manifests[index])
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"lineage": manifests, "publications": publications,
		})
		return
	}
	reader, err := h.dependencies.Datasets.Open(ctx, id)
	if errors.Is(err, storage.ErrDatasetNotFound) {
		writeError(w, http.StatusNotFound, "dataset_not_found")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "repository_unavailable")
		return
	}
	defer reader.Close()
	writeJSON(w, http.StatusOK, sanitizeManifest(reader.Manifest()))
}

func sanitizeManifest(manifest storage.DatasetManifest) storage.DatasetManifest {
	manifest.Source = sanitizeText(manifest.Source)
	manifest.CorrectionReason = sanitizeText(manifest.CorrectionReason)
	return manifest
}

func sanitizeText(value string) string {
	if filepath.IsAbs(value) || strings.Contains(value, `:\`) || strings.Contains(value, `:/`) {
		return "redacted"
	}
	return value
}

func parsePage(request *http.Request) (limit, cursor int, ok bool) {
	limit = 100
	if value := request.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 || parsed > 250 {
			return 0, 0, false
		}
		limit = parsed
	}
	if value := request.URL.Query().Get("cursor"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return 0, 0, false
		}
		cursor = parsed
	}
	return limit, cursor, true
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
