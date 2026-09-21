package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	operatorstartup "github.com/bibhuyash/tradeedge/internal/operator/startup"
	"github.com/bibhuyash/tradeedge/internal/session"
)

const maximumExchangeBody = 4 << 10

type SessionService interface {
	Status(context.Context) (session.Status, error)
	LoginURL(context.Context) (string, error)
	Exchange(context.Context, string) (session.Status, error)
}

type StartupService interface {
	Status(context.Context) operatorstartup.Status
	Start(context.Context) operatorstartup.Status
}

func NewHandler(service SessionService, startupServices ...StartupService) http.Handler {
	mux := http.NewServeMux()
	var startup StartupService
	if len(startupServices) > 0 {
		startup = startupServices[0]
	}
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
	mux.HandleFunc("/api/v1/operator/startup", func(writer http.ResponseWriter, request *http.Request) {
		if startup == nil {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"state": "FAILED", "reason": "startup_unavailable"})
			return
		}
		switch request.Method {
		case http.MethodGet:
			writeJSON(writer, http.StatusOK, startup.Status(request.Context()))
		case http.MethodPost:
			status := startup.Status(request.Context())
			if status.State != operatorstartup.Ready && status.State != operatorstartup.Preparing && status.State != operatorstartup.StartingShadow && status.State != operatorstartup.WaitingForReadiness {
				go startup.Start(context.Background())
			}
			writeJSON(writer, http.StatusAccepted, startup.Status(request.Context()))
		default:
			methodNotAllowed(writer, http.MethodGet+", "+http.MethodPost)
		}
	})
	mux.HandleFunc("/api/v1/operator/state", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, http.MethodGet)
			return
		}
		if startup == nil {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"view": "CONTROL_UNAVAILABLE", "reason": "startup_unavailable"})
			return
		}
		sessionStatus, err := service.Status(request.Context())
		if err != nil {
			writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"view": "CONTROL_UNAVAILABLE", "reason": "session_unavailable"})
			return
		}
		startupStatus := startup.Status(request.Context())
		writeJSON(writer, http.StatusOK, map[string]any{"view": operatorView(sessionStatus, startupStatus), "session": sessionStatus, "startup": startupStatus})
	})
	return mux
}

func operatorView(sessionStatus session.Status, startupStatus operatorstartup.Status) string {
	if sessionStatus.State != session.StateAuthenticated {
		return "LOGIN"
	}
	switch startupStatus.State {
	case operatorstartup.MarketClosed:
		return "MARKET_CLOSED"
	case operatorstartup.Failed:
		return "FAILED"
	case operatorstartup.Ready:
		return "RUNTIME"
	default:
		return "PREPARING"
	}
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
