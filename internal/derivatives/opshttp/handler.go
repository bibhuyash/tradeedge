// Package opshttp exposes bounded, GET-only derivatives validation state.
package opshttp

import (
	"encoding/json"
	"net/http"

	"github.com/bibhuyash/tradeedge/internal/derivatives"
)

type Source interface{ Snapshot() derivatives.Snapshot }
type Handler struct{ source Source }

func New(source Source) http.Handler { return Handler{source: source} }
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/api/v1/derivatives/status" {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(h.source.Snapshot())
}
