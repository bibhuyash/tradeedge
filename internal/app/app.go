package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/bibhuyash/tradeedge/internal/adapters/marketdata/calendarfile"
	marketdatafile "github.com/bibhuyash/tradeedge/internal/adapters/marketdata/file"
	prometheusmetrics "github.com/bibhuyash/tradeedge/internal/adapters/metrics/prometheus"
	telegramadapter "github.com/bibhuyash/tradeedge/internal/adapters/notification/telegram"
	strategymemory "github.com/bibhuyash/tradeedge/internal/adapters/strategy/memory"
	"github.com/bibhuyash/tradeedge/internal/config"
	zerodhaintegration "github.com/bibhuyash/tradeedge/internal/integration/zerodha"
	"github.com/bibhuyash/tradeedge/internal/marketdata/opshttp"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
	"github.com/bibhuyash/tradeedge/internal/notification"
	"github.com/bibhuyash/tradeedge/internal/operations"
	"github.com/bibhuyash/tradeedge/internal/platform/httpserver"
	"github.com/bibhuyash/tradeedge/internal/qualification"
	qualificationopshttp "github.com/bibhuyash/tradeedge/internal/qualification/opshttp"
	strategyopshttp "github.com/bibhuyash/tradeedge/internal/strategy/opshttp"
	strategyrunner "github.com/bibhuyash/tradeedge/internal/strategy/runner"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	if cfg.ZerodhaMode == config.ZerodhaModePaper {
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("validate configuration: %w", err)
		}
		return runProductionPaper(ctx, cfg, logger)
	}
	return RunWithOptions(ctx, cfg, logger, Options{})
}

type Options struct {
	MarketReadiness         httpserver.MarketReadinessSource
	RuntimeReadiness        httpserver.RuntimeReadinessSource
	Metrics                 *prometheusmetrics.Recorder
	Quality                 opshttp.QualitySource
	LatestObservations      opshttp.LatestSource
	StrategyOperations      http.Handler
	ExecutionOperations     http.Handler
	IntegrationOperations   http.Handler
	IntegrationRuntime      interface{ Shutdown(context.Context) error }
	StrategyRunner          interface{ Shutdown(context.Context) error }
	RuntimeOperations       http.Handler
	TradingRuntime          interface{ Shutdown(context.Context) error }
	OperationalOperations   http.Handler
	NotificationRuntime     interface{ Shutdown(context.Context) error }
	QualificationOperations http.Handler
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
	operationalOperations := options.OperationalOperations
	notificationRuntime := options.NotificationRuntime
	if operationalOperations == nil && notificationRuntime == nil {
		var sender notification.Sender = telegramadapter.Disabled{}
		if enabled, botToken, chatID := cfg.Telegram(); enabled {
			adapter, adapterErr := telegramadapter.New(telegramadapter.Config{Enabled: true, Token: botToken, ChatID: chatID}, &http.Client{}, time.Now)
			if adapterErr != nil {
				return fmt.Errorf("compose Telegram notifications: %w", adapterErr)
			}
			sender = adapter
		}
		subsystem, subsystemErr := operations.NewSubsystem(sender, metrics)
		if subsystemErr != nil {
			return fmt.Errorf("compose operational subsystem: %w", subsystemErr)
		}
		notificationRuntime = subsystem
		operationalOperations = subsystem.Handler()
	}
	integrationOperations := options.IntegrationOperations
	integrationRuntime := options.IntegrationRuntime
	if integrationOperations == nil && integrationRuntime == nil {
		mode := cfg.ZerodhaMode
		if mode == "" {
			mode = config.ZerodhaModeOffline
		}
		runtime, runtimeErr := zerodhaintegration.New(zerodhaintegration.Mode(mode), zerodhaintegration.Dependencies{})
		if runtimeErr != nil {
			return fmt.Errorf("compose zerodha integration: %w", runtimeErr)
		}
		integrationRuntime = runtime
		integrationOperations = zerodhaintegration.NewHandler(runtime, 2*cfg.ShutdownTimeout/10)
	}
	strategyOperations := options.StrategyOperations
	strategyRuntime := options.StrategyRunner
	if strategyOperations == nil && strategyRuntime == nil {
		store := strategymemory.NewStore()
		source := options.MarketReadiness
		if source == nil {
			source = disabledMarketReadiness{}
		}
		gate, gateErr := strategyrunner.NewSnapshotReadinessGate(source)
		if gateErr != nil {
			return fmt.Errorf("create strategy readiness gate: %w", gateErr)
		}
		value, runnerErr := strategyrunner.New(strategyrunner.Config{
			MaxConcurrency: cfg.StrategyMaxConcurrency,
			Timeout:        cfg.StrategyTimeout,
		}, strategyrunner.NewRegistry(), store, gate, strategyrunner.RealClock{}, metrics)
		if runnerErr != nil {
			return fmt.Errorf("create strategy runner: %w", runnerErr)
		}
		strategyRuntime = value
		strategyOperations = strategyopshttp.New(
			store, value, 2*cfg.ShutdownTimeout/10,
		)
	}
	qualificationOperations := options.QualificationOperations
	if qualificationOperations == nil {
		engine, qualificationErr := qualification.New(qualification.DefaultPolicy(), nil)
		if qualificationErr != nil {
			return fmt.Errorf("compose qualification engine: %w", qualificationErr)
		}
		qualificationOperations, qualificationErr = qualificationopshttp.New(engine)
		if qualificationErr != nil {
			return fmt.Errorf("compose qualification operations: %w", qualificationErr)
		}
	}
	operations := opshttp.Dependencies{Timeout: 2 * cfg.ShutdownTimeout / 10}
	if options.MarketReadiness != nil {
		operations.Readiness = options.MarketReadiness
	}
	operations.Quality = options.Quality
	operations.Latest = options.LatestObservations
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
		MarketReadiness:         options.MarketReadiness,
		RuntimeReadiness:        options.RuntimeReadiness,
		Metrics:                 metrics.Handler(),
		Operations:              opshttp.New(operations),
		StrategyOperations:      strategyOperations,
		ExecutionOperations:     options.ExecutionOperations,
		IntegrationOperations:   integrationOperations,
		RuntimeOperations:       options.RuntimeOperations,
		OperationalOperations:   operationalOperations,
		QualificationOperations: qualificationOperations,
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
	var shutdownErrors []error
	if options.TradingRuntime != nil {
		if err := options.TradingRuntime.Shutdown(shutdownCtx); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("shutdown trading runtime: %w", err))
		}
	} else if strategyRuntime != nil {
		if err := strategyRuntime.Shutdown(shutdownCtx); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("shutdown strategy runner: %w", err))
		}
	}
	if options.TradingRuntime == nil && integrationRuntime != nil {
		if err := integrationRuntime.Shutdown(shutdownCtx); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("shutdown zerodha integration: %w", err))
		}
	}
	if notificationRuntime != nil {
		if err := notificationRuntime.Shutdown(shutdownCtx); err != nil {
			shutdownErrors = append(shutdownErrors, fmt.Errorf("shutdown notifications: %w", err))
		}
	}
	if err := server.Shutdown(shutdownCtx); err != nil {
		shutdownErrors = append(shutdownErrors, fmt.Errorf("shutdown HTTP server: %w", err))
	}
	if err := <-serverErrors; err != nil {
		shutdownErrors = append(shutdownErrors, fmt.Errorf("serve HTTP during shutdown: %w", err))
	}
	if len(shutdownErrors) > 0 {
		return errors.Join(shutdownErrors...)
	}
	logger.Info("application stopped")
	return nil
}

type disabledMarketReadiness struct{}

func (disabledMarketReadiness) Snapshot(context.Context) readiness.Snapshot {
	return readiness.Snapshot{
		State:         readiness.StateDisabled,
		Reasons:       []readiness.ReasonCode{readiness.ReasonMarketDataDisabled},
		PolicyVersion: "disabled/v1", EvaluatedAt: time.Now().UTC(),
	}
}
