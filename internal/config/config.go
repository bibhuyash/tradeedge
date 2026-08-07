package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ModePaper               = "paper"
	ZerodhaModeOffline      = "OFFLINE"
	ZerodhaModePaper        = "PAPER"
	ZerodhaModeShadow       = "SHADOW"
	ZerodhaModeLiveDisabled = "LIVE_DISABLED"
)

type Config struct {
	Environment            string
	HTTPAddress            string
	LogLevel               string
	ShutdownTimeout        time.Duration
	TradingMode            string
	MarketDataCalendarPath string
	MarketDataDatasetRoot  string
	StrategyMaxConcurrency int
	StrategyTimeout        time.Duration
	RiskMaxConcurrency     int
	RiskTimeout            time.Duration
	ZerodhaMode            string
	ZerodhaReadOnly        bool
}

type LookupEnv func(string) (string, bool)

func Load() (Config, error) {
	return LoadWithLookup(os.LookupEnv)
}

func LoadWithLookup(lookup LookupEnv) (Config, error) {
	cfg := Config{
		Environment:            envOrDefault(lookup, "TRADEEDGE_ENV", "development"),
		HTTPAddress:            envOrDefault(lookup, "TRADEEDGE_HTTP_ADDR", ":8080"),
		LogLevel:               strings.ToLower(envOrDefault(lookup, "TRADEEDGE_LOG_LEVEL", "info")),
		ShutdownTimeout:        10 * time.Second,
		TradingMode:            strings.ToLower(envOrDefault(lookup, "TRADEEDGE_TRADING_MODE", ModePaper)),
		MarketDataCalendarPath: envOrDefault(lookup, "TRADEEDGE_MARKETDATA_CALENDAR", ""),
		MarketDataDatasetRoot:  envOrDefault(lookup, "TRADEEDGE_MARKETDATA_DATASET_ROOT", ""),
		StrategyMaxConcurrency: 4,
		StrategyTimeout:        100 * time.Millisecond,
		RiskMaxConcurrency:     4,
		RiskTimeout:            100 * time.Millisecond,
		ZerodhaMode:            strings.ToUpper(envOrDefault(lookup, "TRADEEDGE_ZERODHA_MODE", ZerodhaModeOffline)),
	}

	if raw, ok := lookup("TRADEEDGE_SHUTDOWN_TIMEOUT"); ok {
		timeout, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return Config{}, fmt.Errorf("parse TRADEEDGE_SHUTDOWN_TIMEOUT: %w", err)
		}
		cfg.ShutdownTimeout = timeout
	}
	if raw, ok := lookup("TRADEEDGE_STRATEGY_MAX_CONCURRENCY"); ok {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return Config{}, fmt.Errorf("parse TRADEEDGE_STRATEGY_MAX_CONCURRENCY: %w", err)
		}
		cfg.StrategyMaxConcurrency = value
	}
	if raw, ok := lookup("TRADEEDGE_STRATEGY_TIMEOUT"); ok {
		value, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return Config{}, fmt.Errorf("parse TRADEEDGE_STRATEGY_TIMEOUT: %w", err)
		}
		cfg.StrategyTimeout = value
	}
	if raw, ok := lookup("TRADEEDGE_RISK_MAX_CONCURRENCY"); ok {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return Config{}, fmt.Errorf("parse TRADEEDGE_RISK_MAX_CONCURRENCY: %w", err)
		}
		cfg.RiskMaxConcurrency = value
	}
	if raw, ok := lookup("TRADEEDGE_RISK_TIMEOUT"); ok {
		value, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return Config{}, fmt.Errorf("parse TRADEEDGE_RISK_TIMEOUT: %w", err)
		}
		cfg.RiskTimeout = value
	}
	if raw, ok := lookup("TRADEEDGE_ZERODHA_READ_ONLY"); ok {
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return Config{}, fmt.Errorf("parse TRADEEDGE_ZERODHA_READ_ONLY: %w", err)
		}
		cfg.ZerodhaReadOnly = value
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Environment) == "" {
		return errors.New("TRADEEDGE_ENV cannot be empty")
	}
	if _, _, err := net.SplitHostPort(c.HTTPAddress); err != nil {
		return fmt.Errorf("invalid TRADEEDGE_HTTP_ADDR: %w", err)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid TRADEEDGE_LOG_LEVEL %q", c.LogLevel)
	}
	if c.ShutdownTimeout <= 0 {
		return errors.New("TRADEEDGE_SHUTDOWN_TIMEOUT must be positive")
	}
	if c.TradingMode != ModePaper {
		return fmt.Errorf("TRADEEDGE_TRADING_MODE must be %q; live trading is unavailable", ModePaper)
	}
	if c.StrategyMaxConcurrency <= 0 || c.StrategyMaxConcurrency > 64 {
		return errors.New("TRADEEDGE_STRATEGY_MAX_CONCURRENCY must be between 1 and 64")
	}
	if c.StrategyTimeout <= 0 || c.StrategyTimeout > time.Minute {
		return errors.New("TRADEEDGE_STRATEGY_TIMEOUT must be positive and at most one minute")
	}
	if c.RiskMaxConcurrency <= 0 || c.RiskMaxConcurrency > 64 {
		return errors.New("TRADEEDGE_RISK_MAX_CONCURRENCY must be between 1 and 64")
	}
	if c.RiskTimeout <= 0 || c.RiskTimeout > time.Minute {
		return errors.New("TRADEEDGE_RISK_TIMEOUT must be positive and at most one minute")
	}
	switch c.ZerodhaMode {
	case "", ZerodhaModeOffline, ZerodhaModePaper, ZerodhaModeShadow, ZerodhaModeLiveDisabled:
	default:
		return fmt.Errorf("invalid TRADEEDGE_ZERODHA_MODE %q; live trading is unavailable", c.ZerodhaMode)
	}
	if (c.ZerodhaMode == ZerodhaModePaper || c.ZerodhaMode == ZerodhaModeShadow) && !c.ZerodhaReadOnly {
		return errors.New("TRADEEDGE_ZERODHA_READ_ONLY=true is required for PAPER or SHADOW")
	}
	return nil
}

func envOrDefault(lookup LookupEnv, key, fallback string) string {
	if value, ok := lookup(key); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}
