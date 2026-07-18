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

func New(address string, logger *slog.Logger, readiness *Readiness) (*Server, error) {
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	if readiness == nil {
		return nil, errors.New("readiness is required")
	}
	return &Server{
		server: &http.Server{
			Addr:              address,
			Handler:           NewHandler(readiness),
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
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", methodGET(func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusOK, "ok")
	}))
	mux.HandleFunc("/readyz", methodGET(func(w http.ResponseWriter, _ *http.Request) {
		if !readiness.IsReady() {
			writeStatus(w, http.StatusServiceUnavailable, "not_ready")
			return
		}
		writeStatus(w, http.StatusOK, "ready")
	}))
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

func writeStatus(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}
