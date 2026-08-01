package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadWithLookupDefaults(t *testing.T) {
	cfg, err := LoadWithLookup(mapLookup(nil))
	if err != nil {
		t.Fatalf("LoadWithLookup() error = %v", err)
	}
	if cfg.Environment != "development" || cfg.HTTPAddress != ":8080" ||
		cfg.LogLevel != "info" || cfg.TradingMode != ModePaper ||
		cfg.ShutdownTimeout != 10*time.Second || cfg.StrategyMaxConcurrency != 4 ||
		cfg.StrategyTimeout != 100*time.Millisecond {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if cfg.RiskMaxConcurrency != 4 || cfg.RiskTimeout != 100*time.Millisecond {
		t.Fatalf("unexpected risk defaults: %#v", cfg)
	}
}

func TestLoadWithLookupOverrides(t *testing.T) {
	cfg, err := LoadWithLookup(mapLookup(map[string]string{
		"TRADEEDGE_ENV":                      "test",
		"TRADEEDGE_HTTP_ADDR":                "127.0.0.1:9090",
		"TRADEEDGE_LOG_LEVEL":                "DEBUG",
		"TRADEEDGE_SHUTDOWN_TIMEOUT":         "250ms",
		"TRADEEDGE_TRADING_MODE":             "PAPER",
		"TRADEEDGE_MARKETDATA_CALENDAR":      "configs/nse-calendar.json",
		"TRADEEDGE_MARKETDATA_DATASET_ROOT":  "data/marketdata",
		"TRADEEDGE_STRATEGY_MAX_CONCURRENCY": "8",
		"TRADEEDGE_STRATEGY_TIMEOUT":         "75ms",
		"TRADEEDGE_RISK_MAX_CONCURRENCY":     "6",
		"TRADEEDGE_RISK_TIMEOUT":             "80ms",
	}))
	if err != nil {
		t.Fatalf("LoadWithLookup() error = %v", err)
	}
	if cfg.LogLevel != "debug" || cfg.ShutdownTimeout != 250*time.Millisecond || cfg.TradingMode != ModePaper ||
		cfg.MarketDataCalendarPath != "configs/nse-calendar.json" ||
		cfg.MarketDataDatasetRoot != "data/marketdata" {
		t.Fatalf("unexpected overrides: %#v", cfg)
	}
	if cfg.StrategyMaxConcurrency != 8 || cfg.StrategyTimeout != 75*time.Millisecond {
		t.Fatalf("unexpected strategy overrides: %#v", cfg)
	}
	if cfg.RiskMaxConcurrency != 6 || cfg.RiskTimeout != 80*time.Millisecond {
		t.Fatalf("unexpected risk overrides: %#v", cfg)
	}
}

func TestLoadWithLookupRejectsInvalidValues(t *testing.T) {
	tests := map[string]map[string]string{
		"live mode":              {"TRADEEDGE_TRADING_MODE": "live"},
		"bad address":            {"TRADEEDGE_HTTP_ADDR": "8080"},
		"bad level":              {"TRADEEDGE_LOG_LEVEL": "trace"},
		"bad duration":           {"TRADEEDGE_SHUTDOWN_TIMEOUT": "later"},
		"negative timeout":       {"TRADEEDGE_SHUTDOWN_TIMEOUT": "-1s"},
		"empty environment":      {"TRADEEDGE_ENV": ""},
		"bad concurrency":        {"TRADEEDGE_STRATEGY_MAX_CONCURRENCY": "0"},
		"large concurrency":      {"TRADEEDGE_STRATEGY_MAX_CONCURRENCY": "65"},
		"bad strategy timeout":   {"TRADEEDGE_STRATEGY_TIMEOUT": "never"},
		"large strategy timeout": {"TRADEEDGE_STRATEGY_TIMEOUT": "61s"},
		"bad risk concurrency":   {"TRADEEDGE_RISK_MAX_CONCURRENCY": "0"},
		"large risk concurrency": {"TRADEEDGE_RISK_MAX_CONCURRENCY": "65"},
		"bad risk timeout":       {"TRADEEDGE_RISK_TIMEOUT": "never"},
		"large risk timeout":     {"TRADEEDGE_RISK_TIMEOUT": "61s"},
	}
	for name, values := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := LoadWithLookup(mapLookup(values))
			if err == nil {
				t.Fatal("LoadWithLookup() error = nil, want error")
			}
			if name == "live mode" && !strings.Contains(err.Error(), "live trading is unavailable") {
				t.Fatalf("error %q does not explain live-mode rejection", err)
			}
		})
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
