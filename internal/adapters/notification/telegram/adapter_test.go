package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bibhuyash/tradeedge/internal/notification"
)

func TestSendAndRateLimitAreSanitized(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !strings.HasSuffix(r.URL.Path, "/botsuper-secret-token/sendMessage") {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"parameters":{"retry_after":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":7}}`))
	}))
	defer server.Close()
	adapter, err := New(Config{Enabled: true, Token: "super-secret-token", ChatID: "sensitive-chat", BaseURL: server.URL}, server.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Send(context.Background(), notification.RenderedMessage{Text: "hello"})
	var retry *notification.RetryableError
	if !errors.As(err, &retry) || retry.Class != "RATE_LIMITED" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
	receipt, err := adapter.Send(context.Background(), notification.RenderedMessage{Text: "hello"})
	if err != nil || receipt.ProviderMessageID != "7" {
		t.Fatalf("send failed: %+v %v", receipt, err)
	}
	raw := adapter.Status()
	if strings.Contains(raw.FailureClass, "secret") {
		t.Fatal("status leaked secret")
	}
}
func TestConfigurationNeverEchoesSecrets(t *testing.T) {
	secret := "bad/token"
	_, err := New(Config{Enabled: true, Token: secret, ChatID: "chat"}, nil, nil)
	if !errors.Is(err, ErrInvalidConfiguration) || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe validation error: %v", err)
	}
}
