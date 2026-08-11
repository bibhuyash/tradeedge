package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
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
	values := map[string]string{apiKeyEnvironment: "public-key", apiSecretEnvironment: "never-print-secret"}
	var output bytes.Buffer
	if err := run([]string{"login-url"}, mapLookup(values), &output, commandDependencies{}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(output.String()); got != "https://kite.zerodha.com/connect/login?api_key=public-key&v=3" {
		t.Fatalf("login URL = %q", got)
	}
	if strings.Contains(output.String(), values[apiSecretEnvironment]) {
		t.Fatal("API secret appeared in output")
	}
}

func TestExchangeTokenRejectsMissingAPIKeyAndSecret(t *testing.T) {
	base := map[string]string{readOnlyEnvironment: "true", requestTokenEnvironment: "request"}
	for name, testCase := range map[string]struct {
		values  map[string]string
		message string
	}{
		"missing api key":    {values: base, message: "Missing required environment variable: " + apiKeyEnvironment},
		"missing api secret": {values: merge(base, map[string]string{apiKeyEnvironment: "key"}), message: "Missing required environment variable: " + apiSecretEnvironment},
	} {
		t.Run(name, func(t *testing.T) {
			var output, errorOutput bytes.Buffer
			if exitCode := execute([]string{"exchange-token"}, mapLookup(testCase.values), &output, &errorOutput, commandDependencies{}); exitCode != 1 {
				t.Fatalf("execute() = %d", exitCode)
			}
			want := "AUTHENTICATION=FAIL\nERROR_TYPE=ConfigurationError\nMESSAGE=" + testCase.message + "\nHTTP_STATUS=0\n"
			if output.String() != want || errorOutput.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q", output.String(), errorOutput.String())
			}
		})
	}
}

func TestExchangeTokenReportsMissingReadOnlyOptIn(t *testing.T) {
	values := credentialValues()
	delete(values, readOnlyEnvironment)
	var output, errorOutput bytes.Buffer
	if exitCode := execute([]string{"exchange-token"}, mapLookup(values), &output, &errorOutput, commandDependencies{}); exitCode != 1 {
		t.Fatalf("execute() = %d", exitCode)
	}
	want := "AUTHENTICATION=FAIL\nERROR_TYPE=ConfigurationError\nMESSAGE=TRADEEDGE_ZERODHA_READ_ONLY must be true\nHTTP_STATUS=0\n"
	if output.String() != want || errorOutput.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
	assertNoCredentialMaterial(t, output.String()+errorOutput.String(), values, "")
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
	values[apiSecretEnvironment] = "recognizable-secret"
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
	for _, secret := range []string{values[apiSecretEnvironment], values[requestTokenEnvironment]} {
		if strings.Contains(combined, secret) {
			t.Fatalf("secret appeared in result: %q", combined)
		}
	}
}

func TestExchangeTokenSurfacesInvalidChecksumSafely(t *testing.T) {
	values := credentialValues()
	var submittedChecksum string
	dependencies := commandDependencies{roundTripper: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		submittedChecksum = request.Form.Get("checksum")
		body := fmt.Sprintf(`{"status":"error","message":"Invalid checksum %s for api_key %s using api_secret %s and request_token %s","error_type":"TokenException"}`,
			submittedChecksum, values[apiKeyEnvironment], values[apiSecretEnvironment], values[requestTokenEnvironment])
		return response(http.StatusForbidden, body), nil
	})}
	var output, errorOutput bytes.Buffer
	if exitCode := execute([]string{"exchange-token"}, mapLookup(values), &output, &errorOutput, dependencies); exitCode != 1 {
		t.Fatalf("execute() = %d", exitCode)
	}
	want := "AUTHENTICATION=FAIL\nERROR_TYPE=TokenException\nMESSAGE=Invalid checksum\nHTTP_STATUS=403\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("stderr = %q", errorOutput.String())
	}
	assertNoCredentialMaterial(t, output.String()+errorOutput.String(), values, submittedChecksum)
}

func TestExchangeTokenSurfacesExpiredTokenSafely(t *testing.T) {
	values := credentialValues()
	dependencies := commandDependencies{roundTripper: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusForbidden, `{"status":"error","message":"Token is invalid or has expired.","error_type":"TokenException"}`), nil
	})}
	var output bytes.Buffer
	err := run([]string{"exchange-token"}, mapLookup(values), &output, dependencies)
	if !errors.Is(err, errAuthentication) || !errors.Is(err, errDiagnosticReported) {
		t.Fatalf("run() error = %v", err)
	}
	want := "AUTHENTICATION=FAIL\nERROR_TYPE=TokenException\nMESSAGE=Token is invalid or expired\nHTTP_STATUS=403\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	assertNoCredentialMaterial(t, output.String()+err.Error(), values, "")
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

func TestPreflightReusesOneAuthenticatedSession(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	values := credentialValues()
	connection := &fakeMarketConnection{frames: []brokerzerodha.MarketFrame{
		{Binary: true, Data: quoteFrame(256265, now)},
		{Binary: true, Data: quoteFrame(260105, now)},
	}}
	dialer := &fakeMarketDialer{connection: connection}
	exchanges, profiles := 0, 0
	dependencies := websocketDependencies(now, dialer)
	dependencies.roundTripper = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/session/token":
			exchanges++
			return response(http.StatusOK, `{"status":"success","data":{"access_token":"generated-access-token"}}`), nil
		case request.Method == http.MethodGet && request.URL.Path == "/user/profile":
			profiles++
			if got := request.Header.Get("Authorization"); got != "token public-key:generated-access-token" {
				t.Fatalf("REST did not use exchanged session")
			}
			return response(http.StatusOK, `{"status":"success","data":{"exchanges":["NSE"],"products":["CNC"],"order_types":["LIMIT"]}}`), nil
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
			return nil, errors.New("unexpected request")
		}
	})

	var output bytes.Buffer
	err := run([]string{"preflight", "-runtime-bundle", "pinned.json", "-timeout", "1s"}, mapLookup(values), &output, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	want := "AUTHENTICATION=PASS\nREST_AUTH=PASS\nWEBSOCKET_AUTH=PASS\nOBSERVATIONS_RECEIVED=2\nSHUTDOWN=PASS\nACCESS_TOKEN_EXPIRES_AT=2026-08-11T00:30:00Z\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if exchanges != 1 || profiles != 1 {
		t.Fatalf("token exchanges=%d profile calls=%d", exchanges, profiles)
	}
	if !strings.Contains(dialer.endpoint, "api_key=public-key") || !strings.Contains(dialer.endpoint, "access_token=generated-access-token") {
		t.Fatal("WebSocket did not use the exchanged session")
	}
	connection.mu.Lock()
	writes, closed := connection.writes, connection.closed
	connection.mu.Unlock()
	if writes != 2 || !closed {
		t.Fatalf("subscription writes=%d closed=%t", writes, closed)
	}
	assertNoCredentialMaterial(t, output.String(), merge(values, map[string]string{accessTokenEnvironment: "generated-access-token"}), "")
}

func TestPreflightSurfacesAuthenticationFailureSafely(t *testing.T) {
	values := credentialValues()
	values[apiSecretEnvironment] = "recognizable-secret"
	values[requestTokenEnvironment] = "recognizable-request-token"
	dependencies := websocketDependencies(time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC), &fakeMarketDialer{connection: &fakeMarketConnection{}})
	dependencies.roundTripper = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusForbidden, `{"status":"error","message":"Invalid checksum recognizable-secret recognizable-request-token","error_type":"TokenException"}`), nil
	})

	var output, errorOutput bytes.Buffer
	exitCode := execute([]string{"preflight", "-runtime-bundle", "pinned.json", "-timeout", "1s"}, mapLookup(values), &output, &errorOutput, dependencies)
	want := "AUTHENTICATION=FAIL\nERROR_TYPE=TokenException\nMESSAGE=Invalid checksum\nHTTP_STATUS=403\n"
	if exitCode != 1 || output.String() != want || errorOutput.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, output.String(), errorOutput.String())
	}
	assertNoCredentialMaterial(t, output.String()+errorOutput.String(), values, "")
}

func TestPreflightValidatesBundleBeforeExchange(t *testing.T) {
	exchanges := 0
	dependencies := commandDependencies{
		loadTokens: func(string) ([]string, error) { return nil, errInvalidConfiguration },
		roundTripper: roundTripFunc(func(*http.Request) (*http.Response, error) {
			exchanges++
			return nil, errors.New("must not exchange")
		}),
	}
	var output bytes.Buffer
	err := run([]string{"preflight", "-runtime-bundle", "invalid.json"}, mapLookup(credentialValues()), &output, dependencies)
	if err == nil || exchanges != 0 {
		t.Fatalf("error=%v exchanges=%d", err, exchanges)
	}
	want := "AUTHENTICATION=FAIL\nERROR_TYPE=ConfigurationError\nMESSAGE=Invalid checksum-pinned runtime bundle\nHTTP_STATUS=0\n"
	if output.String() != want {
		t.Fatalf("output=%q", output.String())
	}
}

func TestPreflightContainsRESTFailureAndDoesNotOpenWebSocket(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	values := credentialValues()
	dialer := &fakeMarketDialer{connection: &fakeMarketConnection{}}
	exchanges := 0
	dependencies := websocketDependencies(now, dialer)
	dependencies.roundTripper = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/session/token" {
			exchanges++
			return response(http.StatusOK, `{"status":"success","data":{"access_token":"generated-access-token"}}`), nil
		}
		return response(http.StatusForbidden, `{"status":"error","message":"recognizable-request-token","error_type":"TokenException"}`), nil
	})

	var output, errorOutput bytes.Buffer
	exitCode := execute([]string{"preflight", "-runtime-bundle", "pinned.json", "-timeout", "1s"}, mapLookup(values), &output, &errorOutput, dependencies)
	if exitCode != 1 || exchanges != 1 || dialer.endpoint != "" {
		t.Fatalf("exit=%d exchanges=%d websocket=%q", exitCode, exchanges, dialer.endpoint)
	}
	want := "AUTHENTICATION=PASS\nREST_AUTH=FAIL\nERROR_TYPE=RESTVerificationError\nMESSAGE=Read-only REST verification failed\nHTTP_STATUS=0\nSHUTDOWN=PASS\n"
	if output.String() != want || errorOutput.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
	assertNoCredentialMaterial(t, output.String()+errorOutput.String(), merge(values, map[string]string{accessTokenEnvironment: "generated-access-token"}), "")
}

func TestPreflightContainsWebSocketTimeoutAndShutsDown(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	values := credentialValues()
	connection := &fakeMarketConnection{}
	dependencies := websocketDependencies(now, &fakeMarketDialer{connection: connection})
	dependencies.roundTripper = preflightRoundTripper(t)

	var output, errorOutput bytes.Buffer
	exitCode := execute([]string{"preflight", "-runtime-bundle", "pinned.json", "-timeout", "20ms"}, mapLookup(values), &output, &errorOutput, dependencies)
	if exitCode != 1 {
		t.Fatalf("execute() = %d", exitCode)
	}
	want := "AUTHENTICATION=PASS\nREST_AUTH=PASS\nWEBSOCKET_AUTH=FAIL\nOBSERVATIONS_RECEIVED=0\nSHUTDOWN=PASS\nERROR_TYPE=WebSocketTimeout\nMESSAGE=Timed out waiting for fresh market observations\nHTTP_STATUS=0\n"
	if output.String() != want || errorOutput.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
	connection.mu.Lock()
	closed := connection.closed
	connection.mu.Unlock()
	if !closed {
		t.Fatal("WebSocket was not closed")
	}
	assertNoCredentialMaterial(t, output.String()+errorOutput.String(), merge(values, map[string]string{accessTokenEnvironment: "generated-access-token"}), "")
}

func TestPreflightReportsMalformedMarketFrameSafely(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	values := credentialValues()
	connection := &fakeMarketConnection{frames: []brokerzerodha.MarketFrame{{Binary: true, Data: []byte{0, 1, 0}}}}
	dependencies := websocketDependencies(now, &fakeMarketDialer{connection: connection})
	dependencies.roundTripper = preflightRoundTripper(t)

	var output, errorOutput bytes.Buffer
	exitCode := execute([]string{"preflight", "-runtime-bundle", "pinned.json", "-timeout", "1s"}, mapLookup(values), &output, &errorOutput, dependencies)
	if exitCode != 1 {
		t.Fatalf("execute() = %d", exitCode)
	}
	want := "AUTHENTICATION=PASS\nREST_AUTH=PASS\nWEBSOCKET_AUTH=FAIL\nOBSERVATIONS_RECEIVED=0\nSHUTDOWN=PASS\nERROR_TYPE=WebSocketProtocolError\nMESSAGE=Received a malformed market-data frame\nHTTP_STATUS=0\n"
	if output.String() != want || errorOutput.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
	assertNoCredentialMaterial(t, output.String()+errorOutput.String(), merge(values, map[string]string{accessTokenEnvironment: "generated-access-token"}), "")
}

func TestCommandSurfaceHasNoMutationCapability(t *testing.T) {
	want := []string{"login-url", "exchange-token", "verify-rest", "verify-websocket", "preflight"}
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
		readOnlyEnvironment:     "true",
		apiKeyEnvironment:       "public-key",
		apiSecretEnvironment:    "api-secret",
		requestTokenEnvironment: "request-token",
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

func preflightRoundTripper(t *testing.T) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/session/token":
			return response(http.StatusOK, `{"status":"success","data":{"access_token":"generated-access-token"}}`), nil
		case "/user/profile":
			return response(http.StatusOK, `{"status":"success","data":{"exchanges":["NSE"],"products":["CNC"],"order_types":["LIMIT"]}}`), nil
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
			return nil, errors.New("unexpected request")
		}
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

func assertNoCredentialMaterial(t *testing.T, output string, values map[string]string, checksum string) {
	t.Helper()
	for _, value := range []string{values[apiKeyEnvironment], values[apiSecretEnvironment], values[requestTokenEnvironment], values[accessTokenEnvironment], checksum} {
		if value != "" && strings.Contains(output, value) {
			t.Fatalf("credential material appeared in output: %q", output)
		}
	}
	if strings.Contains(strings.ToLower(output), "authorization: token") {
		t.Fatalf("authorization header appeared in output: %q", output)
	}
}
