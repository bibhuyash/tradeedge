package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

const (
	ModePaper = "paper"
)

type Config struct {
	Environment            string
	HTTPAddress            string
	LogLevel               string
	ShutdownTimeout        time.Duration
	TradingMode            string
	MarketDataCalendarPath string
	MarketDataDatasetRoot  string
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
	}

	if raw, ok := lookup("TRADEEDGE_SHUTDOWN_TIMEOUT"); ok {
		timeout, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return Config{}, fmt.Errorf("parse TRADEEDGE_SHUTDOWN_TIMEOUT: %w", err)
		}
		cfg.ShutdownTimeout = timeout
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
	return nil
}

func envOrDefault(lookup LookupEnv, key, fallback string) string {
	if value, ok := lookup(key); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}
