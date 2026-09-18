package controlplane

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	brokerzerodha "github.com/bibhuyash/tradeedge/internal/adapters/broker/zerodha"
	"github.com/bibhuyash/tradeedge/internal/adapters/sessionfile"
	"github.com/bibhuyash/tradeedge/internal/session"
)

type Config struct {
	HTTPAddress     string
	SessionFile     string
	ShutdownTimeout time.Duration
	Zerodha         brokerzerodha.Config
	apiKey          string
	apiSecret       string
}

type Dependencies struct {
	RoundTripper http.RoundTripper
	Clock        session.Clock
}

type Application struct {
	config   Config
	service  *session.Service
	handler  http.Handler
	server   *http.Server
	listener net.Listener
}

func LoadConfig(lookup brokerzerodha.LookupEnv) (Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	zerodhaConfig, err := brokerzerodha.LoadConfig(lookup)
	if err != nil || !zerodhaConfig.Enabled {
		return Config{}, brokerzerodha.ErrInvalidConfiguration
	}
	value := Config{
		HTTPAddress: "127.0.0.1:8081", SessionFile: ".cache/tradeedge/session/zerodha.json",
		ShutdownTimeout: 10 * time.Second, Zerodha: zerodhaConfig,
		apiKey:    valueOf(lookup, "TRADEEDGE_ZERODHA_API_KEY"),
		apiSecret: valueOf(lookup, "TRADEEDGE_ZERODHA_API_SECRET"),
	}
	if raw := valueOf(lookup, "TRADEEDGE_CONTROL_HTTP_ADDR"); raw != "" {
		value.HTTPAddress = raw
	}
	if raw := valueOf(lookup, "TRADEEDGE_ZERODHA_SESSION_FILE"); raw != "" {
		value.SessionFile = raw
	}
	if raw := valueOf(lookup, "TRADEEDGE_SHUTDOWN_TIMEOUT"); raw != "" {
		value.ShutdownTimeout, err = time.ParseDuration(raw)
		if err != nil {
			return Config{}, errors.New("invalid control-plane shutdown timeout")
		}
	}
	if _, _, err = net.SplitHostPort(value.HTTPAddress); err != nil || value.ShutdownTimeout <= 0 || value.ShutdownTimeout > time.Minute {
		return Config{}, errors.New("invalid control-plane configuration")
	}
	return value, nil
}

func New(ctx context.Context, config Config, dependencies Dependencies) (*Application, error) {
	store, err := sessionfile.New(config.SessionFile)
	if err != nil {
		return nil, errors.New("configure session store")
	}
	clock := dependencies.Clock
	if clock == nil {
		clock = session.RealClock{}
	}
	port, err := brokerzerodha.NewSessionAuthPort(config.Zerodha, config.apiKey, config.apiSecret, dependencies.RoundTripper, clockAdapter{clock})
	if err != nil {
		return nil, errors.New("configure Zerodha authentication")
	}
	service, err := session.NewService(ctx, store, port, clock)
	if err != nil {
		return nil, errors.New("configure session service")
	}
	handler := NewHandler(service)
	return &Application{config: config, service: service, handler: handler}, nil
}

func (a *Application) Handler() http.Handler     { return a.handler }
func (a *Application) Service() *session.Service { return a.service }

func (a *Application) Run(ctx context.Context, logger *slog.Logger) error {
	if logger == nil {
		return errors.New("logger is required")
	}
	a.server = &http.Server{
		Addr: a.config.HTTPAddress, Handler: a.handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	listener, err := net.Listen("tcp", a.config.HTTPAddress)
	if err != nil {
		return errors.New("start control-plane HTTP server")
	}
	a.listener = listener
	errorsCh := make(chan error, 1)
	go func() {
		serveErr := a.server.Serve(listener)
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		errorsCh <- serveErr
	}()
	logger.Info("control plane started", "http_address", listener.Addr().String())
	select {
	case err = <-errorsCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.config.ShutdownTimeout)
	defer cancel()
	if err = a.server.Shutdown(shutdownCtx); err != nil {
		return errors.New("shutdown control-plane HTTP server")
	}
	if err = <-errorsCh; err != nil {
		return errors.New("serve control-plane HTTP during shutdown")
	}
	logger.Info("control plane stopped")
	return nil
}

func (a *Application) Address() string {
	if a.listener == nil {
		return ""
	}
	return a.listener.Addr().String()
}

type clockAdapter struct{ clock session.Clock }

func (c clockAdapter) Now() time.Time { return c.clock.Now() }

func valueOf(lookup brokerzerodha.LookupEnv, key string) string {
	value, _ := lookup(key)
	return strings.TrimSpace(value)
}
