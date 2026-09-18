package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	brokerzerodha "github.com/bibhuyash/tradeedge/internal/adapters/broker/zerodha"
	"github.com/bibhuyash/tradeedge/internal/session"
)

const (
	testAPIKey       = "public-key"
	testAPISecret    = "api-secret-sensitive"
	testRequestToken = "request-token-sensitive"
	testAccessToken  = "access-token-sensitive"
)

type acceptanceClock struct{ now time.Time }

func (c acceptanceClock) Now() time.Time { return c.now }

func TestZerodhaSessionV2Acceptance(t *testing.T) {
	t.Run("new install login once and restart reuse", func(t *testing.T) {
		var exchanges atomic.Int32
		provider := fakeZerodha(t, func(writer http.ResponseWriter, request *http.Request) {
			exchanges.Add(1)
			assertExchangeRequest(t, request)
			writeProvider(writer, http.StatusOK, `{"status":"success","data":{"access_token":"`+testAccessToken+`"}}`)
		})
		defer provider.Close()

		path := filepath.Join(t.TempDir(), "zerodha-session.json")
		clock := acceptanceClock{now: time.Date(2026, 9, 18, 4, 0, 0, 0, time.UTC)}
		config := acceptanceConfig(path, provider.URL, time.Second)
		dependencies := Dependencies{RoundTripper: provider.Client().Transport, Clock: clock}
		application, err := New(context.Background(), config, dependencies)
		if err != nil {
			t.Fatal(err)
		}
		control := httptest.NewServer(application.Handler())
		defer control.Close()

		status := getStatus(t, control.URL)
		if status.State != session.StateLoginRequired || status.Reused {
			t.Fatalf("new-install status = %#v", status)
		}
		response := getBody(t, control.URL+"/api/v1/session/login-url")
		if !strings.Contains(response, "https://kite.zerodha.com/connect/login?") || !strings.Contains(response, "api_key="+url.QueryEscape(testAPIKey)) {
			t.Fatalf("login-url response = %s", response)
		}

		code, body := exchange(t, control.URL, testRequestToken)
		if code != http.StatusOK {
			t.Fatalf("exchange status = %d body=%s", code, body)
		}
		assertSecretSafe(t, body)
		status = decodeStatus(t, body)
		if status.State != session.StateAuthenticated || status.Reused || exchanges.Load() != 1 {
			t.Fatalf("login-once status=%#v exchanges=%d", status, exchanges.Load())
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, []byte(testAccessToken)) || bytes.Contains(raw, []byte(testRequestToken)) {
			t.Fatal("persisted session does not contain only the access session")
		}

		recreated, err := New(context.Background(), config, dependencies)
		if err != nil {
			t.Fatal(err)
		}
		restarted := httptest.NewServer(recreated.Handler())
		defer restarted.Close()
		status = getStatus(t, restarted.URL)
		if status.State != session.StateAuthenticated || !status.Reused || exchanges.Load() != 1 {
			t.Fatalf("restart status=%#v exchanges=%d", status, exchanges.Load())
		}
	})

	t.Run("invalid request token", func(t *testing.T) {
		var exchanges atomic.Int32
		provider := fakeZerodha(t, func(writer http.ResponseWriter, request *http.Request) {
			exchanges.Add(1)
			writeProvider(writer, http.StatusForbidden, `{"status":"error","error_type":"TokenException","message":"invalid `+testAPISecret+` `+testRequestToken+`"}`)
		})
		defer provider.Close()
		path := filepath.Join(t.TempDir(), "zerodha-session.json")
		control := newAcceptanceServer(t, acceptanceConfig(path, provider.URL, time.Second), provider, acceptanceClock{now: time.Now().UTC()})
		defer control.Close()
		code, body := exchange(t, control.URL, testRequestToken)
		if code != http.StatusUnauthorized || decodeStatus(t, body).State != session.StateLoginRequired || exchanges.Load() != 1 {
			t.Fatalf("invalid-token response=%d %s exchanges=%d", code, body, exchanges.Load())
		}
		assertSecretSafe(t, body)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("invalid request persisted session: %v", err)
		}
	})

	for _, testCase := range []struct {
		name    string
		timeout time.Duration
		handler http.HandlerFunc
	}{
		{name: "network timeout", timeout: 20 * time.Millisecond, handler: func(writer http.ResponseWriter, request *http.Request) {
			select {
			case <-request.Context().Done():
			case <-time.After(200 * time.Millisecond):
			}
		}},
		{name: "provider failure", timeout: time.Second, handler: func(writer http.ResponseWriter, _ *http.Request) {
			writeProvider(writer, http.StatusServiceUnavailable, `{"status":"error","error_type":"NetworkException","message":"provider unavailable"}`)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			provider := fakeZerodha(t, testCase.handler)
			defer provider.Close()
			path := filepath.Join(t.TempDir(), "zerodha-session.json")
			control := newAcceptanceServer(t, acceptanceConfig(path, provider.URL, testCase.timeout), provider, acceptanceClock{now: time.Now().UTC()})
			defer control.Close()
			code, body := exchange(t, control.URL, testRequestToken)
			if code != http.StatusServiceUnavailable || decodeStatus(t, body).State != session.StateError {
				t.Fatalf("failure response=%d %s", code, body)
			}
			assertSecretSafe(t, body)
		})
	}

	t.Run("corrupt store fails closed", func(t *testing.T) {
		var exchanges atomic.Int32
		provider := fakeZerodha(t, func(http.ResponseWriter, *http.Request) { exchanges.Add(1) })
		defer provider.Close()
		path := filepath.Join(t.TempDir(), "zerodha-session.json")
		if err := os.WriteFile(path, []byte(`{"schema_version":"broken"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		control := newAcceptanceServer(t, acceptanceConfig(path, provider.URL, time.Second), provider, acceptanceClock{now: time.Now().UTC()})
		defer control.Close()
		if status := getStatus(t, control.URL); status.State != session.StateError {
			t.Fatalf("corrupt-store status = %#v", status)
		}
		code, _ := exchange(t, control.URL, testRequestToken)
		if code != http.StatusServiceUnavailable || exchanges.Load() != 0 {
			t.Fatalf("corrupt-store exchange status=%d calls=%d", code, exchanges.Load())
		}
	})

	t.Run("concurrent identical exchange is single flight", func(t *testing.T) {
		var exchanges atomic.Int32
		release := make(chan struct{})
		provider := fakeZerodha(t, func(writer http.ResponseWriter, _ *http.Request) {
			exchanges.Add(1)
			<-release
			writeProvider(writer, http.StatusOK, `{"status":"success","data":{"access_token":"`+testAccessToken+`"}}`)
		})
		defer provider.Close()
		path := filepath.Join(t.TempDir(), "zerodha-session.json")
		control := newAcceptanceServer(t, acceptanceConfig(path, provider.URL, time.Second), provider, acceptanceClock{now: time.Now().UTC()})
		defer control.Close()

		const callers = 12
		start := make(chan struct{})
		results := make(chan int, callers)
		var group sync.WaitGroup
		for range callers {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				code, _ := exchange(t, control.URL, testRequestToken)
				results <- code
			}()
		}
		close(start)
		deadline := time.Now().Add(time.Second)
		for exchanges.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		close(release)
		group.Wait()
		close(results)
		for code := range results {
			if code != http.StatusOK {
				t.Fatalf("concurrent response status=%d", code)
			}
		}
		if exchanges.Load() != 1 {
			t.Fatalf("provider exchanges=%d, want 1", exchanges.Load())
		}
		if status := getStatus(t, control.URL); status.State != session.StateAuthenticated {
			t.Fatalf("concurrent status=%#v", status)
		}
	})
}

func acceptanceConfig(path, baseURL string, timeout time.Duration) Config {
	return Config{
		HTTPAddress: "127.0.0.1:0", SessionFile: path, ShutdownTimeout: time.Second,
		Zerodha: brokerzerodha.Config{Enabled: true, BaseURL: baseURL, Timeout: timeout, MaxConcurrency: 1, MappingMaxAge: time.Hour},
		apiKey:  testAPIKey, apiSecret: testAPISecret,
	}
}

func newAcceptanceServer(t *testing.T, config Config, provider *httptest.Server, clock acceptanceClock) *httptest.Server {
	t.Helper()
	application, err := New(context.Background(), config, Dependencies{RoundTripper: provider.Client().Transport, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(application.Handler())
}

func fakeZerodha(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(handler)
}

func assertExchangeRequest(t *testing.T, request *http.Request) {
	t.Helper()
	if request.Method != http.MethodPost || request.URL.Path != "/session/token" {
		t.Fatalf("provider request = %s %s", request.Method, request.URL.Path)
	}
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}
	if request.Form.Get("api_key") != testAPIKey || request.Form.Get("request_token") != testRequestToken || request.Form.Get("checksum") == "" {
		t.Fatal("provider exchange form is incomplete")
	}
}

func writeProvider(writer http.ResponseWriter, status int, body string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, body)
}

func getStatus(t *testing.T, baseURL string) session.Status {
	t.Helper()
	return decodeStatus(t, getBody(t, baseURL+"/api/v1/session/status"))
}

func getBody(t *testing.T, target string) string {
	t.Helper()
	response, err := http.Get(target) //nolint:gosec -- test server URL
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func exchange(t *testing.T, baseURL, token string) (int, string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"request_token": token})
	response, err := http.Post(baseURL+"/api/v1/session/exchange", "application/json", bytes.NewReader(payload)) //nolint:gosec -- test server URL
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(body)
}

func decodeStatus(t *testing.T, body string) session.Status {
	t.Helper()
	var status session.Status
	if err := json.Unmarshal([]byte(body), &status); err != nil {
		t.Fatalf("decode status %q: %v", body, err)
	}
	return status
}

func assertSecretSafe(t *testing.T, output string) {
	t.Helper()
	lower := strings.ToLower(output)
	for _, forbidden := range []string{testAPISecret, testRequestToken, testAccessToken, "authorization", "checksum"} {
		if strings.Contains(lower, strings.ToLower(forbidden)) {
			t.Fatalf("sensitive output contains %q: %s", forbidden, output)
		}
	}
}
