package opshttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bibhuyash/tradeedge/internal/notification"
	"github.com/bibhuyash/tradeedge/internal/operations/cas"
	"github.com/bibhuyash/tradeedge/internal/operations/reporting"
)

type source struct{}

func (source) Health() notification.Health {
	return notification.Health{State: "READY", QueueCapacity: 512}
}
func (source) ProviderStatus() notification.ProviderStatus {
	return notification.ProviderStatus{Provider: "telegram", State: "READY", FailureClass: "TRANSPORT"}
}
func TestAPIsAreBoundedGETOnlyAndRedacted(t *testing.T) {
	store, _ := notification.NewStore(2, 2)
	casStore, _ := cas.NewStore(2)
	reports, _ := reporting.NewAccumulator(2)
	handler := New(Dependencies{Notifications: source{}, Store: store, CAS: casStore, Reports: reports})
	for _, path := range []string{"/api/v1/notifications/health", "/api/v1/notifications/events?limit=999", "/api/v1/notifications/failures", "/api/v1/notifications/queue", "/api/v1/notifications/providers/telegram", "/api/v1/operations/cas-evidence?limit=999", "/api/v1/operations/eod/latest"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK && response.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, response.Code)
		}
		if strings.Contains(response.Body.String(), "bot-token") || strings.Contains(response.Body.String(), "chat-id") {
			t.Fatal("secret leaked")
		}
		post := httptest.NewRequest(http.MethodPost, path, nil)
		postResponse := httptest.NewRecorder()
		handler.ServeHTTP(postResponse, post)
		if postResponse.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s accepted mutation", path)
		}
	}
}
