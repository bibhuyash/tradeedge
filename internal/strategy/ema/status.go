package ema

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// ValidationStatus is the bounded operational view for the one Phase 8
// candidate. It is evidence only and grants no strategy or execution authority.
type ValidationStatus struct {
	StrategyID                        string    `json:"strategy_id"`
	Version                           string    `json:"version"`
	Enabled                           bool      `json:"enabled"`
	WarmupSamples                     int       `json:"warmup_samples"`
	WarmupRequired                    int       `json:"warmup_required"`
	LatestFastEMA                     int64     `json:"latest_fast_ema_scaled"`
	LatestSlowEMA                     int64     `json:"latest_slow_ema_scaled"`
	LastSignal                        string    `json:"last_signal,omitempty"`
	LastSignalTime                    time.Time `json:"last_signal_time,omitempty"`
	LastNoActionReason                string    `json:"last_no_action_reason,omitempty"`
	ProposalsEmitted                  uint64    `json:"proposals_emitted"`
	ProposalsBlocked                  uint64    `json:"proposals_blocked"`
	CurrentAuthoritativePositionState string    `json:"current_authoritative_position_state"`
	ValidationMode                    string    `json:"validation_mode"`
}

type StatusRecorder struct {
	mu    sync.RWMutex
	value ValidationStatus
}

func NewStatusRecorder(enabled bool) *StatusRecorder {
	return &StatusRecorder{value: ValidationStatus{StrategyID: DefinitionName, Version: "1", Enabled: enabled, WarmupRequired: 50, CurrentAuthoritativePositionState: "UNKNOWN", ValidationMode: "PAPER_VALIDATION"}}
}

func (r *StatusRecorder) Snapshot() ValidationStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.value
}

func (r *StatusRecorder) Replace(value ValidationStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value.StrategyID, value.Version, value.ValidationMode, value.WarmupRequired = DefinitionName, "1", "PAPER_VALIDATION", 50
	if value.WarmupSamples < 0 {
		value.WarmupSamples = 0
	}
	if value.WarmupSamples > value.WarmupRequired {
		value.WarmupSamples = value.WarmupRequired
	}
	r.value = value
}

// Handler exposes one bounded GET-only snapshot. It cannot trigger evaluation,
// mutate controls, place orders, or enumerate history.
func (r *StatusRecorder) Handler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if request.URL.Path != "/api/v1/strategy/validation" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(r.Snapshot())
	})
}
