package zerodha

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/bibhuyash/tradeedge/internal/session"
)

var v2APIKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// SessionAuthPort adapts the provider-specific token exchange to the
// provider-neutral control-plane session boundary. Credential values are
// intentionally private and never take part in formatting or error messages.
type SessionAuthPort struct {
	apiKey    string
	apiSecret string
	exchanger *HTTPTokenExchanger
	clock     Clock
}

func (*SessionAuthPort) String() string                 { return "[REDACTED]" }
func (*SessionAuthPort) Format(state fmt.State, _ rune) { _, _ = state.Write([]byte("[REDACTED]")) }
func (*SessionAuthPort) LogValue() slog.Value           { return slog.StringValue("[REDACTED]") }

func NewSessionAuthPort(config Config, apiKey, apiSecret string, roundTripper http.RoundTripper, clock Clock) (*SessionAuthPort, error) {
	apiKey, apiSecret = strings.TrimSpace(apiKey), strings.TrimSpace(apiSecret)
	if !v2APIKeyPattern.MatchString(apiKey) || apiSecret == "" || len(apiSecret) > 512 || strings.ContainsAny(apiSecret, "\r\n\x00") {
		return nil, ErrCredentialsMalformed
	}
	if clock == nil {
		clock = RealClock{}
	}
	exchanger, err := NewHTTPTokenExchanger(config, roundTripper, clock)
	if err != nil {
		return nil, err
	}
	return &SessionAuthPort{apiKey: apiKey, apiSecret: apiSecret, exchanger: exchanger, clock: clock}, nil
}

func (p *SessionAuthPort) LoginURL(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	login, _ := url.Parse("https://kite.zerodha.com/connect/login")
	query := login.Query()
	query.Set("v", "3")
	query.Set("api_key", p.apiKey)
	login.RawQuery = query.Encode()
	return login.String(), nil
}

func (p *SessionAuthPort) Exchange(ctx context.Context, requestToken string) (session.Record, error) {
	result, err := p.exchanger.Exchange(ctx, p.apiKey, p.apiSecret, requestToken)
	if err != nil {
		var failure AuthenticationFailure
		if errors.As(err, &failure) && failure.HTTPStatus == http.StatusForbidden && failure.ErrorType == "TokenException" {
			return session.Record{}, session.ErrInvalidRequestToken
		}
		return session.Record{}, session.ErrUnavailable
	}
	now := p.clock.Now().UTC()
	return session.Record{
		Provider: session.ProviderZerodha, AccessToken: result.accessToken,
		ExpiresAt: result.expiresAt.UTC(), AuthenticatedAt: now,
	}, nil
}

func NewStoredSessionManager(apiKey string, record session.Record, clock Clock) (*SessionManager, error) {
	apiKey = strings.TrimSpace(apiKey)
	if !v2APIKeyPattern.MatchString(apiKey) || record.Provider != session.ProviderZerodha ||
		strings.TrimSpace(record.AccessToken) == "" || strings.ContainsAny(record.AccessToken, "\r\n\x00") ||
		record.ExpiresAt.IsZero() {
		return nil, ErrCredentialsMalformed
	}
	if clock == nil {
		clock = RealClock{}
	}
	if !record.ExpiresAt.After(clock.Now()) {
		return nil, ErrSessionExpired
	}
	return NewSessionManager(CredentialMaterial{
		apiKey: apiKey, accessToken: record.AccessToken, expiresAt: record.ExpiresAt.UTC(),
	}, nil, clock, nil), nil
}

var _ session.AuthPort = (*SessionAuthPort)(nil)
