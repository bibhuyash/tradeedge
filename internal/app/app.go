package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/bibhuyash/tradeedge/internal/adapters/marketdata/calendarfile"
	marketdatafile "github.com/bibhuyash/tradeedge/internal/adapters/marketdata/file"
	prometheusmetrics "github.com/bibhuyash/tradeedge/internal/adapters/metrics/prometheus"
	"github.com/bibhuyash/tradeedge/internal/config"
	"github.com/bibhuyash/tradeedge/internal/marketdata/opshttp"
	"github.com/bibhuyash/tradeedge/internal/platform/httpserver"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	return RunWithOptions(ctx, cfg, logger, Options{})
}

type Options struct {
	MarketReadiness httpserver.MarketReadinessSource
	Metrics         *prometheusmetrics.Recorder
	Quality         opshttp.QualitySource
}

func RunWithMarketReadiness(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	marketReadiness httpserver.MarketReadinessSource,
) error {
	return RunWithOptions(ctx, cfg, logger, Options{MarketReadiness: marketReadiness})
}

func RunWithOptions(
	ctx context.Context,
	cfg config.Config,
	logger *slog.Logger,
	options Options,
) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate configuration: %w", err)
	}
	readiness := &httpserver.Readiness{}
	metrics := options.Metrics
	if metrics == nil {
		metrics = prometheusmetrics.New()
	}
	operations := opshttp.Dependencies{Timeout: 2 * cfg.ShutdownTimeout / 10}
	if options.MarketReadiness != nil {
		operations.Readiness = options.MarketReadiness
	}
	operations.Quality = options.Quality
	if cfg.MarketDataCalendarPath != "" {
		schedule, loadErr := calendarfile.Load(cfg.MarketDataCalendarPath)
		if loadErr != nil {
			return fmt.Errorf("load market-data calendar: %w", loadErr)
		}
		operations.Calendar = schedule
	}
	if cfg.MarketDataDatasetRoot != "" {
		operations.Datasets = marketdatafile.Repository{
			Root: cfg.MarketDataDatasetRoot, Telemetry: metrics,
		}
	}
	server, err := httpserver.NewWithOptions(cfg.HTTPAddress, logger, readiness, httpserver.Options{
		MarketReadiness: options.MarketReadiness,
		Metrics:         metrics.Handler(),
		Operations:      opshttp.New(operations),
	})
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
