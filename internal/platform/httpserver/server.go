package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
)

type Readiness struct {
	ready atomic.Bool
}

func (r *Readiness) Set(ready bool) { r.ready.Store(ready) }
func (r *Readiness) IsReady() bool  { return r.ready.Load() }

type Server struct {
	server    *http.Server
	readiness *Readiness
	listener  net.Listener
}

type MarketReadinessSource interface {
	Snapshot(context.Context) readiness.Snapshot
}

type Options struct {
	MarketReadiness    MarketReadinessSource
	Operations         http.Handler
	StrategyOperations http.Handler
	Metrics            http.Handler
}

func New(address string, logger *slog.Logger, readiness *Readiness) (*Server, error) {
	return NewWithOptions(address, logger, readiness, Options{})
}

func NewWithOptions(address string, logger *slog.Logger, readiness *Readiness, options Options) (*Server, error) {
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	if readiness == nil {
		return nil, errors.New("readiness is required")
	}
	return &Server{
		server: &http.Server{
			Addr:              address,
			Handler:           NewHandlerWithOptions(readiness, options),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       60 * time.Second,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		},
		readiness: readiness,
	}, nil
}

func NewHandler(readiness *Readiness) http.Handler {
	return NewHandlerWithOptions(readiness, Options{})
}

func NewHandlerWithOptions(process *Readiness, options Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", methodGET(func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusOK, "ok")
	}))
	mux.HandleFunc("/readyz", methodGET(func(w http.ResponseWriter, r *http.Request) {
		if !process.IsReady() {
			writeStatus(w, http.StatusServiceUnavailable, "not_ready")
			return
		}
		snapshot := readiness.Snapshot{
			EvaluatedAt: time.Now().UTC(), State: readiness.StateDisabled,
			Reasons: []readiness.ReasonCode{readiness.ReasonMarketDataDisabled},
		}
		if options.MarketReadiness != nil {
			snapshot = options.MarketReadiness.Snapshot(r.Context())
		}
		status := http.StatusOK
		text := "ready"
		if !snapshot.OperationallyReady() {
			status = http.StatusServiceUnavailable
			text = "not_ready"
		}
		writeJSON(w, status, map[string]any{
			"status": text, "trading_permitted": snapshot.TradingPermitted,
			"market_data_state": snapshot.State, "reason_codes": snapshot.Reasons,
			"evaluated_at": snapshot.EvaluatedAt, "calendar_version": snapshot.CalendarVersion,
		})
	}))
	if options.Metrics != nil {
		mux.Handle("/metrics", methodGETHandler(options.Metrics))
	}
	if options.Operations != nil {
		mux.Handle("/api/v1/market-data/", options.Operations)
	}
	if options.StrategyOperations != nil {
		mux.Handle("/api/v1/strategy/", options.StrategyOperations)
	}
	return mux
}

func (s *Server) Start() (<-chan error, error) {
	listener, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return nil, err
	}
	s.listener = listener
	errCh := make(chan error, 1)
	go func() {
		err := s.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
		close(errCh)
	}()
	return errCh, nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.readiness.Set(false)
	return s.server.Shutdown(ctx)
}

func (s *Server) Address() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func methodGET(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeStatus(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		next(w, r)
	}
}

func methodGETHandler(next http.Handler) http.Handler {
	return methodGET(next.ServeHTTP)
}

func writeStatus(w http.ResponseWriter, code int, status string) {
	writeJSON(w, code, map[string]string{"status": status})
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
