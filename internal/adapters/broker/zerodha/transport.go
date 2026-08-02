package zerodha

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const maxResponseBytes = 32 << 20

type ReadTransport interface {
	Profile(context.Context, string) ([]byte, int, error)
	Instruments(context.Context, string) ([]byte, int, error)
	CloseIdleConnections()
}

type HTTPTransport struct {
	baseURL string
	client  *http.Client
	retries int
	limit   chan struct{}
	mu      sync.RWMutex
	stopped bool
}

func NewHTTPTransport(config Config, roundTripper http.RoundTripper) (*HTTPTransport, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if roundTripper == nil {
		roundTripper = http.DefaultTransport
	}
	return &HTTPTransport{
		baseURL: strings.TrimRight(config.BaseURL, "/"), retries: config.ReadRetries,
		limit:  make(chan struct{}, config.MaxConcurrency),
		client: &http.Client{Transport: roundTripper, Timeout: config.Timeout},
	}, nil
}

func (transport *HTTPTransport) Profile(ctx context.Context, authorization string) ([]byte, int, error) {
	return transport.read(ctx, "/user/profile", authorization)
}

func (transport *HTTPTransport) Instruments(ctx context.Context, authorization string) ([]byte, int, error) {
	return transport.read(ctx, "/instruments", authorization)
}

func (transport *HTTPTransport) read(ctx context.Context, path, authorization string) ([]byte, int, error) {
	transport.mu.RLock()
	stopped := transport.stopped
	transport.mu.RUnlock()
	if stopped {
		return nil, 0, ErrStopped
	}
	select {
	case transport.limit <- struct{}{}:
		defer func() { <-transport.limit }()
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	var body []byte
	var status int
	var err error
	for attempt := 0; attempt <= transport.retries; attempt++ {
		body, status, err = transport.readOnce(ctx, path, authorization)
		if err == nil || status == http.StatusTooManyRequests || status == http.StatusForbidden || (status >= 400 && status < 500) {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	return body, status, err
}

func (transport *HTTPTransport) readOnce(ctx context.Context, path, authorization string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, transport.baseURL+path, nil)
	if err != nil {
		return nil, 0, ErrUnavailable
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("X-Kite-Version", "3")
	response, err := transport.client.Do(request)
	if err != nil {
		return nil, 0, errors.Join(ErrUnavailable, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return nil, response.StatusCode, ErrMalformedResponse
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return nil, response.StatusCode, ErrRateLimited
	}
	if response.StatusCode == http.StatusForbidden {
		return nil, response.StatusCode, ErrSessionExpired
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, response.StatusCode, ErrUnavailable
	}
	return body, response.StatusCode, nil
}

func (transport *HTTPTransport) CloseIdleConnections() {
	transport.mu.Lock()
	transport.stopped = true
	transport.mu.Unlock()
	transport.client.CloseIdleConnections()
}

type HTTPTokenExchanger struct {
	baseURL string
	client  *http.Client
	clock   Clock
}

func NewHTTPTokenExchanger(config Config, roundTripper http.RoundTripper, clock Clock) (*HTTPTokenExchanger, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if roundTripper == nil {
		roundTripper = http.DefaultTransport
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &HTTPTokenExchanger{baseURL: strings.TrimRight(config.BaseURL, "/"), client: &http.Client{Transport: roundTripper, Timeout: config.Timeout}, clock: clock}, nil
}

func (exchanger *HTTPTokenExchanger) Exchange(ctx context.Context, apiKey, apiSecret, requestToken string) (TokenExchangeResult, error) {
	digest := sha256.Sum256([]byte(apiKey + requestToken + apiSecret))
	form := url.Values{"api_key": {apiKey}, "request_token": {requestToken}, "checksum": {hex.EncodeToString(digest[:])}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, exchanger.baseURL+"/session/token", bytes.NewBufferString(form.Encode()))
	if err != nil {
		return TokenExchangeResult{}, ErrAuthentication
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Kite-Version", "3")
	response, err := exchanger.client.Do(request)
	if err != nil {
		return TokenExchangeResult{}, ErrAuthentication
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return TokenExchangeResult{}, ErrAuthentication
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if decoder.Decode(&payload) != nil || payload.Status != "success" || strings.TrimSpace(payload.Data.AccessToken) == "" {
		return TokenExchangeResult{}, ErrAuthentication
	}
	return TokenExchangeResult{accessToken: payload.Data.AccessToken, expiresAt: nextSessionExpiry(exchanger.clock.Now())}, nil
}

func nextSessionExpiry(now time.Time) time.Time {
	location, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		location = time.FixedZone("IST", 5*60*60+30*60)
	}
	local := now.In(location)
	expiry := time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, location)
	if !expiry.After(local) {
		expiry = expiry.AddDate(0, 0, 1)
	}
	return expiry.UTC()
}
