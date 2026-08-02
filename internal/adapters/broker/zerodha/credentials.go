package zerodha

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// CredentialMaterial is deliberately opaque outside this package. Its string
// representation is always redacted and errors never include secret values.
type CredentialMaterial struct {
	apiKey       string
	apiSecret    string
	requestToken string
	accessToken  string
	expiresAt    time.Time
}

func (CredentialMaterial) String() string { return "[REDACTED]" }

func (CredentialMaterial) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte("[REDACTED]"))
}

func (CredentialMaterial) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

type CredentialSource interface {
	Load(context.Context) (CredentialMaterial, error)
}

type EnvCredentialSource struct{ Lookup LookupEnv }

func (source EnvCredentialSource) Load(ctx context.Context) (CredentialMaterial, error) {
	if err := ctx.Err(); err != nil {
		return CredentialMaterial{}, err
	}
	if source.Lookup == nil {
		return CredentialMaterial{}, ErrCredentialsMissing
	}
	value := CredentialMaterial{
		apiKey:       valueOf(source.Lookup, "TRADEEDGE_ZERODHA_API_KEY"),
		apiSecret:    valueOf(source.Lookup, "TRADEEDGE_ZERODHA_API_SECRET"),
		requestToken: valueOf(source.Lookup, "TRADEEDGE_ZERODHA_REQUEST_TOKEN"),
		accessToken:  valueOf(source.Lookup, "TRADEEDGE_ZERODHA_ACCESS_TOKEN"),
	}
	if value.apiKey == "" || value.apiSecret == "" {
		return CredentialMaterial{}, ErrCredentialsMissing
	}
	if strings.ContainsAny(value.apiKey+value.apiSecret+value.requestToken+value.accessToken, "\r\n\x00") {
		return CredentialMaterial{}, ErrCredentialsMalformed
	}
	if value.accessToken != "" {
		raw := valueOf(source.Lookup, "TRADEEDGE_ZERODHA_ACCESS_TOKEN_EXPIRES_AT")
		expires, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return CredentialMaterial{}, ErrCredentialsMalformed
		}
		value.expiresAt = expires.UTC()
	}
	return value, nil
}

func valueOf(lookup LookupEnv, key string) string {
	value, _ := lookup(key)
	return strings.TrimSpace(value)
}
