// Package opshttp exposes bounded GET-only Phase 7 M2 operational evidence.
package opshttp

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/bibhuyash/tradeedge/internal/notification"
	"github.com/bibhuyash/tradeedge/internal/operations/cas"
	"github.com/bibhuyash/tradeedge/internal/operations/reporting"
)

type NotificationSource interface {
	Health() notification.Health
	ProviderStatus() notification.ProviderStatus
}
type Dependencies struct {
	Notifications NotificationSource
	Store         *notification.Store
	CAS           *cas.Store
	Reports       *reporting.Accumulator
}

func New(deps Dependencies) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/notifications/health", get(func(w http.ResponseWriter, _ *http.Request) {
		if deps.Notifications == nil {
			write(w, http.StatusServiceUnavailable, map[string]string{"state": "UNAVAILABLE"})
			return
		}
		write(w, http.StatusOK, deps.Notifications.Health())
	}))
	mux.HandleFunc("/api/v1/notifications/events", get(func(w http.ResponseWriter, r *http.Request) {
		if deps.Store == nil {
			write(w, http.StatusServiceUnavailable, map[string]string{"state": "UNAVAILABLE"})
			return
		}
		write(w, http.StatusOK, map[string]any{"items": deps.Store.RecentEvents(limit(r)), "limit": limit(r)})
	}))
	mux.HandleFunc("/api/v1/notifications/failures", get(func(w http.ResponseWriter, r *http.Request) {
		if deps.Store == nil {
			write(w, http.StatusServiceUnavailable, map[string]string{"state": "UNAVAILABLE"})
			return
		}
		write(w, http.StatusOK, map[string]any{"items": deps.Store.RecentDeliveries(limit(r), true), "limit": limit(r)})
	}))
	mux.HandleFunc("/api/v1/notifications/queue", get(func(w http.ResponseWriter, _ *http.Request) {
		if deps.Notifications == nil {
			write(w, http.StatusServiceUnavailable, map[string]string{"state": "UNAVAILABLE"})
			return
		}
		write(w, http.StatusOK, deps.Notifications.Health())
	}))
	mux.HandleFunc("/api/v1/notifications/providers/telegram", get(func(w http.ResponseWriter, _ *http.Request) {
		if deps.Notifications == nil {
			write(w, http.StatusServiceUnavailable, map[string]string{"state": "UNAVAILABLE"})
			return
		}
		write(w, http.StatusOK, deps.Notifications.ProviderStatus())
	}))
	mux.HandleFunc("/api/v1/operations/cas-evidence", get(func(w http.ResponseWriter, r *http.Request) {
		if deps.CAS == nil {
			write(w, http.StatusServiceUnavailable, map[string]string{"state": "UNAVAILABLE"})
			return
		}
		write(w, http.StatusOK, map[string]any{"items": deps.CAS.Recent(limit(r)), "limit": limit(r)})
	}))
	mux.HandleFunc("/api/v1/operations/eod/latest", get(func(w http.ResponseWriter, _ *http.Request) {
		if deps.Reports == nil {
			write(w, http.StatusServiceUnavailable, map[string]string{"state": "UNAVAILABLE"})
			return
		}
		value, ok := deps.Reports.Latest()
		if !ok {
			write(w, http.StatusNotFound, map[string]string{"state": "NOT_FOUND"})
			return
		}
		write(w, http.StatusOK, value)
	}))
	return mux
}
func get(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			write(w, http.StatusMethodNotAllowed, map[string]string{"status": "method_not_allowed"})
			return
		}
		next(w, r)
	}
}
func limit(r *http.Request) int {
	value := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			value = parsed
		}
	}
	if value < 1 {
		value = 1
	}
	if value > 100 {
		value = 100
	}
	return value
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
