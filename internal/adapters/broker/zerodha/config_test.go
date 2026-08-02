package zerodha

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestConfigAndCredentialLoadingFailsClosedAndRedacts(t *testing.T) {
	lookup := mapLookup(map[string]string{
		"TRADEEDGE_ZERODHA_READ_ONLY": "true", "TRADEEDGE_ZERODHA_TIMEOUT": "500ms",
		"TRADEEDGE_ZERODHA_MAX_CONCURRENCY": "3", "TRADEEDGE_ZERODHA_READ_RETRIES": "1",
		"TRADEEDGE_ZERODHA_MAPPING_MAX_AGE": "12h",
	})
	cfg, err := LoadConfig(lookup)
	if err != nil || !cfg.Enabled || cfg.Timeout != 500*time.Millisecond || cfg.MaxConcurrency != 3 || cfg.ReadRetries != 1 {
		t.Fatalf("LoadConfig() = %#v, %v", cfg, err)
	}
	for name, values := range map[string]map[string]string{
		"malformed boolean": {"TRADEEDGE_ZERODHA_READ_ONLY": "perhaps"},
		"insecure URL":      {"TRADEEDGE_ZERODHA_BASE_URL": "http://api.kite.trade"},
		"unbounded timeout": {"TRADEEDGE_ZERODHA_TIMEOUT": "31s"},
		"bad retry":         {"TRADEEDGE_ZERODHA_READ_RETRIES": "6"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfig(mapLookup(values)); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("LoadConfig() error = %v", err)
			}
		})
	}

	if _, err := (EnvCredentialSource{Lookup: mapLookup(nil)}).Load(context.Background()); !errors.Is(err, ErrCredentialsMissing) {
		t.Fatalf("missing credentials error = %v", err)
	}
	secretValues := map[string]string{
		"TRADEEDGE_ZERODHA_API_KEY": "key-value", "TRADEEDGE_ZERODHA_API_SECRET": "secret-value",
		"TRADEEDGE_ZERODHA_REQUEST_TOKEN": "request-value",
	}
	material, err := (EnvCredentialSource{Lookup: mapLookup(secretValues)}).Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	formatted := fmt.Sprintf("%v|%+v|%#v", material, material, material)
	for _, forbidden := range secretValues {
		if strings.Contains(formatted, forbidden) {
			t.Fatalf("credential leaked in formatting: %q", formatted)
		}
	}
	if material.String() != "[REDACTED]" {
		t.Fatalf("String() = %q", material.String())
	}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("credential check", "credential", material)
	for _, forbidden := range secretValues {
		if strings.Contains(logged.String(), forbidden) {
			t.Fatalf("credential leaked in structured log: %q", logged.String())
		}
	}
}

func TestCredentialRestorationRequiresExplicitExpiry(t *testing.T) {
	values := map[string]string{"TRADEEDGE_ZERODHA_API_KEY": "key", "TRADEEDGE_ZERODHA_API_SECRET": "secret", "TRADEEDGE_ZERODHA_ACCESS_TOKEN": "token"}
	if _, err := (EnvCredentialSource{Lookup: mapLookup(values)}).Load(context.Background()); !errors.Is(err, ErrCredentialsMalformed) {
		t.Fatalf("Load() error = %v", err)
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
