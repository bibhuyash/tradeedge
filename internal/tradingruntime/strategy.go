package tradingruntime

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
)

type StrategyState string

const (
	StrategyRegistered        StrategyState = "REGISTERED"
	StrategyDisabled          StrategyState = "DISABLED"
	StrategyWarmingUp         StrategyState = "WARMING_UP"
	StrategyActive            StrategyState = "ACTIVE"
	StrategySessionRestricted StrategyState = "SESSION_RESTRICTED"
	StrategyRiskRestricted    StrategyState = "RISK_RESTRICTED"
	StrategyHalted            StrategyState = "HALTED"
	StrategyStopping          StrategyState = "STOPPING"
)

type CASPolicy string

const (
	CASSafe       CASPolicy = "CAS_SAFE"
	CASRestricted CASPolicy = "CAS_RESTRICTED"
	CASDisabled   CASPolicy = "CAS_DISABLED"
)

type StrategyRegistration struct {
	ID      domain.StrategyID
	Enabled bool
	CAS     CASPolicy
	Version string
}

type StrategySnapshot struct {
	ID        domain.StrategyID `json:"-"`
	Strategy  string            `json:"strategy_id"`
	State     StrategyState     `json:"state"`
	CASPolicy CASPolicy         `json:"cas_policy"`
	Reason    string            `json:"reason,omitempty"`
	Revision  uint64            `json:"revision"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type StrategyTransition struct {
	Strategy string        `json:"strategy_id"`
	From     StrategyState `json:"from,omitempty"`
	To       StrategyState `json:"to"`
	Reason   string        `json:"reason"`
	Revision uint64        `json:"revision"`
	At       time.Time     `json:"at"`
}

type StrategyManager struct {
	mu      sync.Mutex
	values  map[domain.StrategyID]StrategySnapshot
	journal []StrategyTransition
	maximum int
}

func NewStrategyManager() *StrategyManager {
	return &StrategyManager{values: map[domain.StrategyID]StrategySnapshot{}, maximum: 1000}
}

func (m *StrategyManager) Register(value StrategyRegistration, at time.Time) (StrategySnapshot, error) {
	if value.ID == "" || value.Version == "" || at.IsZero() || (value.CAS != CASSafe && value.CAS != CASRestricted && value.CAS != CASDisabled) {
		return StrategySnapshot{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, found := m.values[value.ID]; found {
		return StrategySnapshot{}, errors.New("strategy already registered")
	}
	state := StrategyRegistered
	if !value.Enabled {
		state = StrategyDisabled
	}
	snapshot := StrategySnapshot{ID: value.ID, Strategy: string(value.ID), State: state, CASPolicy: value.CAS, Revision: 1, UpdatedAt: at.UTC()}
	m.values[value.ID] = snapshot
	m.recordLocked(StrategyTransition{Strategy: snapshot.Strategy, To: snapshot.State, Reason: "REGISTERED", Revision: snapshot.Revision, At: snapshot.UpdatedAt})
	return snapshot, nil
}

func (m *StrategyManager) Reconcile(session SessionSnapshot, readiness ReadinessSnapshot, controls ControlSnapshot, at time.Time) []StrategySnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, current := range m.values {
		if current.State == StrategyDisabled || current.State == StrategyHalted || current.State == StrategyStopping {
			continue
		}
		next, reason := eligibleState(current, session, readiness, controls)
		if next != current.State || reason != current.Reason {
			previous := current.State
			current.State, current.Reason, current.Revision, current.UpdatedAt = next, reason, current.Revision+1, at.UTC()
			m.values[id] = current
			m.recordLocked(StrategyTransition{Strategy: current.Strategy, From: previous, To: next, Reason: reason, Revision: current.Revision, At: current.UpdatedAt})
		}
	}
	return m.snapshotsLocked()
}

func eligibleState(value StrategySnapshot, session SessionSnapshot, readiness ReadinessSnapshot, controls ControlSnapshot) (StrategyState, string) {
	if controls.GlobalBlocked || controls.Strategies[value.ID] {
		return StrategyHalted, "KILL_SWITCH_OR_CIRCUIT_BREAKER"
	}
	if !readiness.Ready {
		return StrategyRiskRestricted, "RUNTIME_NOT_READY"
	}
	if session.State == SessionWarmingUp || session.State == SessionReady {
		return StrategyWarmingUp, "WARMING_UP"
	}
	if session.State != SessionNormalTrading && session.State != SessionPreCAS && session.State != SessionCASActive && session.State != SessionPostCAS {
		return StrategySessionRestricted, "SESSION_INELIGIBLE"
	}
	if session.Regime == calendar.RegimeCAS && value.CASPolicy != CASSafe {
		return StrategySessionRestricted, "CAS_POLICY"
	}
	return StrategyActive, ""
}

func (m *StrategyManager) Stop(at time.Time) []StrategySnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, current := range m.values {
		if current.State != StrategyDisabled {
			previous := current.State
			current.State, current.Reason, current.Revision, current.UpdatedAt = StrategyStopping, "RUNTIME_DRAINING", current.Revision+1, at.UTC()
			m.values[id] = current
			m.recordLocked(StrategyTransition{Strategy: current.Strategy, From: previous, To: current.State, Reason: current.Reason, Revision: current.Revision, At: current.UpdatedAt})
		}
	}
	return m.snapshotsLocked()
}

func (m *StrategyManager) Disable(id domain.StrategyID, reason string, at time.Time) (StrategySnapshot, error) {
	return m.setExplicit(id, StrategyDisabled, reason, at)
}

// Recover returns a halted strategy to REGISTERED. Eligibility reconciliation
// must still pass before it can warm up or become active.
func (m *StrategyManager) Recover(id domain.StrategyID, evidence string, at time.Time) (StrategySnapshot, error) {
	if evidence == "" {
		return StrategySnapshot{}, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, found := m.values[id]
	if !found || current.State != StrategyHalted || at.IsZero() {
		return StrategySnapshot{}, ErrInvalidTransition
	}
	previous := current.State
	current.State, current.Reason, current.Revision, current.UpdatedAt = StrategyRegistered, evidence, current.Revision+1, at.UTC()
	m.values[id] = current
	m.recordLocked(StrategyTransition{Strategy: current.Strategy, From: previous, To: current.State, Reason: evidence, Revision: current.Revision, At: current.UpdatedAt})
	return current, nil
}

func (m *StrategyManager) setExplicit(id domain.StrategyID, state StrategyState, reason string, at time.Time) (StrategySnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, found := m.values[id]
	if !found || reason == "" || at.IsZero() || current.State == StrategyStopping {
		return StrategySnapshot{}, ErrInvalidTransition
	}
	previous := current.State
	current.State, current.Reason, current.Revision, current.UpdatedAt = state, reason, current.Revision+1, at.UTC()
	m.values[id] = current
	m.recordLocked(StrategyTransition{Strategy: current.Strategy, From: previous, To: state, Reason: reason, Revision: current.Revision, At: current.UpdatedAt})
	return current, nil
}

func (m *StrategyManager) Transitions() []StrategyTransition {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]StrategyTransition(nil), m.journal...)
}

func (m *StrategyManager) recordLocked(value StrategyTransition) {
	m.journal = append(m.journal, value)
	if len(m.journal) > m.maximum {
		m.journal = append([]StrategyTransition(nil), m.journal[len(m.journal)-m.maximum:]...)
	}
}

func (m *StrategyManager) Snapshots() []StrategySnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotsLocked()
}
func (m *StrategyManager) snapshotsLocked() []StrategySnapshot {
	values := make([]StrategySnapshot, 0, len(m.values))
	for _, value := range m.values {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Strategy < values[j].Strategy })
	return values
}

func strategyAllows(snapshot StrategySnapshot, effect ExposureEffect, regime calendar.Regime) bool {
	if snapshot.State == StrategyActive {
		return true
	}
	return effect == ExposureReduce && snapshot.State == StrategySessionRestricted && regime == calendar.RegimeCAS && snapshot.CASPolicy != CASDisabled
}
