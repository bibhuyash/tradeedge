package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/session"
)

const maximumExchangeBody = 4 << 10

type SessionService interface {
	Status(context.Context) (session.Status, error)
	LoginURL(context.Context) (string, error)
	Exchange(context.Context, string) (session.Status, error)
}

func NewHandler(service SessionService) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/v1/session/status", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		status, err := service.Status(request.Context())
		if err != nil {
			writeJSON(writer, http.StatusServiceUnavailable, errorResponse(session.Status{Provider: session.ProviderZerodha, State: session.StateError}, "session_unavailable"))
			return
		}
		writeJSON(writer, http.StatusOK, status)
	})
	mux.HandleFunc("/api/v1/session/login-url", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		loginURL, err := service.LoginURL(request.Context())
		if err != nil {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "login_url_unavailable"})
			return
		}
		writeJSON(writer, http.StatusOK, map[string]string{"provider": session.ProviderZerodha, "login_url": loginURL})
	})
	mux.HandleFunc("/api/v1/session/exchange", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, http.MethodPost)
			return
		}
		request.Body = http.MaxBytesReader(writer, request.Body, maximumExchangeBody)
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var input struct {
			RequestToken string `json:"request_token"`
		}
		if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.RequestToken) == "" {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
		defer cancel()
		status, err := service.Exchange(ctx, input.RequestToken)
		input.RequestToken = ""
		switch {
		case err == nil:
			writeJSON(writer, http.StatusOK, status)
		case errors.Is(err, session.ErrInvalidRequestToken):
			writeJSON(writer, http.StatusUnauthorized, errorResponse(status, "login_required"))
		case errors.Is(err, session.ErrBusy):
			writeJSON(writer, http.StatusConflict, errorResponse(status, "exchange_in_progress"))
		default:
			writeJSON(writer, http.StatusServiceUnavailable, errorResponse(session.Status{Provider: session.ProviderZerodha, State: session.StateError}, "authentication_unavailable"))
		}
	})
	return mux
}

func errorResponse(status session.Status, code string) map[string]any {
	response := map[string]any{"provider": status.Provider, "state": status.State, "reused": status.Reused, "error": code}
	if !status.ExpiresAt.IsZero() {
		response["expires_at"] = status.ExpiresAt.UTC()
	}
	return response
}

func methodNotAllowed(writer http.ResponseWriter, allowed string) {
	writer.Header().Set("Allow", allowed)
	writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
