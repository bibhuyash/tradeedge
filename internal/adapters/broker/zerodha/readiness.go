package zerodha

import (
	"context"
	"errors"
	"sync"
	"time"

	brokertelemetry "github.com/bibhuyash/tradeedge/internal/broker/telemetry"
)

type ReadinessState string

const (
	ReadinessDisabled      ReadinessState = "DISABLED"
	ReadinessLoginRequired ReadinessState = "LOGIN_REQUIRED"
	ReadinessReady         ReadinessState = "READY"
	ReadinessDegraded      ReadinessState = "DEGRADED"
	ReadinessExpired       ReadinessState = "EXPIRED"
	ReadinessStopped       ReadinessState = "STOPPED"
)

type ReadinessSnapshot struct {
	State             ReadinessState     `json:"state"`
	ReasonCodes       []string           `json:"reason_codes"`
	Session           SessionState       `json:"session_state"`
	MappingVersion    string             `json:"mapping_version,omitempty"`
	LastCheckedAt     time.Time          `json:"last_checked_at,omitempty"`
	ReadOnly          bool               `json:"read_only"`
	OrderMutation     bool               `json:"order_mutation_permitted"`
	CapabilitySummary CapabilitySnapshot `json:"capabilities"`
}

type Connectivity struct {
	enabled  bool
	client   *Client
	session  *SessionManager
	mapper   *Mapper
	clock    Clock
	recorder brokertelemetry.Recorder
	mu       sync.RWMutex
	snapshot ReadinessSnapshot
}

func NewConnectivity(enabled bool, client *Client, session *SessionManager, mapper *Mapper, clock Clock, recorder brokertelemetry.Recorder) (*Connectivity, error) {
	if clock == nil {
		clock = RealClock{}
	}
	value := &Connectivity{enabled: enabled, client: client, session: session, mapper: mapper, clock: clock, recorder: brokertelemetry.Safe(recorder)}
	value.snapshot = ReadinessSnapshot{State: ReadinessDisabled, ReasonCodes: []string{"ZERODHA_DISABLED"}, ReadOnly: true, OrderMutation: false}
	if enabled && (client == nil || session == nil || mapper == nil) {
		return nil, ErrInvalidConfiguration
	}
	return value, nil
}

func (connectivity *Connectivity) Check(ctx context.Context) ReadinessSnapshot {
	if !connectivity.enabled {
		return connectivity.Snapshot()
	}
	session := connectivity.session.Snapshot()
	result := ReadinessSnapshot{Session: session.State, LastCheckedAt: connectivity.clock.Now(), ReadOnly: true, OrderMutation: false, MappingVersion: string(connectivity.mapper.master.Version())}
	switch session.State {
	case SessionLoginRequired, SessionUnconfigured, SessionAuthFailed:
		result.State, result.ReasonCodes = ReadinessLoginRequired, []string{"ZERODHA_LOGIN_REQUIRED"}
	case SessionExpired:
		result.State, result.ReasonCodes = ReadinessExpired, []string{"ZERODHA_SESSION_EXPIRED"}
	case SessionStopped:
		result.State, result.ReasonCodes = ReadinessStopped, []string{"ZERODHA_STOPPED"}
	default:
		capabilities, err := connectivity.client.Capabilities(ctx)
		if err != nil {
			result.State, result.ReasonCodes = ReadinessDegraded, []string{readinessReason(err)}
		} else if err = connectivity.mapper.validateGeneration(result.LastCheckedAt); err != nil {
			result.State, result.ReasonCodes = ReadinessDegraded, []string{"ZERODHA_MAPPING_STALE"}
		} else {
			result.State, result.CapabilitySummary = ReadinessReady, capabilities
		}
	}
	connectivity.mu.Lock()
	connectivity.snapshot = result
	connectivity.mu.Unlock()
	outcome := brokertelemetry.OutcomeSuccess
	if result.State != ReadinessReady {
		outcome = brokertelemetry.OutcomeFailure
	}
	connectivity.recorder.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationReadiness, Outcome: outcome})
	return connectivity.Snapshot()
}

func (connectivity *Connectivity) Snapshot() ReadinessSnapshot {
	connectivity.mu.RLock()
	defer connectivity.mu.RUnlock()
	result := connectivity.snapshot
	result.ReasonCodes = append([]string(nil), result.ReasonCodes...)
	result.CapabilitySummary.Exchanges = append([]string(nil), result.CapabilitySummary.Exchanges...)
	result.CapabilitySummary.Products = append([]string(nil), result.CapabilitySummary.Products...)
	result.CapabilitySummary.OrderTypes = append([]string(nil), result.CapabilitySummary.OrderTypes...)
	return result
}

func (connectivity *Connectivity) Shutdown() {
	if connectivity.client != nil {
		connectivity.client.Shutdown()
	}
	connectivity.mu.Lock()
	connectivity.snapshot.State = ReadinessStopped
	connectivity.snapshot.ReasonCodes = []string{"ZERODHA_STOPPED"}
	connectivity.snapshot.CapabilitySummary = CapabilitySnapshot{}
	connectivity.mu.Unlock()
}

func readinessReason(err error) string {
	switch {
	case errors.Is(err, ErrSessionExpired):
		return "ZERODHA_SESSION_EXPIRED"
	case errors.Is(err, ErrRateLimited):
		return "ZERODHA_RATE_LIMITED"
	case errors.Is(err, context.DeadlineExceeded):
		return "ZERODHA_TIMEOUT"
	case errors.Is(err, ErrMalformedResponse):
		return "ZERODHA_MALFORMED_RESPONSE"
	default:
		return "ZERODHA_UNAVAILABLE"
	}
}
