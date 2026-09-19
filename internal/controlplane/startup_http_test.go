package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	operatorstartup "github.com/bibhuyash/tradeedge/internal/operator/startup"
	"github.com/bibhuyash/tradeedge/internal/session"
)

type httpSession struct{}

func (httpSession) Status(context.Context) (session.Status, error) {
	return session.Status{Provider: session.ProviderZerodha, State: session.StateAuthenticated}, nil
}
func (httpSession) LoginURL(context.Context) (string, error) {
	return "https://kite.example/login", nil
}
func (httpSession) Exchange(context.Context, string) (session.Status, error) {
	return session.Status{Provider: session.ProviderZerodha, State: session.StateAuthenticated}, nil
}

type httpStartup struct {
	mu     sync.Mutex
	status operatorstartup.Status
	starts int
}

func (s *httpStartup) Status(context.Context) operatorstartup.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}
func (s *httpStartup) Start(context.Context) operatorstartup.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts++
	s.status = operatorstartup.Status{State: operatorstartup.Ready}
	return s.status
}

func TestStartupGetAndPost(t *testing.T) {
	startup := &httpStartup{status: operatorstartup.Status{State: operatorstartup.Authenticated}}
	server := httptest.NewServer(NewHandler(httpSession{}, startup))
	defer server.Close()
	response, err := http.Get(server.URL + "/api/v1/operator/startup")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET=%d", response.StatusCode)
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/operator/startup", nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("POST=%d", response.StatusCode)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		startup.mu.Lock()
		calls := startup.starts
		startup.mu.Unlock()
		if calls == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("startup not triggered")
}

func TestStartupResponseDoesNotExposeSecrets(t *testing.T) {
	startup := &httpStartup{status: operatorstartup.Status{State: operatorstartup.Failed, Reason: "startup unavailable"}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/operator/startup", nil)
	response := httptest.NewRecorder()
	NewHandler(httpSession{}, startup).ServeHTTP(response, request)
	body := response.Body.String()
	for _, secret := range []string{"request_token", "access_token", "api_secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response exposed %s", secret)
		}
	}
}
