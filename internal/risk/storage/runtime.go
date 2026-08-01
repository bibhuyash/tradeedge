package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

var (
	ErrInvalidPortfolioPublication = errors.New("invalid portfolio decision publication")
	ErrCorruptPortfolioCheckpoint  = errors.New("corrupt portfolio checkpoint")
)

type PortfolioRevisionConflictError struct {
	PortfolioID portfoliomodel.PortfolioID
	Expected    portfoliomodel.PortfolioRevision
	Actual      portfoliomodel.PortfolioRevision
}

func (value *PortfolioRevisionConflictError) Error() string {
	return fmt.Sprintf("%v for %s: expected %d, actual %d", ErrStaleRevision,
		value.PortfolioID, value.Expected, value.Actual)
}
func (value *PortfolioRevisionConflictError) Unwrap() error { return ErrStaleRevision }

type PortfolioCheckpoint struct {
	Snapshot           portfoliomodel.PortfolioSnapshot
	SnapshotChecksum   portfoliomodel.StateChecksum
	ParentSnapshotID   portfoliomodel.PortfolioSnapshotID
	ParentChecksum     portfoliomodel.StateChecksum
	TriggerID          riskmodel.DecisionTriggerID
	ProposalID         strategymodel.ProposalID
	DecisionID         riskmodel.PortfolioRiskDecisionID
	ReservationID      portfoliomodel.CapitalReservationID
	CheckpointChecksum portfoliomodel.StateChecksum
	canonical          []byte
}

func NewPortfolioCheckpoint(value PortfolioCheckpoint) (PortfolioCheckpoint, error) {
	if value.Snapshot.ID().IsZero() || value.Snapshot.Revision().Validate() != nil {
		return PortfolioCheckpoint{}, ErrCorruptPortfolioCheckpoint
	}
	snapshotChecksum, _ := portfoliomodel.NewStateChecksum(value.Snapshot.CanonicalJSON())
	if !value.SnapshotChecksum.IsZero() && value.SnapshotChecksum != snapshotChecksum {
		return PortfolioCheckpoint{}, ErrCorruptPortfolioCheckpoint
	}
	value.SnapshotChecksum = snapshotChecksum
	genesis := value.Snapshot.Revision() == 1
	if genesis {
		if !value.ParentSnapshotID.IsZero() || !value.ParentChecksum.IsZero() ||
			!value.TriggerID.IsZero() || !value.ProposalID.IsZero() || !value.DecisionID.IsZero() ||
			!value.ReservationID.IsZero() {
			return PortfolioCheckpoint{}, ErrCorruptPortfolioCheckpoint
		}
	} else if value.ParentSnapshotID.IsZero() || value.ParentChecksum.IsZero() ||
		value.TriggerID.IsZero() || value.ProposalID.IsZero() || value.DecisionID.IsZero() {
		return PortfolioCheckpoint{}, ErrCorruptPortfolioCheckpoint
	}
	raw, err := json.Marshal(struct {
		PortfolioID, SnapshotID, SnapshotChecksum, ParentSnapshotID, ParentChecksum string
		TriggerID, ProposalID, DecisionID, ReservationID                            string
		Revision                                                                    uint64
	}{
		PortfolioID: value.Snapshot.PortfolioID().String(), SnapshotID: value.Snapshot.ID().String(),
		SnapshotChecksum: value.SnapshotChecksum.String(), ParentSnapshotID: value.ParentSnapshotID.String(),
		ParentChecksum: value.ParentChecksum.String(), TriggerID: value.TriggerID.String(),
		ProposalID: value.ProposalID.String(), DecisionID: value.DecisionID.String(),
		ReservationID: value.ReservationID.String(), Revision: uint64(value.Snapshot.Revision()),
	})
	if err != nil {
		return PortfolioCheckpoint{}, ErrCorruptPortfolioCheckpoint
	}
	checksum, _ := portfoliomodel.NewStateChecksum(raw)
	if !value.CheckpointChecksum.IsZero() && value.CheckpointChecksum != checksum {
		return PortfolioCheckpoint{}, ErrCorruptPortfolioCheckpoint
	}
	value.CheckpointChecksum = checksum
	value.canonical = raw
	return value, nil
}

func (value PortfolioCheckpoint) CanonicalJSON() []byte {
	return append([]byte(nil), value.canonical...)
}

type PortfolioDecisionPublication struct {
	TriggerID           riskmodel.DecisionTriggerID
	PortfolioID         portfoliomodel.PortfolioID
	ExpectedSnapshotID  portfoliomodel.PortfolioSnapshotID
	ExpectedRevision    portfoliomodel.PortfolioRevision
	ExpectedCheckpoint  portfoliomodel.StateChecksum
	Candidate           portfoliomodel.AllocationCandidate
	Evaluation          riskmodel.RiskEvaluation
	Decision            riskmodel.PortfolioRiskDecision
	Reservation         *portfoliomodel.CapitalReservation
	NextCheckpoint      PortfolioCheckpoint
	PublicationChecksum portfoliomodel.StateChecksum
	canonical           []byte
}

func NewPortfolioDecisionPublication(value PortfolioDecisionPublication) (PortfolioDecisionPublication, error) {
	if value.TriggerID.IsZero() || value.PortfolioID.IsZero() || value.ExpectedSnapshotID.IsZero() ||
		value.ExpectedRevision.Validate() != nil || value.ExpectedCheckpoint.IsZero() ||
		value.Candidate.ID().IsZero() || value.Evaluation.ID().IsZero() || value.Decision.ID().IsZero() ||
		value.NextCheckpoint.Snapshot.ID().IsZero() || value.NextCheckpoint.Snapshot.PortfolioID() != value.PortfolioID ||
		value.NextCheckpoint.Snapshot.Revision() != value.ExpectedRevision+1 ||
		value.Decision.Spec().ExpectedPortfolioRevision != value.ExpectedRevision ||
		value.Decision.Spec().PortfolioSnapshotID != value.ExpectedSnapshotID ||
		value.Decision.Spec().AllocationCandidate.ID() != value.Candidate.ID() ||
		value.Decision.Spec().RiskEvaluation.ID() != value.Evaluation.ID() ||
		value.NextCheckpoint.TriggerID != value.TriggerID ||
		value.NextCheckpoint.ProposalID != value.Candidate.ProposalID() ||
		value.NextCheckpoint.DecisionID != value.Decision.ID() ||
		value.NextCheckpoint.ParentSnapshotID != value.ExpectedSnapshotID ||
		value.NextCheckpoint.ParentChecksum != value.ExpectedCheckpoint {
		return PortfolioDecisionPublication{}, ErrInvalidPortfolioPublication
	}
	if value.Reservation != nil {
		if value.Reservation.Spec().PortfolioRevision != value.ExpectedRevision+1 ||
			value.NextCheckpoint.ReservationID != value.Reservation.ID() {
			return PortfolioDecisionPublication{}, ErrInvalidPortfolioPublication
		}
	} else if !value.NextCheckpoint.ReservationID.IsZero() {
		return PortfolioDecisionPublication{}, ErrInvalidPortfolioPublication
	}
	reservation := json.RawMessage("null")
	if value.Reservation != nil {
		reservation = value.Reservation.CanonicalJSON()
	}
	raw, err := json.Marshal(struct {
		TriggerID, PortfolioID, ExpectedSnapshotID, ExpectedCheckpoint string
		ExpectedRevision                                               uint64
		Candidate, Evaluation, Decision, Reservation, NextCheckpoint   json.RawMessage
	}{
		TriggerID: value.TriggerID.String(), PortfolioID: value.PortfolioID.String(),
		ExpectedSnapshotID: value.ExpectedSnapshotID.String(), ExpectedRevision: uint64(value.ExpectedRevision),
		ExpectedCheckpoint: value.ExpectedCheckpoint.String(), Candidate: value.Candidate.CanonicalJSON(),
		Evaluation: value.Evaluation.CanonicalJSON(), Decision: value.Decision.CanonicalJSON(),
		Reservation: reservation, NextCheckpoint: value.NextCheckpoint.CanonicalJSON(),
	})
	if err != nil {
		return PortfolioDecisionPublication{}, ErrInvalidPortfolioPublication
	}
	checksum, _ := portfoliomodel.NewStateChecksum(raw)
	if !value.PublicationChecksum.IsZero() && value.PublicationChecksum != checksum {
		return PortfolioDecisionPublication{}, ErrInvalidPortfolioPublication
	}
	value.PublicationChecksum = checksum
	value.canonical = raw
	return value, nil
}

func (value PortfolioDecisionPublication) CanonicalJSON() []byte {
	return append([]byte(nil), value.canonical...)
}

type RuntimePublicationStatus string

const (
	RuntimePublicationCommitted  RuntimePublicationStatus = "COMMITTED"
	RuntimePublicationIdempotent RuntimePublicationStatus = "IDEMPOTENT_REPLAY"
)

type RuntimePublicationReceipt struct {
	Status              RuntimePublicationStatus
	TriggerID           riskmodel.DecisionTriggerID
	DecisionID          riskmodel.PortfolioRiskDecisionID
	SnapshotID          portfoliomodel.PortfolioSnapshotID
	Revision            portfoliomodel.PortfolioRevision
	ReservationID       portfoliomodel.CapitalReservationID
	PublicationChecksum portfoliomodel.StateChecksum
}

type RuntimeRepository interface {
	InitializePortfolio(context.Context, PortfolioCheckpoint) (RegistrationOutcome, error)
	RestorePortfolioCheckpoint(context.Context, PortfolioCheckpoint) (RegistrationOutcome, error)
	CurrentPortfolioCheckpoint(context.Context, portfoliomodel.PortfolioID) (PortfolioCheckpoint, error)
	PortfolioCheckpoint(context.Context, portfoliomodel.PortfolioID, portfoliomodel.PortfolioRevision) (PortfolioCheckpoint, error)
	CommittedPublication(context.Context, riskmodel.DecisionTriggerID) (RuntimePublicationReceipt, error)
	PublishPortfolioDecision(context.Context, PortfolioDecisionPublication) (RuntimePublicationReceipt, error)
	Decision(context.Context, riskmodel.PortfolioRiskDecisionID) (riskmodel.PortfolioRiskDecision, error)
	Candidate(context.Context, portfoliomodel.AllocationCandidateID) (portfoliomodel.AllocationCandidate, error)
	Evaluation(context.Context, riskmodel.RiskEvaluationID) (riskmodel.RiskEvaluation, error)
	Reservation(context.Context, portfoliomodel.CapitalReservationID) (portfoliomodel.CapitalReservation, error)
}
