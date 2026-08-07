package zerodha

import (
	"context"
	"errors"
	"sync"
	"time"

	zerodhaadapter "github.com/bibhuyash/tradeedge/internal/adapters/broker/zerodha"
	executionhealth "github.com/bibhuyash/tradeedge/internal/execution/health"
)

type Mode string

const (
	ModeOffline      Mode = "OFFLINE"
	ModePaper        Mode = "PAPER"
	ModeShadow       Mode = "SHADOW"
	ModeLiveDisabled Mode = "LIVE_DISABLED"
)

type State string

const (
	StateDisabled State = "DISABLED"
	StateReady    State = "READY"
	StateBlocked  State = "BLOCKED"
	StateStopped  State = "STOPPED"
)

type Health struct {
	Mode                  Mode                          `json:"mode"`
	State                 State                         `json:"state"`
	ReasonCodes           []string                      `json:"reason_codes"`
	MutationPermitted     bool                          `json:"mutation_permitted"`
	Session               string                        `json:"session_state,omitempty"`
	MappingVersion        string                        `json:"mapping_version,omitempty"`
	Stream                zerodhaadapter.StreamSnapshot `json:"stream"`
	ReconciliationBlocked bool                          `json:"reconciliation_blocked"`
	UnknownOrders         int                           `json:"unknown_orders"`
	EvaluatedAt           time.Time                     `json:"evaluated_at"`
}

type Connectivity interface {
	Check(context.Context) zerodhaadapter.ReadinessSnapshot
	Snapshot() zerodhaadapter.ReadinessSnapshot
	Shutdown()
}
type Stream interface {
	Snapshot() zerodhaadapter.StreamSnapshot
	Shutdown()
}
type PaperHealth interface {
	Health() executionhealth.PaperBroker
}
type ReconciliationHealth interface {
	Health() executionhealth.Reconciliation
}
type UnknownSource interface {
	UnknownCount(context.Context) (int, error)
}
type ShadowSource interface {
	Decisions(int) []zerodhaadapter.ShadowDecision
}

type Dependencies struct {
	Connectivity   Connectivity
	Stream         Stream
	Paper          PaperHealth
	Reconciliation ReconciliationHealth
	Unknown        UnknownSource
	Shadow         ShadowSource
	Clock          zerodhaadapter.Clock
}

type RecentError struct {
	Category   string    `json:"category"`
	Operation  string    `json:"operation"`
	Retryable  bool      `json:"retryable"`
	OccurredAt time.Time `json:"occurred_at"`
}

type Runtime struct {
	mode         Mode
	dependencies Dependencies
	mu           sync.RWMutex
	errors       []RecentError
	stopped      bool
}

func New(mode Mode, dependencies Dependencies) (*Runtime, error) {
	switch mode {
	case ModeOffline, ModeLiveDisabled:
	case ModePaper, ModeShadow:
		if dependencies.Connectivity == nil || dependencies.Stream == nil || dependencies.Paper == nil || dependencies.Reconciliation == nil || dependencies.Unknown == nil {
			return nil, errors.New("incomplete zerodha non-mutating runtime")
		}
	default:
		return nil, errors.New("invalid zerodha integration mode")
	}
	if dependencies.Clock == nil {
		dependencies.Clock = zerodhaadapter.RealClock{}
	}
	return &Runtime{mode: mode, dependencies: dependencies}, nil
}

func (runtime *Runtime) Health(ctx context.Context) Health {
	runtime.mu.RLock()
	stopped := runtime.stopped
	runtime.mu.RUnlock()
	now := runtime.dependencies.Clock.Now()
	result := Health{Mode: runtime.mode, State: StateDisabled, MutationPermitted: false, EvaluatedAt: now, Stream: zerodhaadapter.StreamSnapshot{State: zerodhaadapter.StreamStopped}}
	if stopped {
		result.State, result.ReasonCodes = StateStopped, []string{"INTEGRATION_STOPPED"}
		return result
	}
	if runtime.mode == ModeOffline {
		result.ReasonCodes = []string{"ZERODHA_OFFLINE"}
		return result
	}
	if runtime.mode == ModeLiveDisabled {
		result.State, result.ReasonCodes = StateBlocked, []string{"LIVE_UNAVAILABLE"}
		return result
	}
	connectivity := runtime.dependencies.Connectivity.Check(ctx)
	result.Session, result.MappingVersion, result.Stream = string(connectivity.Session), connectivity.MappingVersion, runtime.dependencies.Stream.Snapshot()
	result.State = StateReady
	if connectivity.State != zerodhaadapter.ReadinessReady {
		result.State, result.ReasonCodes = StateBlocked, append(result.ReasonCodes, connectivity.ReasonCodes...)
	}
	if result.Stream.State != zerodhaadapter.StreamConnected {
		result.State, result.ReasonCodes = StateBlocked, append(result.ReasonCodes, "ORDER_STREAM_NOT_READY")
	}
	if paper := runtime.dependencies.Paper.Health(); !paper.Available {
		result.State, result.ReasonCodes = StateBlocked, append(result.ReasonCodes, "PAPER_BROKER_UNAVAILABLE")
	}
	if health := runtime.dependencies.Reconciliation.Health(); health.Blocked || !health.Available {
		result.State, result.ReconciliationBlocked, result.ReasonCodes = StateBlocked, true, append(result.ReasonCodes, "RECONCILIATION_BLOCKED")
	}
	count, err := runtime.dependencies.Unknown.UnknownCount(ctx)
	if err != nil {
		result.State, result.ReasonCodes = StateBlocked, append(result.ReasonCodes, "UNKNOWN_STATUS_UNAVAILABLE")
	} else if count > 0 {
		result.State, result.UnknownOrders, result.ReasonCodes = StateBlocked, count, append(result.ReasonCodes, "UNKNOWN_ORDERS")
	}
	return result
}

func (runtime *Runtime) RecordError(category, operation string, retryable bool) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.errors = append(runtime.errors, RecentError{Category: boundedCategory(category), Operation: boundedOperation(operation), Retryable: retryable, OccurredAt: runtime.dependencies.Clock.Now()})
	if len(runtime.errors) > 1000 {
		runtime.errors = append([]RecentError(nil), runtime.errors[len(runtime.errors)-1000:]...)
	}
}
func (runtime *Runtime) Errors(limit int) []RecentError {
	runtime.mu.RLock()
	defer runtime.mu.RUnlock()
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	start := len(runtime.errors) - limit
	if start < 0 {
		start = 0
	}
	return append([]RecentError(nil), runtime.errors[start:]...)
}
func (runtime *Runtime) Shadow(limit int) []zerodhaadapter.ShadowDecision {
	if runtime.dependencies.Shadow == nil {
		return []zerodhaadapter.ShadowDecision{}
	}
	return runtime.dependencies.Shadow.Decisions(limit)
}
func (runtime *Runtime) Reconciliation() executionhealth.Reconciliation {
	if runtime.dependencies.Reconciliation == nil {
		return executionhealth.Reconciliation{}
	}
	return runtime.dependencies.Reconciliation.Health()
}
func (runtime *Runtime) Unknown(ctx context.Context) (int, error) {
	if runtime.dependencies.Unknown == nil {
		return 0, errors.New("unknown source unavailable")
	}
	return runtime.dependencies.Unknown.UnknownCount(ctx)
}
func (runtime *Runtime) Shutdown(context.Context) error {
	runtime.mu.Lock()
	if runtime.stopped {
		runtime.mu.Unlock()
		return nil
	}
	runtime.stopped = true
	runtime.mu.Unlock()
	if runtime.dependencies.Stream != nil {
		runtime.dependencies.Stream.Shutdown()
	}
	if runtime.dependencies.Connectivity != nil {
		runtime.dependencies.Connectivity.Shutdown()
	}
	return nil
}
func boundedCategory(value string) string {
	switch value {
	case "session", "mapping", "stream", "rate_limit", "reconciliation", "checkpoint", "adapter":
		return value
	default:
		return "adapter"
	}
}
func boundedOperation(value string) string {
	switch value {
	case "connect", "read", "reconnect", "restore", "reconcile", "shutdown":
		return value
	default:
		return "read"
	}
}
