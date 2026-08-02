package zerodha

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestHTTPTransportRateLimitRetryTimeoutAndReadOnlyPaths(t *testing.T) {
	cfg := Config{Enabled: true, BaseURL: "https://api.kite.trade", Timeout: 25 * time.Millisecond, MaxConcurrency: 2, ReadRetries: 1, MappingMaxAge: time.Hour}
	var mu sync.Mutex
	calls := 0
	transport, err := NewHTTPTransport(cfg, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || (request.URL.Path != "/user/profile" && request.URL.Path != "/instruments") {
			t.Fatalf("unsafe request reached transport: %s %s", request.Method, request.URL.Path)
		}
		mu.Lock()
		calls++
		current := calls
		mu.Unlock()
		if current == 1 {
			return nil, errors.New("temporary")
		}
		return response(http.StatusOK, validProfile), nil
	}))
	if err != nil {
		t.Fatalf("NewHTTPTransport() error = %v", err)
	}
	if _, _, err = transport.Profile(context.Background(), "redacted"); err != nil || calls != 2 {
		t.Fatalf("Profile() error = %v, calls = %d", err, calls)
	}

	rateLimited, _ := NewHTTPTransport(cfg, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusTooManyRequests, `{}`), nil
	}))
	if _, _, err = rateLimited.Profile(context.Background(), "redacted"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate-limit error = %v", err)
	}

	timed, _ := NewHTTPTransport(cfg, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	if _, _, err = timed.Profile(context.Background(), "redacted"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	timed.CloseIdleConnections()
	if _, _, err = timed.Profile(context.Background(), "redacted"); !errors.Is(err, ErrStopped) {
		t.Fatalf("stopped error = %v", err)
	}
}

func TestHTTPTokenExchangeUsesOnlySessionEndpointAndDailyExpiry(t *testing.T) {
	now := time.Date(2026, 8, 2, 1, 0, 0, 0, time.UTC)
	clock := &fixedClock{now: now}
	cfg := Config{BaseURL: "https://api.kite.trade", Timeout: time.Second, MaxConcurrency: 1, MappingMaxAge: time.Hour}
	exchanger, err := NewHTTPTokenExchanger(cfg, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/session/token" {
			t.Fatalf("unexpected token request: %s %s", request.Method, request.URL.Path)
		}
		return response(http.StatusOK, `{"status":"success","data":{"access_token":"token-value"}}`), nil
	}), clock)
	if err != nil {
		t.Fatalf("NewHTTPTokenExchanger() error = %v", err)
	}
	result, err := exchanger.Exchange(context.Background(), "key", "secret", "request")
	if err != nil || result.accessToken == "" || !result.expiresAt.After(now) {
		t.Fatalf("Exchange() = %#v, %v", result, err)
	}
}
