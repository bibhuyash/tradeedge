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
	baseURL   string
	client    *http.Client
	clock     Clock
	failure   AuthenticationFailure
	failureMu sync.RWMutex
}

// AuthenticationFailure contains only bounded, sanitized provider diagnostics.
// Error deliberately remains generic so accidental formatting cannot reveal
// response contents or credential material.
type AuthenticationFailure struct {
	ErrorType  string
	Message    string
	HTTPStatus int
}

func (AuthenticationFailure) Error() string { return ErrAuthentication.Error() }

func (AuthenticationFailure) Unwrap() error { return ErrAuthentication }

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
	exchanger.clearFailure()
	digest := sha256.Sum256([]byte(apiKey + requestToken + apiSecret))
	checksum := hex.EncodeToString(digest[:])
	form := url.Values{"api_key": {apiKey}, "request_token": {requestToken}, "checksum": {checksum}}
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
	var payload struct {
		Status    string `json:"status"`
		Message   string `json:"message"`
		ErrorType string `json:"error_type"`
		Data      struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decodeErr := decoder.Decode(&payload)
	if response.StatusCode < 200 || response.StatusCode >= 300 || decodeErr != nil || payload.Status != "success" || strings.TrimSpace(payload.Data.AccessToken) == "" {
		if decodeErr == nil && strings.EqualFold(strings.TrimSpace(payload.Status), "error") {
			if failure, ok := newAuthenticationFailure(payload.ErrorType, payload.Message, response.StatusCode, apiKey, apiSecret, requestToken, checksum); ok {
				exchanger.setFailure(failure)
				return TokenExchangeResult{}, failure
			}
		}
		return TokenExchangeResult{}, ErrAuthentication
	}
	return TokenExchangeResult{accessToken: payload.Data.AccessToken, expiresAt: nextSessionExpiry(exchanger.clock.Now())}, nil
}

// LastAuthenticationFailure returns the latest classified provider failure.
// It never returns raw response data or credential values.
func (exchanger *HTTPTokenExchanger) LastAuthenticationFailure() (AuthenticationFailure, bool) {
	exchanger.failureMu.RLock()
	defer exchanger.failureMu.RUnlock()
	return exchanger.failure, exchanger.failure.HTTPStatus != 0
}

func (exchanger *HTTPTokenExchanger) clearFailure() {
	exchanger.failureMu.Lock()
	exchanger.failure = AuthenticationFailure{}
	exchanger.failureMu.Unlock()
}

func (exchanger *HTTPTokenExchanger) setFailure(value AuthenticationFailure) {
	exchanger.failureMu.Lock()
	exchanger.failure = value
	exchanger.failureMu.Unlock()
}

func newAuthenticationFailure(errorType, message string, status int, sensitive ...string) (AuthenticationFailure, bool) {
	errorType = strings.TrimSpace(errorType)
	if status < 100 || status > 599 || !safeErrorType(errorType, sensitive...) {
		return AuthenticationFailure{}, false
	}
	message = sanitizeAuthenticationMessage(message, sensitive...)
	if message == "" {
		return AuthenticationFailure{}, false
	}
	return AuthenticationFailure{ErrorType: errorType, Message: message, HTTPStatus: status}, true
}

func safeErrorType(value string, sensitive ...string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (index > 0 && character >= '0' && character <= '9') || (index > 0 && (character == '_' || character == '-' || character == '.')) {
			continue
		}
		return false
	}
	lower := strings.ToLower(value)
	for _, secret := range sensitive {
		if secret != "" && strings.Contains(value, secret) {
			return false
		}
	}
	for _, forbidden := range []string{"api_key", "api-secret", "api_secret", "request_token", "access_token", "checksum", "authorization"} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	return true
}

func sanitizeAuthenticationMessage(value string, sensitive ...string) string {
	value = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, value)
	for _, secret := range sensitive {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "checksum") && (strings.Contains(lower, "invalid") || strings.Contains(lower, "mismatch")) {
		return "Invalid checksum"
	}
	if strings.Contains(lower, "token") && (strings.Contains(lower, "invalid") || strings.Contains(lower, "expired")) {
		return "Token is invalid or expired"
	}
	for _, forbidden := range []string{"api_key", "api key", "api_secret", "api secret", "request_token", "request token", "access_token", "access token", "checksum", "authorization"} {
		value = replaceFold(value, forbidden, "[REDACTED]")
	}
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 256 {
		value = string(runes[:256])
	}
	return value
}

func replaceFold(value, old, replacement string) string {
	for {
		index := strings.Index(strings.ToLower(value), strings.ToLower(old))
		if index < 0 {
			return value
		}
		value = value[:index] + replacement + value[index+len(old):]
	}
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
