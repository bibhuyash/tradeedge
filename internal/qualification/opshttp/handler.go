// Package opshttp exposes bounded GET-only qualification evidence.
package opshttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/bibhuyash/tradeedge/internal/qualification"
)

type Source interface {
	Snapshot() qualification.Snapshot
	Scorecards() []qualification.Scorecard
	RecentSignals(int) []qualification.SignalRecord
	Strategy(string, qualification.Underlying) (qualification.Series, qualification.Scorecard, error)
}

type Handler struct{ source Source }

func New(source Source) (http.Handler, error) {
	if source == nil {
		return nil, qualification.ErrInvalid
	}
	return Handler{source}, nil
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case r.URL.Path == "/api/v1/qualification/strategies":
		_ = json.NewEncoder(w).Encode(h.source.Snapshot().Series)
	case r.URL.Path == "/api/v1/qualification/signals/recent":
		_ = json.NewEncoder(w).Encode(h.source.RecentSignals(parseLimit(r.URL.Query().Get("limit"))))
	case r.URL.Path == "/api/v1/qualification/scorecards":
		_ = json.NewEncoder(w).Encode(h.source.Scorecards())
	case strings.HasPrefix(r.URL.Path, "/api/v1/qualification/strategies/"):
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/qualification/strategies/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		series, score, err := h.source.Strategy(parts[0], qualification.Underlying(parts[1]))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(struct {
			Series    qualification.Series    `json:"series"`
			Scorecard qualification.Scorecard `json:"scorecard"`
		}{series, score})
	default:
		http.NotFound(w, r)
	}
}

func parseLimit(raw string) int {
	value := 20
	if raw != "" {
		_, _ = fmt.Sscan(raw, &value)
	}
	if value <= 0 {
		value = 20
	}
	if value > 100 {
		value = 100
	}
	return value
}
