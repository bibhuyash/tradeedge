// Package zerodha contains the read-only Phase 5 M1 Zerodha connectivity adapter.
// It intentionally exposes no order submission, modification, or cancellation API.
package zerodha

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	ProviderName = "zerodha"
	defaultURL   = "https://api.kite.trade"
)

var (
	ErrInvalidConfiguration = errors.New("invalid zerodha read-only configuration")
	ErrCredentialsMissing   = errors.New("zerodha credentials missing")
	ErrCredentialsMalformed = errors.New("zerodha credentials malformed")
	ErrAuthentication       = errors.New("zerodha authentication failed")
	ErrSessionExpired       = errors.New("zerodha session expired")
	ErrRateLimited          = errors.New("zerodha read rate limited")
	ErrUnavailable          = errors.New("zerodha read unavailable")
	ErrMalformedResponse    = errors.New("malformed zerodha response")
	ErrStopped              = errors.New("zerodha read client stopped")
	ErrMappingMissing       = errors.New("zerodha instrument mapping missing")
	ErrMappingStale         = errors.New("zerodha instrument mapping stale")
	ErrMappingAmbiguous     = errors.New("zerodha instrument mapping ambiguous")
	ErrDerivativeExpired    = errors.New("zerodha derivative expired")
)

type Config struct {
	Enabled        bool
	BaseURL        string
	Timeout        time.Duration
	MaxConcurrency int
	ReadRetries    int
	MappingMaxAge  time.Duration
}

type LookupEnv func(string) (string, bool)

func LoadConfig(lookup LookupEnv) (Config, error) {
	cfg := Config{BaseURL: defaultURL, Timeout: 2 * time.Second, MaxConcurrency: 4, ReadRetries: 2, MappingMaxAge: 24 * time.Hour}
	if raw, ok := lookup("TRADEEDGE_ZERODHA_READ_ONLY"); ok {
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return Config{}, ErrInvalidConfiguration
		}
		cfg.Enabled = value
	}
	if raw, ok := lookup("TRADEEDGE_ZERODHA_BASE_URL"); ok {
		cfg.BaseURL = strings.TrimSpace(raw)
	}
	if err := parseDuration(lookup, "TRADEEDGE_ZERODHA_TIMEOUT", &cfg.Timeout); err != nil {
		return Config{}, err
	}
	if err := parseInt(lookup, "TRADEEDGE_ZERODHA_MAX_CONCURRENCY", &cfg.MaxConcurrency); err != nil {
		return Config{}, err
	}
	if err := parseInt(lookup, "TRADEEDGE_ZERODHA_READ_RETRIES", &cfg.ReadRetries); err != nil {
		return Config{}, err
	}
	if err := parseDuration(lookup, "TRADEEDGE_ZERODHA_MAPPING_MAX_AGE", &cfg.MappingMaxAge); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	parsed, err := url.Parse(c.BaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ErrInvalidConfiguration
	}
	if c.Timeout <= 0 || c.Timeout > 30*time.Second || c.MaxConcurrency <= 0 || c.MaxConcurrency > 32 ||
		c.ReadRetries < 0 || c.ReadRetries > 5 || c.MappingMaxAge <= 0 || c.MappingMaxAge > 7*24*time.Hour {
		return ErrInvalidConfiguration
	}
	return nil
}

func parseDuration(lookup LookupEnv, key string, destination *time.Duration) error {
	if raw, ok := lookup(key); ok {
		value, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("%w: duration", ErrInvalidConfiguration)
		}
		*destination = value
	}
	return nil
}

func parseInt(lookup LookupEnv, key string, destination *int) error {
	if raw, ok := lookup(key); ok {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("%w: integer", ErrInvalidConfiguration)
		}
		*destination = value
	}
	return nil
}
