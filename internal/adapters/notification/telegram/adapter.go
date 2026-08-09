// Package telegram implements outbound-only Telegram presentation. It exposes
// no polling, webhook, command, or trading-control capability.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/notification"
)

var ErrInvalidConfiguration = errors.New("invalid telegram configuration")

type Config struct {
	Enabled       bool
	Token, ChatID string
	BaseURL       string
}

func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.Token) == "" || strings.TrimSpace(c.ChatID) == "" || strings.ContainsAny(c.Token, "\r\n/?#") || strings.ContainsAny(c.ChatID, "\r\n") {
		return ErrInvalidConfiguration
	}
	return nil
}

type Adapter struct {
	client           *http.Client
	endpoint, chatID string
	now              func() time.Time
	mu               sync.RWMutex
	status           notification.ProviderStatus
}

func New(cfg Config, client *http.Client, now func() time.Time) (*Adapter, error) {
	if cfg.Validate() != nil || !cfg.Enabled {
		return nil, ErrInvalidConfiguration
	}
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	if now == nil {
		now = time.Now
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	return &Adapter{client: &copyClient, endpoint: base + "/bot" + cfg.Token + "/sendMessage", chatID: cfg.ChatID, now: now, status: notification.ProviderStatus{Provider: "telegram", State: "READY"}}, nil
}

func (a *Adapter) Send(ctx context.Context, message notification.RenderedMessage) (notification.Receipt, error) {
	body, _ := json.Marshal(struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}{a.chatID, message.Text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return notification.Receipt{}, a.fail("REQUEST_INVALID", false, 0)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(req)
	if err != nil {
		return notification.Receipt{}, a.fail("TRANSPORT", true, 0)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 32<<10))
	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	_ = json.Unmarshal(raw, &result)
	if response.StatusCode >= 200 && response.StatusCode < 300 && result.OK {
		at := a.now().UTC()
		a.mu.Lock()
		a.status.State = "READY"
		a.status.LastSuccess = at
		a.status.FailureClass = ""
		a.status.RateLimitedUntil = time.Time{}
		a.mu.Unlock()
		return notification.Receipt{ProviderMessageID: strconv.FormatInt(result.Result.MessageID, 10)}, nil
	}
	if response.StatusCode == http.StatusTooManyRequests {
		after := time.Duration(result.Parameters.RetryAfter) * time.Second
		if after <= 0 {
			after = time.Second
		}
		return notification.Receipt{}, a.fail("RATE_LIMITED", true, after)
	}
	if response.StatusCode >= 500 {
		return notification.Receipt{}, a.fail("SERVER_ERROR", true, 0)
	}
	return notification.Receipt{}, a.fail("PERMANENT_REQUEST", false, 0)
}
func (a *Adapter) fail(class string, retry bool, after time.Duration) error {
	at := a.now().UTC()
	a.mu.Lock()
	a.status.State = "DEGRADED"
	a.status.LastFailure = at
	a.status.FailureClass = class
	if after > 0 {
		a.status.RateLimitedUntil = at.Add(after)
	}
	a.mu.Unlock()
	if retry {
		return &notification.RetryableError{Class: class, After: after}
	}
	return errors.New(class)
}
func (a *Adapter) Status() notification.ProviderStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

type Disabled struct{}

func (Disabled) Send(context.Context, notification.RenderedMessage) (notification.Receipt, error) {
	return notification.Receipt{}, nil
}
func (Disabled) Status() notification.ProviderStatus {
	return notification.ProviderStatus{Provider: "telegram", State: "DISABLED"}
}
