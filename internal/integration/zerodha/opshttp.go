package zerodha

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Handler struct {
	runtime *Runtime
	timeout time.Duration
}

func NewHandler(runtime *Runtime, timeout time.Duration) http.Handler {
	if timeout <= 0 || timeout > 10*time.Second {
		timeout = 2 * time.Second
	}
	return Handler{runtime: runtime, timeout: timeout}
}
func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		write(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	if handler.runtime == nil {
		write(writer, http.StatusServiceUnavailable, map[string]string{"error": "integration_unavailable"})
		return
	}
	limit, ok := parseLimit(request)
	if !ok {
		write(writer, http.StatusBadRequest, map[string]string{"error": "invalid_limit"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	switch strings.TrimSuffix(request.URL.Path, "/") {
	case "/api/v1/integrations/zerodha/health":
		health := handler.runtime.Health(ctx)
		status := http.StatusOK
		if health.State == StateBlocked || health.State == StateStopped {
			status = http.StatusServiceUnavailable
		}
		write(writer, status, health)
	case "/api/v1/integrations/zerodha/errors":
		write(writer, http.StatusOK, map[string]any{"items": handler.runtime.Errors(limit), "limit": limit})
	case "/api/v1/integrations/zerodha/shadow":
		write(writer, http.StatusOK, map[string]any{"items": handler.runtime.Shadow(limit), "limit": limit})
	case "/api/v1/integrations/zerodha/unknown":
		count, err := handler.runtime.Unknown(ctx)
		if err != nil {
			write(writer, http.StatusServiceUnavailable, map[string]string{"error": "unknown_status_unavailable"})
			return
		}
		write(writer, http.StatusOK, map[string]any{"count": count, "limit": limit})
	case "/api/v1/integrations/zerodha/reconciliation":
		write(writer, http.StatusOK, handler.runtime.Reconciliation())
	default:
		write(writer, http.StatusNotFound, map[string]string{"error": "not_found"})
	}
}
func parseLimit(request *http.Request) (int, bool) {
	raw := request.URL.Query().Get("limit")
	if raw == "" {
		return 50, true
	}
	value, err := strconv.Atoi(raw)
	return value, err == nil && value > 0 && value <= 100
}
func write(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
