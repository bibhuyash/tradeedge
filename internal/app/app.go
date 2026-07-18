package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bibhuyash/tradeedge/internal/config"
	"github.com/bibhuyash/tradeedge/internal/platform/httpserver"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate configuration: %w", err)
	}
	readiness := &httpserver.Readiness{}
	server, err := httpserver.New(cfg.HTTPAddress, logger, readiness)
	if err != nil {
		return fmt.Errorf("create HTTP server: %w", err)
	}
	serverErrors, err := server.Start()
	if err != nil {
		return fmt.Errorf("start HTTP server: %w", err)
	}

	readiness.Set(true)
	logger.Info("application started",
		"environment", cfg.Environment,
		"trading_mode", cfg.TradingMode,
		"http_address", server.Address(),
	)

	select {
	case err := <-serverErrors:
		readiness.Set(false)
		if err != nil {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	case <-ctx.Done():
		readiness.Set(false)
		logger.Info("shutdown requested")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}
	if err := <-serverErrors; err != nil {
		return fmt.Errorf("serve HTTP during shutdown: %w", err)
	}
	logger.Info("application stopped")
	return nil
}
