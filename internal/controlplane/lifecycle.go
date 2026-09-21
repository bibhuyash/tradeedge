package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"time"

	zerodhaops "github.com/bibhuyash/tradeedge/internal/integration/zerodha"
	"github.com/bibhuyash/tradeedge/internal/marketvalidation"
	"github.com/bibhuyash/tradeedge/internal/operator/preparation"
	operatorstartup "github.com/bibhuyash/tradeedge/internal/operator/startup"
	"github.com/bibhuyash/tradeedge/internal/session"
)

type sessionSource struct{ service *session.Service }

func (s sessionSource) Authenticated(ctx context.Context) (bool, error) {
	status, err := s.service.Status(ctx)
	return status.State == session.StateAuthenticated, err
}

type preparationSource struct {
	config preparation.Config
	runner preparation.Runner
}

func (p preparationSource) Prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return preparation.Prepare(p.config, p.runner, io.Discard)
}

type marketSource struct {
	policyPath string
	now        func() time.Time
}

func (m marketSource) TradingDate() string {
	return m.now().In(time.FixedZone("IST", 19800)).Format("2006-01-02")
}

func (m marketSource) Closed(ctx context.Context) (bool, string, error) {
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	now := m.now().In(time.FixedZone("IST", 19800))
	date := now.Format("2006-01-02")
	_, _, classification, err := marketvalidation.GenerateTradingCalendar(m.policyPath, filepath.Join(filepath.Dir(m.policyPath), "probe.json"), date)
	if err != nil {
		return false, "", err
	}
	if classification == marketvalidation.CalendarTradingDay {
		if now.Hour() >= 16 {
			return true, "SESSION_CLOSED", nil
		}
		return false, "", nil
	}
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return true, "WEEKEND", nil
	}
	return true, "HOLIDAY", nil
}

type readinessClient struct {
	url     string
	client  *http.Client
	timeout time.Duration
}

func (r readinessClient) Ready(ctx context.Context) (bool, error) {
	deadline := time.NewTimer(r.timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
		if err != nil {
			return false, err
		}
		response, err := r.client.Do(request)
		if err == nil {
			var body struct {
				Status string `json:"status"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&body)
			response.Body.Close()
			if decodeErr == nil && response.StatusCode == http.StatusOK && body.Status == "ready" {
				return true, nil
			}
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			return false, errors.New("shadow readiness timeout")
		case <-ticker.C:
		}
	}
}

func newPreparationConfig(config Config, clock session.Clock) preparation.Config {
	return preparation.Config{Repository: config.Repository, CredentialsFile: filepath.Join(config.Repository, ".env"), SelectorsFile: filepath.Join(config.Repository, ".cache", "tradeedge", "preparation.env"), SessionFile: config.SessionFile, ValidationCommand: config.ValidationCommand, Now: clock.Now(), ReadOnly: func(args []string, sessionFile string) (string, error) {
		var stdout, stderr bytes.Buffer
		if zerodhaops.ExecuteStoredReadOnly(args, sessionFile, &stdout, &stderr) != 0 {
			return stdout.String(), errors.New("read-only Zerodha operation failed")
		}
		return stdout.String(), nil
	}}
}

func newLifecycle(config Config, service *session.Service, clock session.Clock, runtime operatorstartup.RuntimeManager) (*operatorstartup.Service, error) {
	return operatorstartup.New(preparationSource{config: newPreparationConfig(config, clock), runner: preparation.CommandRunner{Directory: config.Repository}}, runtime, readinessClient{url: config.ShadowReadinessURL, client: &http.Client{Timeout: 2 * time.Second}, timeout: config.StartupTimeout}, sessionSource{service}, marketSource{policyPath: filepath.Join(config.Repository, ".cache", "market-validation", "config", "nse-calendar-policy-2026.json"), now: clock.Now})
}
