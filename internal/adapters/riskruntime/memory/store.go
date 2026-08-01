package memory

import (
	"bytes"
	"context"
	"sync"

	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	riskstorage "github.com/bibhuyash/tradeedge/internal/risk/storage"
)

type Limits struct {
	Portfolios, Publications int
}

func DefaultLimits() Limits { return Limits{Portfolios: 16, Publications: 10000} }

type storedPublication struct {
	value     riskstorage.PortfolioDecisionPublication
	receipt   riskstorage.RuntimePublicationReceipt
	canonical []byte
}

type Store struct {
	mu               sync.RWMutex
	limits           Limits
	current          map[portfoliomodel.PortfolioID]portfoliomodel.PortfolioRevision
	checkpoints      map[portfoliomodel.PortfolioID]map[portfoliomodel.PortfolioRevision]riskstorage.PortfolioCheckpoint
	publications     map[riskmodel.DecisionTriggerID]storedPublication
	decisions        map[riskmodel.PortfolioRiskDecisionID]riskmodel.PortfolioRiskDecision
	candidates       map[portfoliomodel.AllocationCandidateID]portfoliomodel.AllocationCandidate
	evaluations      map[riskmodel.RiskEvaluationID]riskmodel.RiskEvaluation
	reservations     map[portfoliomodel.CapitalReservationID]portfoliomodel.CapitalReservation
	failBeforeCommit bool
}

func NewStore() *Store { return NewStoreWithLimits(DefaultLimits()) }
func NewStoreWithLimits(limits Limits) *Store {
	if limits.Portfolios <= 0 || limits.Publications <= 0 {
		limits = DefaultLimits()
	}
	return &Store{limits: limits, current: make(map[portfoliomodel.PortfolioID]portfoliomodel.PortfolioRevision),
		checkpoints:  make(map[portfoliomodel.PortfolioID]map[portfoliomodel.PortfolioRevision]riskstorage.PortfolioCheckpoint),
		publications: make(map[riskmodel.DecisionTriggerID]storedPublication),
		decisions:    make(map[riskmodel.PortfolioRiskDecisionID]riskmodel.PortfolioRiskDecision),
		candidates:   make(map[portfoliomodel.AllocationCandidateID]portfoliomodel.AllocationCandidate),
		evaluations:  make(map[riskmodel.RiskEvaluationID]riskmodel.RiskEvaluation),
		reservations: make(map[portfoliomodel.CapitalReservationID]portfoliomodel.CapitalReservation)}
}

func (store *Store) SetFailBeforeCommitForTest(value bool) {
	store.mu.Lock()
	store.failBeforeCommit = value
	store.mu.Unlock()
}

func (store *Store) InitializePortfolio(ctx context.Context, checkpoint riskstorage.PortfolioCheckpoint) (riskstorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return riskstorage.RegistrationOutcome{}, err
	}
	validated, err := riskstorage.NewPortfolioCheckpoint(checkpoint)
	if err != nil || validated.Snapshot.Revision() != 1 {
		return riskstorage.RegistrationOutcome{}, riskstorage.ErrCorruptPortfolioCheckpoint
	}
	portfolio := validated.Snapshot.PortfolioID()
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return riskstorage.RegistrationOutcome{}, err
	}
	if revision, found := store.current[portfolio]; found {
		existing := store.checkpoints[portfolio][revision]
		if bytes.Equal(existing.CanonicalJSON(), validated.CanonicalJSON()) {
			return riskstorage.RegistrationOutcome{Status: riskstorage.RegistrationIdempotent}, nil
		}
		return riskstorage.RegistrationOutcome{}, &riskstorage.IdentityCollisionError{Kind: "portfolio_genesis", Identity: portfolio.String()}
	}
	if len(store.current) >= store.limits.Portfolios {
		return riskstorage.RegistrationOutcome{}, riskstorage.ErrCapacityExhausted
	}
	store.checkpoints[portfolio] = map[portfoliomodel.PortfolioRevision]riskstorage.PortfolioCheckpoint{1: validated}
	store.current[portfolio] = 1
	return riskstorage.RegistrationOutcome{Status: riskstorage.RegistrationCommitted}, nil
}

func (store *Store) RestorePortfolioCheckpoint(ctx context.Context, checkpoint riskstorage.PortfolioCheckpoint) (riskstorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return riskstorage.RegistrationOutcome{}, err
	}
	validated, err := riskstorage.NewPortfolioCheckpoint(checkpoint)
	if err != nil {
		return riskstorage.RegistrationOutcome{}, riskstorage.ErrCorruptPortfolioCheckpoint
	}
	portfolio := validated.Snapshot.PortfolioID()
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, found := store.current[portfolio]; found {
		return riskstorage.RegistrationOutcome{}, &riskstorage.IdentityCollisionError{Kind: "portfolio_restore", Identity: portfolio.String()}
	}
	if len(store.current) >= store.limits.Portfolios {
		return riskstorage.RegistrationOutcome{}, riskstorage.ErrCapacityExhausted
	}
	store.checkpoints[portfolio] = map[portfoliomodel.PortfolioRevision]riskstorage.PortfolioCheckpoint{
		validated.Snapshot.Revision(): validated,
	}
	store.current[portfolio] = validated.Snapshot.Revision()
	return riskstorage.RegistrationOutcome{Status: riskstorage.RegistrationCommitted}, nil
}

func (store *Store) CurrentPortfolioCheckpoint(ctx context.Context, id portfoliomodel.PortfolioID) (riskstorage.PortfolioCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return riskstorage.PortfolioCheckpoint{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	revision, found := store.current[id]
	if !found {
		return riskstorage.PortfolioCheckpoint{}, riskstorage.ErrNotFound
	}
	return store.checkpoints[id][revision], nil
}

func (store *Store) PortfolioCheckpoint(ctx context.Context, id portfoliomodel.PortfolioID, revision portfoliomodel.PortfolioRevision) (riskstorage.PortfolioCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return riskstorage.PortfolioCheckpoint{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	values, found := store.checkpoints[id]
	if !found {
		return riskstorage.PortfolioCheckpoint{}, riskstorage.ErrNotFound
	}
	value, found := values[revision]
	if !found {
		return riskstorage.PortfolioCheckpoint{}, riskstorage.ErrNotFound
	}
	return value, nil
}

func (store *Store) CommittedPublication(ctx context.Context, trigger riskmodel.DecisionTriggerID) (riskstorage.RuntimePublicationReceipt, error) {
	if err := ctx.Err(); err != nil {
		return riskstorage.RuntimePublicationReceipt{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.publications[trigger]
	if !found {
		return riskstorage.RuntimePublicationReceipt{}, riskstorage.ErrNotFound
	}
	return value.receipt, nil
}

func (store *Store) PublishPortfolioDecision(ctx context.Context, publication riskstorage.PortfolioDecisionPublication) (riskstorage.RuntimePublicationReceipt, error) {
	if err := ctx.Err(); err != nil {
		return riskstorage.RuntimePublicationReceipt{}, err
	}
	validated, err := riskstorage.NewPortfolioDecisionPublication(publication)
	if err != nil {
		return riskstorage.RuntimePublicationReceipt{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return riskstorage.RuntimePublicationReceipt{}, err
	}
	if existing, found := store.publications[validated.TriggerID]; found {
		if bytes.Equal(existing.canonical, validated.CanonicalJSON()) {
			receipt := existing.receipt
			receipt.Status = riskstorage.RuntimePublicationIdempotent
			return receipt, nil
		}
		return riskstorage.RuntimePublicationReceipt{}, &riskstorage.IdentityCollisionError{Kind: "decision_trigger", Identity: validated.TriggerID.String()}
	}
	actual, found := store.current[validated.PortfolioID]
	if !found {
		return riskstorage.RuntimePublicationReceipt{}, riskstorage.ErrNotFound
	}
	current := store.checkpoints[validated.PortfolioID][actual]
	if actual != validated.ExpectedRevision || current.Snapshot.ID() != validated.ExpectedSnapshotID ||
		current.CheckpointChecksum != validated.ExpectedCheckpoint {
		return riskstorage.RuntimePublicationReceipt{}, &riskstorage.PortfolioRevisionConflictError{
			PortfolioID: validated.PortfolioID, Expected: validated.ExpectedRevision, Actual: actual}
	}
	if validated.NextCheckpoint.ParentSnapshotID != current.Snapshot.ID() ||
		validated.NextCheckpoint.ParentChecksum != current.CheckpointChecksum {
		return riskstorage.RuntimePublicationReceipt{}, riskstorage.ErrCorruptPortfolioCheckpoint
	}
	if len(store.publications) >= store.limits.Publications {
		return riskstorage.RuntimePublicationReceipt{}, riskstorage.ErrCapacityExhausted
	}
	if store.failBeforeCommit {
		return riskstorage.RuntimePublicationReceipt{}, riskstorage.ErrInternal
	}
	receipt := riskstorage.RuntimePublicationReceipt{Status: riskstorage.RuntimePublicationCommitted,
		TriggerID: validated.TriggerID, DecisionID: validated.Decision.ID(),
		SnapshotID: validated.NextCheckpoint.Snapshot.ID(), Revision: validated.NextCheckpoint.Snapshot.Revision(),
		PublicationChecksum: validated.PublicationChecksum}
	if validated.Reservation != nil {
		receipt.ReservationID = validated.Reservation.ID()
	}
	store.checkpoints[validated.PortfolioID][receipt.Revision] = validated.NextCheckpoint
	store.current[validated.PortfolioID] = receipt.Revision
	store.decisions[validated.Decision.ID()] = validated.Decision
	store.candidates[validated.Candidate.ID()] = validated.Candidate
	store.evaluations[validated.Evaluation.ID()] = validated.Evaluation
	if validated.Reservation != nil {
		store.reservations[validated.Reservation.ID()] = *validated.Reservation
	}
	store.publications[validated.TriggerID] = storedPublication{value: validated, receipt: receipt, canonical: validated.CanonicalJSON()}
	return receipt, nil
}

func (store *Store) Decision(ctx context.Context, id riskmodel.PortfolioRiskDecisionID) (riskmodel.PortfolioRiskDecision, error) {
	if err := ctx.Err(); err != nil {
		return riskmodel.PortfolioRiskDecision{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.decisions[id]
	if !found {
		return riskmodel.PortfolioRiskDecision{}, riskstorage.ErrNotFound
	}
	return value, nil
}
func (store *Store) Candidate(ctx context.Context, id portfoliomodel.AllocationCandidateID) (portfoliomodel.AllocationCandidate, error) {
	if err := ctx.Err(); err != nil {
		return portfoliomodel.AllocationCandidate{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.candidates[id]
	if !found {
		return portfoliomodel.AllocationCandidate{}, riskstorage.ErrNotFound
	}
	return value, nil
}
func (store *Store) Evaluation(ctx context.Context, id riskmodel.RiskEvaluationID) (riskmodel.RiskEvaluation, error) {
	if err := ctx.Err(); err != nil {
		return riskmodel.RiskEvaluation{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.evaluations[id]
	if !found {
		return riskmodel.RiskEvaluation{}, riskstorage.ErrNotFound
	}
	return value, nil
}
func (store *Store) Reservation(ctx context.Context, id portfoliomodel.CapitalReservationID) (portfoliomodel.CapitalReservation, error) {
	if err := ctx.Err(); err != nil {
		return portfoliomodel.CapitalReservation{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.reservations[id]
	if !found {
		return portfoliomodel.CapitalReservation{}, riskstorage.ErrNotFound
	}
	return value, nil
}
