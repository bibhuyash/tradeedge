package zerodha

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fixedClock struct{ now time.Time }

func (clock *fixedClock) Now() time.Time { return clock.now }

type fakeExchanger struct {
	result TokenExchangeResult
	err    error
	calls  int
}

func (value *fakeExchanger) Exchange(context.Context, string, string, string) (TokenExchangeResult, error) {
	value.calls++
	return value.result, value.err
}

func TestSessionAuthenticationExpiryAndNoRefresh(t *testing.T) {
	now := time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	clock := &fixedClock{now: now}
	exchanger := &fakeExchanger{result: TokenExchangeResult{accessToken: "access-value", expiresAt: now.Add(time.Hour)}}
	manager := NewSessionManager(CredentialMaterial{apiKey: "api-value", apiSecret: "secret-value", requestToken: "request-value"}, exchanger, clock, nil)
	if manager.Snapshot().State != SessionLoginRequired {
		t.Fatalf("initial state = %s", manager.Snapshot().State)
	}
	if err := manager.Authenticate(context.Background()); err != nil || manager.Snapshot().State != SessionAuthenticated {
		t.Fatalf("Authenticate() = %v, state %s", err, manager.Snapshot().State)
	}
	header, err := manager.Authorization()
	if err != nil || !strings.HasPrefix(header, "token ") {
		t.Fatalf("Authorization() = %q, %v", header, err)
	}
	clock.now = now.Add(2 * time.Hour)
	if _, err := manager.Authorization(); !errors.Is(err, ErrSessionExpired) || manager.Snapshot().State != SessionExpired {
		t.Fatalf("expired Authorization() error = %v, state %s", err, manager.Snapshot().State)
	}
	if exchanger.calls != 1 {
		t.Fatalf("token exchange calls = %d, want 1; refresh must not be invented", exchanger.calls)
	}
}

func TestSessionAuthenticationFailureAndRestartRestoration(t *testing.T) {
	now := time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	clock := &fixedClock{now: now}
	failed := NewSessionManager(CredentialMaterial{apiKey: "key", apiSecret: "secret", requestToken: "request"}, &fakeExchanger{err: errors.New("opaque")}, clock, nil)
	if err := failed.Authenticate(context.Background()); !errors.Is(err, ErrAuthentication) || failed.Snapshot().State != SessionAuthFailed {
		t.Fatalf("Authenticate() = %v, state %s", err, failed.Snapshot().State)
	}
	restored := NewSessionManager(CredentialMaterial{apiKey: "key", apiSecret: "secret", accessToken: "access", expiresAt: now.Add(time.Hour)}, nil, clock, nil)
	if restored.Snapshot().State != SessionAuthenticated {
		t.Fatalf("restored state = %s", restored.Snapshot().State)
	}
	expired := NewSessionManager(CredentialMaterial{apiKey: "key", apiSecret: "secret", accessToken: "access", expiresAt: now.Add(-time.Second)}, nil, clock, nil)
	if expired.Snapshot().State != SessionExpired {
		t.Fatalf("expired restored state = %s", expired.Snapshot().State)
	}
	restored.Shutdown()
	if _, err := restored.Authorization(); !errors.Is(err, ErrStopped) {
		t.Fatalf("Authorization() after Shutdown error = %v", err)
	}
}

func TestValidRestoredAccessTokenPrecedesRequestTokenExchange(t *testing.T) {
	now := time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)
	exchanger := &fakeExchanger{result: TokenExchangeResult{accessToken: "unexpected", expiresAt: now.Add(time.Hour)}}
	manager := NewSessionManager(CredentialMaterial{apiKey: "key", apiSecret: "secret", requestToken: "unconsumed-or-consumed", accessToken: "restored", expiresAt: now.Add(time.Hour)}, exchanger, &fixedClock{now: now}, nil)
	if err := manager.Authenticate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exchanger.calls != 0 {
		t.Fatalf("request-token exchanges=%d", exchanger.calls)
	}
	header, err := manager.Authorization()
	if err != nil || header != "token key:restored" {
		t.Fatalf("authorization=%q err=%v", header, err)
	}
}

func TestSessionAuthenticationPreservesSanitizedProviderFailure(t *testing.T) {
	now := time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC)
	providerFailure := AuthenticationFailure{ErrorType: "TokenException", Message: "Invalid checksum", HTTPStatus: 403}
	manager := NewSessionManager(
		CredentialMaterial{apiKey: "key", apiSecret: "secret", requestToken: "request"},
		&fakeExchanger{err: providerFailure},
		&fixedClock{now: now},
		nil,
	)
	err := manager.Authenticate(context.Background())
	var propagated AuthenticationFailure
	if !errors.Is(err, ErrAuthentication) || !errors.As(err, &propagated) || propagated != providerFailure {
		t.Fatalf("Authenticate() error = %#v", err)
	}
	if manager.Snapshot().State != SessionAuthFailed {
		t.Fatalf("state = %s", manager.Snapshot().State)
	}
}
