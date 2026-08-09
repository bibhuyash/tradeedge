// Package opshttp exposes bounded GET-only financial diagnostics.
package opshttp

import (
	"context"
	"encoding/json"
	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	"github.com/bibhuyash/tradeedge/internal/accounting/valuation"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Repository interface {
	Current(context.Context, portfoliomodel.PortfolioID) (valuation.PortfolioFinancialSnapshot, accountingmodel.StateChecksum, error)
	LastComplete(context.Context, portfoliomodel.PortfolioID) (valuation.PortfolioFinancialSnapshot, error)
	Valuations(context.Context, portfoliomodel.PortfolioID, int) ([]valuation.PositionValuation, error)
}
type HealthSource interface{ Health() valuation.Health }
type ReconciliationSource interface {
	Reconciliation(context.Context, portfoliomodel.PortfolioID) (any, error)
}
type Handler struct {
	repository     Repository
	health         HealthSource
	reconciliation ReconciliationSource
	timeout        time.Duration
}

func New(repository Repository, health HealthSource, timeout time.Duration) http.Handler {
	if timeout <= 0 || timeout > 10*time.Second {
		timeout = 2 * time.Second
	}
	return Handler{repository: repository, health: health, timeout: timeout}
}
func NewWithReconciliation(repository Repository, health HealthSource, reconciliation ReconciliationSource, timeout time.Duration) http.Handler {
	value := New(repository, health, timeout).(Handler)
	value.reconciliation = reconciliation
	return value
}
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		write(w, 405, map[string]string{"error": "method_not_allowed"})
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/v1/financial/health" {
		if h.health == nil {
			write(w, 503, map[string]string{"error": "health_unavailable"})
			return
		}
		value := h.health.Health()
		code := 200
		if value.Closed || value.LastFailure != "" {
			code = 503
		}
		write(w, code, value)
		return
	}
	rawID := r.URL.Query().Get("portfolio_id")
	id, err := portfoliomodel.ParsePortfolioID(rawID)
	if err != nil {
		write(w, 400, map[string]string{"error": "invalid_portfolio_id"})
		return
	}
	if h.repository == nil {
		write(w, 503, map[string]string{"error": "repository_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	switch path {
	case "/api/v1/financial/snapshot", "/api/v1/financial/pnl", "/api/v1/financial/exposure", "/api/v1/financial/readiness":
		value, _, readErr := h.repository.Current(ctx, id)
		if readErr != nil {
			write(w, 503, map[string]string{"error": "financial_state_unavailable"})
			return
		}
		write(w, 200, value)
	case "/api/v1/financial/last-complete":
		value, readErr := h.repository.LastComplete(ctx, id)
		if readErr != nil {
			write(w, 404, map[string]string{"error": "complete_snapshot_not_found"})
			return
		}
		write(w, 200, value)
	case "/api/v1/financial/reconciliation":
		if h.reconciliation == nil {
			write(w, 503, map[string]string{"error": "reconciliation_unavailable"})
			return
		}
		value, readErr := h.reconciliation.Reconciliation(ctx, id)
		if readErr != nil {
			write(w, 503, map[string]string{"error": "reconciliation_unavailable"})
			return
		}
		write(w, 200, value)
	case "/api/v1/financial/positions":
		limit := 50
		if text := r.URL.Query().Get("limit"); text != "" {
			parsed, e := strconv.Atoi(text)
			if e != nil || parsed < 1 || parsed > 100 {
				write(w, 400, map[string]string{"error": "invalid_limit"})
				return
			}
			limit = parsed
		}
		values, readErr := h.repository.Valuations(ctx, id, limit)
		if readErr != nil {
			write(w, 503, map[string]string{"error": "valuations_unavailable"})
			return
		}
		write(w, 200, map[string]any{"items": values, "limit": limit})
	default:
		write(w, 404, map[string]string{"error": "not_found"})
	}
}
func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
