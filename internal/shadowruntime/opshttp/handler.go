// Package opshttp exposes bounded GET-only live SHADOW operational state.
package opshttp

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/bibhuyash/tradeedge/internal/shadowruntime"
)

type Source interface {
	Snapshot() shadowruntime.Snapshot
	Status() []shadowruntime.UnderlyingStatus
	SessionScorecards() []shadowruntime.SessionScorecard
	MultiSessionScorecards() []shadowruntime.MultiSessionScorecard
}

type Handler struct{ source Source }

func New(source Source) (http.Handler, error) {
	if source == nil {
		return nil, shadowruntime.ErrInvalid
	}
	return Handler{source: source}, nil
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch path {
	case "/api/v1/shadow/runtime":
		snapshot := h.source.Snapshot()
		_ = json.NewEncoder(w).Encode(struct {
			Mode, Strategy, Candidate, Qualification, BrokerOrders string
			Revision                                               uint64
			Status                                                 []shadowruntime.UnderlyingStatus
		}{"SHADOW", "EMA_REFERENCE_V1", "REFERENCE_CANDIDATE", "NOT_ALPHA_QUALIFIED", "DISABLED", snapshot.Revision, h.source.Status()})
	case "/api/v1/shadow/warmup", "/api/v1/shadow/strategies":
		_ = json.NewEncoder(w).Encode(h.source.Status())
	case "/api/v1/shadow/sessions", "/api/v1/shadow/scorecards":
		_ = json.NewEncoder(w).Encode(h.source.SessionScorecards())
	case "/api/v1/shadow/multi-session":
		_ = json.NewEncoder(w).Encode(h.source.MultiSessionScorecards())
	default:
		http.NotFound(w, r)
	}
}
