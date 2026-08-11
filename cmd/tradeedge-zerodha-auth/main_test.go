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
	"github.com/bibhuyash/tradeedge/internal/marketdata"
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
	for _, line := range []string{"AUTHENTICATION=PASS", "REST_AUTH=PASS", "WEBSOCKET_AUTH=PASS", "OBSERVATIONS_RECEIVED=2", "TEXT_MESSAGES_RECEIVED=0", "BINARY_FRAMES_RECEIVED=2", "INDEX_PACKETS_RECEIVED=2", "TOKEN_MATCHES=2", "FRESH_OBSERVATIONS=2", "LAST_FAILURE_STAGE=NONE", "ACCESS_TOKEN_EXPIRES_AT=2026-08-11T00:30:00Z"} {
		if !strings.Contains(output.String(), line+"\n") {
			t.Fatalf("missing %q in output %q", line, output.String())
		}
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

func TestPreflightIgnoresTimestampLessIndexQuoteUntilFreshFullPackets(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	connection := &fakeMarketConnection{frames: []brokerzerodha.MarketFrame{
		{Binary: true, Data: indexQuoteFrame(256265)},
		{Binary: true, Data: quoteFrame(256265, now)},
		{Binary: true, Data: quoteFrame(260105, now)},
	}}
	dependencies := websocketDependencies(now, &fakeMarketDialer{connection: connection})
	dependencies.roundTripper = preflightRoundTripper(t)

	var output bytes.Buffer
	if err := run([]string{"preflight", "-runtime-bundle", "pinned.json", "-timeout", "1s", "-max-age", "5s"}, mapLookup(credentialValues()), &output, dependencies); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "WEBSOCKET_AUTH=PASS\n") || !strings.Contains(output.String(), "INDEX_PACKETS_RECEIVED=3\n") || !strings.Contains(output.String(), "FRESH_OBSERVATIONS=2\n") {
		t.Fatalf("output=%q", output.String())
	}
}

func TestPreflightReportsTimestampLessIndexPacketAtFreshnessStage(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	connection := &fakeMarketConnection{frames: []brokerzerodha.MarketFrame{{Binary: true, Data: indexQuoteFrame(256265)}}}
	dependencies := websocketDependencies(now, &fakeMarketDialer{connection: connection})
	dependencies.roundTripper = preflightRoundTripper(t)

	var output bytes.Buffer
	err := run([]string{"preflight", "-runtime-bundle", "pinned.json", "-timeout", "20ms", "-max-age", "5s"}, mapLookup(credentialValues()), &output, dependencies)
	if err == nil {
		t.Fatal("expected preflight failure")
	}
	for _, line := range []string{
		"BINARY_FRAMES_RECEIVED=1", "PACKETS_RECEIVED=1", "INDEX_PACKETS_RECEIVED=1",
		"PACKETS_DECODED=1", "PACKETS_REJECTED=0", "TOKEN_MATCHES=1",
		"FRESH_OBSERVATIONS=0", "LAST_FAILURE_STAGE=FRESHNESS",
	} {
		if !strings.Contains(output.String(), line+"\n") {
			t.Fatalf("missing %q in output %q", line, output.String())
		}
	}
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
	want := "AUTHENTICATION=PASS\nREST_AUTH=PASS\nWEBSOCKET_AUTH=FAIL\nOBSERVATIONS_RECEIVED=0\nSHUTDOWN=PASS\nERROR_TYPE=WebSocketTimeout\nMESSAGE=Timed out waiting for fresh market observations\nHTTP_STATUS=0\n" +
		"WEBSOCKET_HANDSHAKE=PASS\nSUBSCRIBE_SENT=PASS\nEXPECTED_TOKEN_COUNT=2\nEXPECTED_TOKENS_VALID=PASS\nTEXT_MESSAGES_RECEIVED=0\nBROKER_MESSAGES_RECEIVED=0\nINSTRUMENTS_META_RECEIVED=0\nAPP_CODE_RECEIVED=0\nORDER_UPDATES_RECEIVED=0\nPROVIDER_ERRORS_RECEIVED=0\nBINARY_FRAMES_RECEIVED=0\nHEARTBEATS_RECEIVED=0\nPACKETS_RECEIVED=0\nINDEX_PACKETS_RECEIVED=0\nPACKETS_DECODED=0\nPACKETS_REJECTED=0\nTOKEN_MATCHES=0\nFRESH_OBSERVATIONS=0\nLAST_FAILURE_STAGE=FRAME_RECEIVE\n"
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
	want := "AUTHENTICATION=PASS\nREST_AUTH=PASS\nWEBSOCKET_AUTH=FAIL\nOBSERVATIONS_RECEIVED=0\nSHUTDOWN=PASS\nERROR_TYPE=WebSocketProtocolError\nMESSAGE=Received a malformed market-data frame\nHTTP_STATUS=0\n" +
		"WEBSOCKET_HANDSHAKE=PASS\nSUBSCRIBE_SENT=PASS\nEXPECTED_TOKEN_COUNT=2\nEXPECTED_TOKENS_VALID=PASS\nTEXT_MESSAGES_RECEIVED=0\nBROKER_MESSAGES_RECEIVED=0\nINSTRUMENTS_META_RECEIVED=0\nAPP_CODE_RECEIVED=0\nORDER_UPDATES_RECEIVED=0\nPROVIDER_ERRORS_RECEIVED=0\nBINARY_FRAMES_RECEIVED=1\nHEARTBEATS_RECEIVED=0\nPACKETS_RECEIVED=1\nINDEX_PACKETS_RECEIVED=0\nPACKETS_DECODED=0\nPACKETS_REJECTED=1\nTOKEN_MATCHES=0\nFRESH_OBSERVATIONS=0\nLAST_FAILURE_STAGE=PACKET_DECODE\n" +
		"FRAME_SEQUENCE=1\nFRAME_MESSAGE_TYPE=BINARY\nFRAME_LENGTH=3\nFRAME_CLASSIFICATION=MARKET_DATA\n"
	if output.String() != want || errorOutput.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
	assertNoCredentialMaterial(t, output.String()+errorOutput.String(), merge(values, map[string]string{accessTokenEnvironment: "generated-access-token"}), "")
}

func TestPreflightIdentifiesUnknownTextBeforeBinaryDecode(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	payload := []byte(`{"type":"future","data":{}}`)
	connection := &fakeMarketConnection{frames: []brokerzerodha.MarketFrame{{MessageType: brokerzerodha.MarketMessageText, Data: payload}}}
	dependencies := websocketDependencies(now, &fakeMarketDialer{connection: connection})
	dependencies.roundTripper = preflightRoundTripper(t)

	var output bytes.Buffer
	err := run([]string{"preflight", "-runtime-bundle", "pinned.json", "-timeout", "1s"}, mapLookup(credentialValues()), &output, dependencies)
	if err == nil {
		t.Fatal("expected preflight failure")
	}
	for _, line := range []string{
		"ERROR_TYPE=WebSocketMessageTypeError", "MESSAGE=Received an unknown WebSocket text message type", "BINARY_FRAMES_RECEIVED=0", "PACKETS_REJECTED=0",
		"LAST_FAILURE_STAGE=MESSAGE_TYPE", "FRAME_SEQUENCE=1", "FRAME_MESSAGE_TYPE=TEXT",
		fmt.Sprintf("FRAME_LENGTH=%d", len(payload)), "FRAME_CLASSIFICATION=UNKNOWN", "TEXT_MESSAGE_TYPE=unknown",
	} {
		if !strings.Contains(output.String(), line+"\n") {
			t.Fatalf("missing %q in output %q", line, output.String())
		}
	}
	if strings.Contains(output.String(), string(payload)) {
		t.Fatal("frame payload appeared in diagnostics")
	}
}

func TestPreflightContinuesAfterObservedInstrumentMetadata(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	metadata := []byte(`{"type":"instruments_meta","data":{"count":90517,"etag":"12345678901234567890123456"}}`)
	appCode := []byte(`{"type":"app_code","timestamp":"2023-05-30T13:31:46+05:30"}`)
	connection := &fakeMarketConnection{frames: []brokerzerodha.MarketFrame{
		{MessageType: brokerzerodha.MarketMessageText, Data: metadata},
		{MessageType: brokerzerodha.MarketMessageText, Data: appCode},
		{Binary: true, Data: []byte{0}},
		{Binary: true, Data: quoteFrame(256265, now)},
		{Binary: true, Data: quoteFrame(260105, now)},
	}}
	dependencies := websocketDependencies(now, &fakeMarketDialer{connection: connection})
	dependencies.roundTripper = preflightRoundTripper(t)

	var output bytes.Buffer
	if err := run([]string{"preflight", "-runtime-bundle", "pinned.json", "-timeout", "1s"}, mapLookup(credentialValues()), &output, dependencies); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"WEBSOCKET_AUTH=PASS", "OBSERVATIONS_RECEIVED=2", "TEXT_MESSAGES_RECEIVED=2",
		"INSTRUMENTS_META_RECEIVED=1", "APP_CODE_RECEIVED=1", "BINARY_FRAMES_RECEIVED=3", "HEARTBEATS_RECEIVED=1",
		"INDEX_PACKETS_RECEIVED=2", "TOKEN_MATCHES=2", "FRESH_OBSERVATIONS=2",
		"FRAME_LENGTH=86", "FRAME_CLASSIFICATION=INSTRUMENTS_META", "TEXT_MESSAGE_TYPE=instruments_meta",
		"FRAME_LENGTH=59", "FRAME_CLASSIFICATION=APP_CODE", "TEXT_MESSAGE_TYPE=app_code",
	} {
		if !strings.Contains(output.String(), line+"\n") {
			t.Fatalf("missing %q in output %q", line, output.String())
		}
	}
	if strings.Contains(output.String(), "90517") || strings.Contains(output.String(), "12345678901234567890123456") || strings.Contains(output.String(), "2023-05-30") {
		t.Fatal("startup metadata payload appeared in diagnostics")
	}
}

func TestPreflightContinuesAcrossDocumentedTextMessages(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	sensitiveOrderID := "sensitive-order-id"
	connection := &fakeMarketConnection{frames: []brokerzerodha.MarketFrame{
		{MessageType: brokerzerodha.MarketMessageText, Data: []byte(`{"type":"message","data":"market alert"}`)},
		{MessageType: brokerzerodha.MarketMessageText, Data: []byte(`{"type":"message","data":"second alert"}`)},
		{MessageType: brokerzerodha.MarketMessageText, Data: []byte(`{"type":"order","data":{"order_id":"` + sensitiveOrderID + `"}}`)},
		{Binary: true, Data: []byte{0}},
		{Binary: true, Data: indexQuoteFrame(256265)},
		{Binary: true, Data: quoteFrame(256265, now)},
		{Binary: true, Data: quoteFrame(260105, now)},
	}}
	dependencies := websocketDependencies(now, &fakeMarketDialer{connection: connection})
	dependencies.roundTripper = preflightRoundTripper(t)

	var output bytes.Buffer
	if err := run([]string{"preflight", "-runtime-bundle", "pinned.json", "-timeout", "1s"}, mapLookup(credentialValues()), &output, dependencies); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"WEBSOCKET_AUTH=PASS", "OBSERVATIONS_RECEIVED=2", "TEXT_MESSAGES_RECEIVED=3",
		"BROKER_MESSAGES_RECEIVED=2", "ORDER_UPDATES_RECEIVED=1", "PROVIDER_ERRORS_RECEIVED=0",
		"BINARY_FRAMES_RECEIVED=4", "HEARTBEATS_RECEIVED=1", "INDEX_PACKETS_RECEIVED=3",
		"TOKEN_MATCHES=2", "FRESH_OBSERVATIONS=2", "LAST_FAILURE_STAGE=NONE",
		"FRAME_CLASSIFICATION=BROKER_MESSAGE", "TEXT_MESSAGE_TYPE=message", "FRAME_CLASSIFICATION=ORDER_UPDATE", "TEXT_MESSAGE_TYPE=order",
	} {
		if !strings.Contains(output.String(), line+"\n") {
			t.Fatalf("missing %q in output %q", line, output.String())
		}
	}
	if strings.Contains(output.String(), sensitiveOrderID) || strings.Contains(output.String(), "market alert") {
		t.Fatal("text payload appeared in diagnostics")
	}
}

func TestPreflightFailsSafelyOnProviderTextError(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	providerDetail := "sensitive provider detail"
	connection := &fakeMarketConnection{frames: []brokerzerodha.MarketFrame{{MessageType: brokerzerodha.MarketMessageText, Data: []byte(`{"type":"error","data":"` + providerDetail + `"}`)}}}
	dependencies := websocketDependencies(now, &fakeMarketDialer{connection: connection})
	dependencies.roundTripper = preflightRoundTripper(t)

	var output bytes.Buffer
	err := run([]string{"preflight", "-runtime-bundle", "pinned.json", "-timeout", "1s"}, mapLookup(credentialValues()), &output, dependencies)
	if err == nil {
		t.Fatal("expected provider error")
	}
	for _, line := range []string{"ERROR_TYPE=WebSocketProviderError", "MESSAGE=Zerodha reported a WebSocket provider error", "TEXT_MESSAGES_RECEIVED=1", "PROVIDER_ERRORS_RECEIVED=1", "FRAME_CLASSIFICATION=PROVIDER_ERROR", "TEXT_MESSAGE_TYPE=error"} {
		if !strings.Contains(output.String(), line+"\n") {
			t.Fatalf("missing %q in output %q", line, output.String())
		}
	}
	if strings.Contains(output.String(), providerDetail) {
		t.Fatal("provider payload appeared in diagnostics")
	}
}

func TestPreflightFailsClosedOnMalformedText(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	payload := []byte(`{"type":`)
	connection := &fakeMarketConnection{frames: []brokerzerodha.MarketFrame{{MessageType: brokerzerodha.MarketMessageText, Data: payload}}}
	dependencies := websocketDependencies(now, &fakeMarketDialer{connection: connection})
	dependencies.roundTripper = preflightRoundTripper(t)

	var output bytes.Buffer
	err := run([]string{"preflight", "-runtime-bundle", "pinned.json", "-timeout", "1s"}, mapLookup(credentialValues()), &output, dependencies)
	if err == nil {
		t.Fatal("expected malformed text failure")
	}
	for _, line := range []string{"ERROR_TYPE=WebSocketTextProtocolError", "FRAME_CLASSIFICATION=MALFORMED_TEXT", "TEXT_MESSAGE_TYPE=unknown"} {
		if !strings.Contains(output.String(), line+"\n") {
			t.Fatalf("missing %q in output %q", line, output.String())
		}
	}
	if strings.Contains(output.String(), string(payload)) {
		t.Fatal("malformed payload appeared in diagnostics")
	}
}

func TestPreflightCountsOneByteHeartbeatBeforeValidation(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	connection := &fakeMarketConnection{frames: []brokerzerodha.MarketFrame{{Binary: true, Data: []byte{0}}}}
	dependencies := websocketDependencies(now, &fakeMarketDialer{connection: connection})
	dependencies.roundTripper = preflightRoundTripper(t)

	var output bytes.Buffer
	err := run([]string{"preflight", "-runtime-bundle", "pinned.json", "-timeout", "20ms"}, mapLookup(credentialValues()), &output, dependencies)
	if err == nil {
		t.Fatal("expected preflight timeout")
	}
	for _, line := range []string{
		"BINARY_FRAMES_RECEIVED=1", "HEARTBEATS_RECEIVED=1", "PACKETS_RECEIVED=0",
		"PACKETS_REJECTED=0", "FRAME_MESSAGE_TYPE=BINARY", "FRAME_LENGTH=1",
		"FRAME_CLASSIFICATION=HEARTBEAT", "LAST_FAILURE_STAGE=FRAME_RECEIVE",
	} {
		if !strings.Contains(output.String(), line+"\n") {
			t.Fatalf("missing %q in output %q", line, output.String())
		}
	}
}

func TestFreshObservationEnforcesFiveSecondPolicy(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 5, 0, time.UTC)
	base := marketdata.Observation{ExchangeTime: now.Add(-5 * time.Second), IngestedAt: now.Add(-time.Second)}
	if !freshObservation(base, now, 5*time.Second) {
		t.Fatal("five-second boundary should be fresh")
	}
	base.ExchangeTime = now.Add(-5*time.Second - time.Nanosecond)
	if freshObservation(base, now, 5*time.Second) {
		t.Fatal("observation older than five seconds was accepted")
	}
	base.ExchangeTime = time.Time{}
	if freshObservation(base, now, 5*time.Second) {
		t.Fatal("timestamp-less observation was accepted")
	}
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

func indexQuoteFrame(token uint32) []byte {
	packet := make([]byte, 28)
	binary.BigEndian.PutUint32(packet[0:4], token)
	binary.BigEndian.PutUint32(packet[4:8], 10000)
	binary.BigEndian.PutUint32(packet[8:12], 10100)
	binary.BigEndian.PutUint32(packet[12:16], 9900)
	binary.BigEndian.PutUint32(packet[16:20], 9950)
	binary.BigEndian.PutUint32(packet[20:24], 9980)
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
