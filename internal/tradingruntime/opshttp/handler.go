// Package opshttp exposes bounded, read-only Phase 7 runtime diagnostics.
package opshttp

import (
	"encoding/json"
	"net/http"

	"github.com/bibhuyash/tradeedge/internal/tradingruntime"
)

type Source interface {
	Snapshot() tradingruntime.RuntimeSnapshot
}

func New(source Source) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runtime/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if source == nil {
			http.Error(w, "runtime unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(source.Snapshot())
	})
	return mux
}
