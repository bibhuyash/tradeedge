package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"sync"

	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	riskstorage "github.com/bibhuyash/tradeedge/internal/risk/storage"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

type Limits struct {
	Policies  int
	Decisions int
	Controls  int
}

func DefaultLimits() Limits { return Limits{Policies: 64, Decisions: 10000, Controls: 256} }

type storedPolicy struct {
	value     riskmodel.RiskPolicy
	canonical []byte
}
type storedDecision struct {
	value     riskmodel.PortfolioRiskDecision
	canonical []byte
}

type proposalRevisionKey struct {
	proposal strategymodel.ProposalID
	revision portfoliomodel.PortfolioRevision
}

type killSwitchRevisionKey struct {
	id       portfoliomodel.KillSwitchID
	revision uint64
}

type circuitBreakerRevisionKey struct {
	id       portfoliomodel.CircuitBreakerID
	revision uint64
}

type Store struct {
	mu              sync.RWMutex
	limits          Limits
	policies        map[riskmodel.RiskPolicyID]storedPolicy
	decisions       map[riskmodel.PortfolioRiskDecisionID]storedDecision
	byProposal      map[proposalRevisionKey]riskmodel.PortfolioRiskDecisionID
	killSwitches    map[killSwitchRevisionKey]portfoliomodel.KillSwitch
	circuitBreakers map[circuitBreakerRevisionKey]portfoliomodel.CircuitBreaker
}

func NewStore() *Store { return NewStoreWithLimits(DefaultLimits()) }
func NewStoreWithLimits(limits Limits) *Store {
	if limits.Policies <= 0 || limits.Decisions <= 0 || limits.Controls <= 0 {
		limits = DefaultLimits()
	}
	return &Store{
		limits: limits, policies: make(map[riskmodel.RiskPolicyID]storedPolicy),
		decisions:       make(map[riskmodel.PortfolioRiskDecisionID]storedDecision),
		byProposal:      make(map[proposalRevisionKey]riskmodel.PortfolioRiskDecisionID),
		killSwitches:    make(map[killSwitchRevisionKey]portfoliomodel.KillSwitch),
		circuitBreakers: make(map[circuitBreakerRevisionKey]portfoliomodel.CircuitBreaker),
	}
}

func (store *Store) RegisterPolicy(ctx context.Context,
	value riskmodel.RiskPolicy) (riskstorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return riskstorage.RegistrationOutcome{}, err
	}
	canonical, err := json.Marshal(value.Spec())
	if err != nil || value.ID().IsZero() {
		return riskstorage.RegistrationOutcome{}, riskmodel.ErrInvalidRiskPolicy
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return riskstorage.RegistrationOutcome{}, err
	}
	if existing, found := store.policies[value.ID()]; found {
		if bytes.Equal(existing.canonical, canonical) {
			return riskstorage.RegistrationOutcome{Status: riskstorage.RegistrationIdempotent}, nil
		}
		return riskstorage.RegistrationOutcome{}, &riskstorage.IdentityCollisionError{
			Kind: "policy", Identity: value.ID().String(),
		}
	}
	if len(store.policies) >= store.limits.Policies {
		return riskstorage.RegistrationOutcome{}, riskstorage.ErrCapacityExhausted
	}
	store.policies[value.ID()] = storedPolicy{value: value, canonical: append([]byte(nil), canonical...)}
	return riskstorage.RegistrationOutcome{Status: riskstorage.RegistrationCommitted}, nil
}

func (store *Store) Policy(ctx context.Context,
	id riskmodel.RiskPolicyID) (riskmodel.RiskPolicy, error) {
	if err := ctx.Err(); err != nil {
		return riskmodel.RiskPolicy{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.policies[id]
	if !found {
		return riskmodel.RiskPolicy{}, riskstorage.ErrNotFound
	}
	return value.value, nil
}

func (store *Store) Policies(ctx context.Context) ([]riskmodel.RiskPolicy, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]riskmodel.RiskPolicy, 0, len(store.policies))
	for _, value := range store.policies {
		result = append(result, value.value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID().String() < result[j].ID().String() })
	return result, nil
}

func (store *Store) AppendDecision(ctx context.Context,
	value riskmodel.PortfolioRiskDecision) (riskstorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return riskstorage.RegistrationOutcome{}, err
	}
	canonical := value.CanonicalJSON()
	if value.ID().IsZero() || len(canonical) == 0 {
		return riskstorage.RegistrationOutcome{}, riskmodel.ErrInvalidPortfolioRiskDecision
	}
	spec := value.Spec()
	key := proposalRevisionKey{proposal: value.ProposalID(), revision: spec.ExpectedPortfolioRevision}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return riskstorage.RegistrationOutcome{}, err
	}
	if existing, found := store.decisions[value.ID()]; found {
		if bytes.Equal(existing.canonical, canonical) {
			return riskstorage.RegistrationOutcome{Status: riskstorage.RegistrationIdempotent}, nil
		}
		return riskstorage.RegistrationOutcome{}, &riskstorage.IdentityCollisionError{
			Kind: "decision", Identity: value.ID().String(),
		}
	}
	if existingID, found := store.byProposal[key]; found && existingID != value.ID() {
		return riskstorage.RegistrationOutcome{}, &riskstorage.IdentityCollisionError{
			Kind: "proposal_revision", Identity: value.ProposalID().String(),
		}
	}
	if len(store.decisions) >= store.limits.Decisions {
		return riskstorage.RegistrationOutcome{}, riskstorage.ErrCapacityExhausted
	}
	store.decisions[value.ID()] = storedDecision{value: value, canonical: append([]byte(nil), canonical...)}
	store.byProposal[key] = value.ID()
	return riskstorage.RegistrationOutcome{Status: riskstorage.RegistrationCommitted}, nil
}

func (store *Store) Decision(ctx context.Context,
	id riskmodel.PortfolioRiskDecisionID) (riskmodel.PortfolioRiskDecision, error) {
	if err := ctx.Err(); err != nil {
		return riskmodel.PortfolioRiskDecision{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.decisions[id]
	if !found {
		return riskmodel.PortfolioRiskDecision{}, riskstorage.ErrNotFound
	}
	return value.value, nil
}

func (store *Store) DecisionByProposal(ctx context.Context, proposal strategymodel.ProposalID,
	revision portfoliomodel.PortfolioRevision) (riskmodel.PortfolioRiskDecision, error) {
	if err := ctx.Err(); err != nil {
		return riskmodel.PortfolioRiskDecision{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	id, found := store.byProposal[proposalRevisionKey{proposal: proposal, revision: revision}]
	if !found {
		return riskmodel.PortfolioRiskDecision{}, riskstorage.ErrNotFound
	}
	return store.decisions[id].value, nil
}

func (store *Store) Decisions(ctx context.Context,
	portfolio portfoliomodel.PortfolioID) ([]riskmodel.PortfolioRiskDecision, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	var result []riskmodel.PortfolioRiskDecision
	for _, value := range store.decisions {
		if value.value.Spec().PortfolioID == portfolio {
			result = append(result, value.value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i].Spec(), result[j].Spec()
		if left.GeneratedAt.Equal(right.GeneratedAt) {
			return result[i].ID().String() < result[j].ID().String()
		}
		return left.GeneratedAt.Before(right.GeneratedAt)
	})
	return result, nil
}

func (store *Store) RegisterKillSwitchState(ctx context.Context,
	value portfoliomodel.KillSwitch) (riskstorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return riskstorage.RegistrationOutcome{}, err
	}
	spec := value.Spec()
	key := killSwitchRevisionKey{id: spec.ID, revision: spec.StateRevision}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return riskstorage.RegistrationOutcome{}, err
	}
	if existing, found := store.killSwitches[key]; found {
		if reflect.DeepEqual(existing.Spec(), spec) {
			return riskstorage.RegistrationOutcome{Status: riskstorage.RegistrationIdempotent}, nil
		}
		return riskstorage.RegistrationOutcome{}, &riskstorage.IdentityCollisionError{
			Kind: "kill_switch_state", Identity: spec.ID.String(),
		}
	}
	if len(store.killSwitches) >= store.limits.Controls {
		return riskstorage.RegistrationOutcome{}, riskstorage.ErrCapacityExhausted
	}
	store.killSwitches[key] = value
	return riskstorage.RegistrationOutcome{Status: riskstorage.RegistrationCommitted}, nil
}

func (store *Store) KillSwitchState(ctx context.Context,
	id portfoliomodel.KillSwitchID, revision uint64) (portfoliomodel.KillSwitch, error) {
	if err := ctx.Err(); err != nil {
		return portfoliomodel.KillSwitch{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.killSwitches[killSwitchRevisionKey{id: id, revision: revision}]
	if !found {
		return portfoliomodel.KillSwitch{}, riskstorage.ErrNotFound
	}
	return value, nil
}

func (store *Store) KillSwitchStates(ctx context.Context,
	id portfoliomodel.KillSwitchID) ([]portfoliomodel.KillSwitch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	var result []portfoliomodel.KillSwitch
	for key, value := range store.killSwitches {
		if key.id == id {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Spec().StateRevision < result[j].Spec().StateRevision
	})
	return result, nil
}

func (store *Store) RegisterCircuitBreakerState(ctx context.Context,
	value portfoliomodel.CircuitBreaker) (riskstorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return riskstorage.RegistrationOutcome{}, err
	}
	spec := value.Spec()
	key := circuitBreakerRevisionKey{id: spec.ID, revision: spec.StateRevision}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return riskstorage.RegistrationOutcome{}, err
	}
	if existing, found := store.circuitBreakers[key]; found {
		if reflect.DeepEqual(existing.Spec(), spec) {
			return riskstorage.RegistrationOutcome{Status: riskstorage.RegistrationIdempotent}, nil
		}
		return riskstorage.RegistrationOutcome{}, &riskstorage.IdentityCollisionError{
			Kind: "circuit_breaker_state", Identity: spec.ID.String(),
		}
	}
	if len(store.circuitBreakers) >= store.limits.Controls {
		return riskstorage.RegistrationOutcome{}, riskstorage.ErrCapacityExhausted
	}
	store.circuitBreakers[key] = value
	return riskstorage.RegistrationOutcome{Status: riskstorage.RegistrationCommitted}, nil
}

func (store *Store) CircuitBreakerState(ctx context.Context,
	id portfoliomodel.CircuitBreakerID, revision uint64) (portfoliomodel.CircuitBreaker, error) {
	if err := ctx.Err(); err != nil {
		return portfoliomodel.CircuitBreaker{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.circuitBreakers[circuitBreakerRevisionKey{id: id, revision: revision}]
	if !found {
		return portfoliomodel.CircuitBreaker{}, riskstorage.ErrNotFound
	}
	return value, nil
}

func (store *Store) CircuitBreakerStates(ctx context.Context,
	id portfoliomodel.CircuitBreakerID) ([]portfoliomodel.CircuitBreaker, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	var result []portfoliomodel.CircuitBreaker
	for key, value := range store.circuitBreakers {
		if key.id == id {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Spec().StateRevision < result[j].Spec().StateRevision
	})
	return result, nil
}
