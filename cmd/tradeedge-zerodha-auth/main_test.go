package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	brokerzerodha "github.com/bibhuyash/tradeedge/internal/adapters/broker/zerodha"
)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type fakeMarketDialer struct {
	connection brokerzerodha.MarketConnection
	endpoint   string
}

func (dialer *fakeMarketDialer) Dial(_ context.Context, endpoint string) (brokerzerodha.MarketConnection, error) {
	dialer.endpoint = endpoint
	return dialer.connection, nil
}

type fakeMarketConnection struct {
	mu        sync.Mutex
	frames    []brokerzerodha.MarketFrame
	readError error
	writes    int
	closed    bool
}

func (connection *fakeMarketConnection) Read(ctx context.Context) (brokerzerodha.MarketFrame, error) {
	connection.mu.Lock()
	if len(connection.frames) > 0 {
		frame := connection.frames[0]
		connection.frames = connection.frames[1:]
		connection.mu.Unlock()
		return frame, nil
	}
	err := connection.readError
	connection.mu.Unlock()
	if err != nil {
		return brokerzerodha.MarketFrame{}, err
	}
	<-ctx.Done()
	return brokerzerodha.MarketFrame{}, ctx.Err()
}

func (connection *fakeMarketConnection) WriteJSON(context.Context, any) error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	connection.writes++
	return nil
}

func (connection *fakeMarketConnection) Close() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	connection.closed = true
	return nil
}

func TestLoginURLRequiresAPIKey(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"login-url"}, mapLookup(nil), &output, commandDependencies{}); !errors.Is(err, errInvalidConfiguration) {
		t.Fatalf("run() error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("unexpected output %q", output.String())
	}
}

func TestLoginURLIsSafeAndContainsNoSecret(t *testing.T) {
	values := map[string]string{apiKeyEnvironment: "public-key", "TRADEEDGE_ZERODHA_API_SECRET": "never-print-secret"}
	var output bytes.Buffer
	if err := run([]string{"login-url"}, mapLookup(values), &output, commandDependencies{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(output.String()); got != "https://kite.zerodha.com/connect/login?api_key=public-key&v=3" {
		t.Fatalf("login URL = %q", got)
	}
	if strings.Contains(output.String(), values["TRADEEDGE_ZERODHA_API_SECRET"]) {
		t.Fatal("API secret appeared in output")
	}
}

func TestExchangeTokenRejectsMissingAPIKeyAndSecret(t *testing.T) {
	base := map[string]string{"TRADEEDGE_ZERODHA_READ_ONLY": "true", requestTokenEnvironment: "request"}
	for name, values := range map[string]map[string]string{
		"missing api key":    base,
		"missing api secret": merge(base, map[string]string{apiKeyEnvironment: "key"}),
	} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			if err := run([]string{"exchange-token"}, mapLookup(values), &output, commandDependencies{}); !errors.Is(err, errAuthentication) {
				t.Fatalf("run() error = %v", err)
			}
			if output.Len() != 0 {
				t.Fatalf("unexpected output %q", output.String())
			}
		})
	}
}

func TestExchangeTokenRejectsInvalidRequestToken(t *testing.T) {
	values := credentialValues()
	values[requestTokenEnvironment] = "bad\nrequest"
	var output bytes.Buffer
	if err := run([]string{"exchange-token"}, mapLookup(values), &output, commandDependencies{}); !errors.Is(err, errAuthentication) {
		t.Fatalf("run() error = %v", err)
	}
}

func TestExchangeTokenFailureIsRedacted(t *testing.T) {
	values := credentialValues()
	values["TRADEEDGE_ZERODHA_API_SECRET"] = "recognizable-secret"
	values[requestTokenEnvironment] = "recognizable-request-token"
	dependencies := commandDependencies{roundTripper: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusForbidden, `{"status":"error"}`), nil
	})}
	var output bytes.Buffer
	err := run([]string{"exchange-token"}, mapLookup(values), &output, dependencies)
	if !errors.Is(err, errAuthentication) {
		t.Fatalf("run() error = %v", err)
	}
	combined := output.String() + err.Error()
	for _, secret := range []string{values["TRADEEDGE_ZERODHA_API_SECRET"], values[requestTokenEnvironment]} {
		if strings.Contains(combined, secret) {
			t.Fatalf("secret appeared in result: %q", combined)
		}
	}
}

func TestExchangeTokenSuccessAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	dependencies := commandDependencies{clock: fixedClock{now: now}, roundTripper: exchangeRoundTripper(t)}
	var output bytes.Buffer
	if err := run([]string{"exchange-token"}, mapLookup(credentialValues()), &output, dependencies); err != nil {
		t.Fatal(err)
	}
	want := "AUTHENTICATED\nACCESS_TOKEN_EXPIRES_AT=2026-08-11T00:30:00Z\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if strings.Contains(output.String(), "generated-access-token") {
		t.Fatal("access token appeared in output")
	}
}

func TestVerifyREST(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	values := restoredCredentialValues(now)
	dependencies := commandDependencies{clock: fixedClock{now: now}, roundTripper: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/user/profile" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		return response(http.StatusOK, `{"status":"success","data":{"exchanges":["NSE"],"products":["CNC"],"order_types":["LIMIT"]}}`), nil
	})}
	var output bytes.Buffer
	if err := run([]string{"verify-rest"}, mapLookup(values), &output, dependencies); err != nil {
		t.Fatal(err)
	}
	if output.String() != "REST_AUTH=PASS\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestVerifyWebSocketReceivesApprovedObservationsAndDisconnects(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	connection := &fakeMarketConnection{frames: []brokerzerodha.MarketFrame{
		{Binary: true, Data: quoteFrame(256265, now)},
		{Binary: true, Data: quoteFrame(260105, now)},
	}}
	dialer := &fakeMarketDialer{connection: connection}
	dependencies := websocketDependencies(now, dialer)
	var output bytes.Buffer
	if err := run([]string{"verify-websocket", "-runtime-bundle", "pinned.json", "-timeout", "1s"}, mapLookup(restoredCredentialValues(now)), &output, dependencies); err != nil {
		t.Fatal(err)
	}
	if output.String() != "WEBSOCKET_AUTH=PASS\nOBSERVATIONS_RECEIVED=2\nSHUTDOWN=PASS\n" {
		t.Fatalf("output = %q", output.String())
	}
	connection.mu.Lock()
	writes, closed := connection.writes, connection.closed
	connection.mu.Unlock()
	if writes != 2 || !closed {
		t.Fatalf("subscription writes=%d closed=%t", writes, closed)
	}
	if strings.Contains(dialer.endpoint, valuesFrom(restoredCredentialValues(now), "TRADEEDGE_ZERODHA_API_SECRET")) {
		t.Fatal("API secret appeared in WebSocket endpoint")
	}
}

func TestVerifyWebSocketTimeout(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	connection := &fakeMarketConnection{}
	dependencies := websocketDependencies(now, &fakeMarketDialer{connection: connection})
	var output bytes.Buffer
	err := run([]string{"verify-websocket", "-runtime-bundle", "pinned.json", "-timeout", "20ms"}, mapLookup(restoredCredentialValues(now)), &output, dependencies)
	if !errors.Is(err, errWebSocketVerification) || output.String() != "WEBSOCKET_AUTH=FAIL\nOBSERVATIONS_RECEIVED=0\nSHUTDOWN=PASS\n" {
		t.Fatalf("error=%v output=%q", err, output.String())
	}
}

func TestVerifyWebSocketDisconnect(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	connection := &fakeMarketConnection{frames: []brokerzerodha.MarketFrame{{Binary: true, Data: quoteFrame(256265, now)}}, readError: errors.New("disconnected")}
	dependencies := websocketDependencies(now, &fakeMarketDialer{connection: connection})
	var output bytes.Buffer
	err := run([]string{"verify-websocket", "-runtime-bundle", "pinned.json", "-timeout", "1s"}, mapLookup(restoredCredentialValues(now)), &output, dependencies)
	if !errors.Is(err, errWebSocketVerification) || !strings.Contains(output.String(), "OBSERVATIONS_RECEIVED=1") || !strings.Contains(output.String(), "SHUTDOWN=PASS") {
		t.Fatalf("error=%v output=%q", err, output.String())
	}
}

func TestCommandSurfaceHasNoMutationCapability(t *testing.T) {
	want := []string{"login-url", "exchange-token", "verify-rest", "verify-websocket"}
	if strings.Join(supportedCommands, ",") != strings.Join(want, ",") {
		t.Fatalf("supported commands = %v", supportedCommands)
	}
	for _, command := range []string{"place-order", "modify-order", "cancel-order", "LIVE_ENABLED"} {
		if err := run([]string{command}, mapLookup(nil), io.Discard, commandDependencies{}); !errors.Is(err, errInvalidCommand) {
			t.Fatalf("command %q error = %v", command, err)
		}
	}
}

func websocketDependencies(now time.Time, dialer brokerzerodha.MarketDialer) commandDependencies {
	config := brokerzerodha.DefaultMarketStreamConfig()
	config.MaxReconnects = 0
	config.InitialBackoff = time.Millisecond
	config.MaximumBackoff = time.Millisecond
	return commandDependencies{
		clock: nowClock(now), dialer: dialer, streamConfig: config,
		loadTokens: func(string) ([]string, error) { return []string{"256265", "260105"}, nil },
	}
}

func nowClock(now time.Time) brokerzerodha.Clock { return fixedClock{now: now} }

func credentialValues() map[string]string {
	return map[string]string{
		"TRADEEDGE_ZERODHA_READ_ONLY":  "true",
		apiKeyEnvironment:              "public-key",
		"TRADEEDGE_ZERODHA_API_SECRET": "api-secret",
		requestTokenEnvironment:        "request-token",
	}
}

func restoredCredentialValues(now time.Time) map[string]string {
	values := credentialValues()
	delete(values, requestTokenEnvironment)
	values[accessTokenEnvironment] = "access-token"
	values[accessExpiryEnvironment] = now.Add(time.Hour).Format(time.RFC3339)
	return values
}

func exchangeRoundTripper(t *testing.T) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/session/token" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		return response(http.StatusOK, `{"status":"success","data":{"access_token":"generated-access-token"}}`), nil
	})
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func quoteFrame(token uint32, at time.Time) []byte {
	packet := make([]byte, 32)
	binary.BigEndian.PutUint32(packet[0:4], token)
	binary.BigEndian.PutUint32(packet[4:8], 10000)
	binary.BigEndian.PutUint32(packet[8:12], 10100)
	binary.BigEndian.PutUint32(packet[12:16], 9900)
	binary.BigEndian.PutUint32(packet[16:20], 9950)
	binary.BigEndian.PutUint32(packet[20:24], 9980)
	binary.BigEndian.PutUint32(packet[28:32], uint32(at.Unix()))
	frame := make([]byte, 4+len(packet))
	binary.BigEndian.PutUint16(frame[0:2], 1)
	binary.BigEndian.PutUint16(frame[2:4], uint16(len(packet)))
	copy(frame[4:], packet)
	return frame
}

func mapLookup(values map[string]string) lookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func merge(first, second map[string]string) map[string]string {
	result := make(map[string]string, len(first)+len(second))
	for key, value := range first {
		result[key] = value
	}
	for key, value := range second {
		result[key] = value
	}
	return result
}

func valuesFrom(values map[string]string, key string) string { return values[key] }
