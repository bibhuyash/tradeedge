package runner

import (
	"context"
	"errors"
	"time"

	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	portfolioallocation "github.com/bibhuyash/tradeedge/internal/portfolio/allocation"
	portfolioconfig "github.com/bibhuyash/tradeedge/internal/portfolio/config"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	riskstorage "github.com/bibhuyash/tradeedge/internal/risk/storage"
	risktelemetry "github.com/bibhuyash/tradeedge/internal/risk/telemetry"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

type Outcome string

const (
	OutcomeApproved            Outcome = "COMMITTED_APPROVED"
	OutcomeModified            Outcome = "COMMITTED_MODIFIED"
	OutcomeRejected            Outcome = "COMMITTED_REJECTED"
	OutcomeDeferred            Outcome = "COMMITTED_DEFERRED"
	OutcomeDuplicateCommitted  Outcome = "DUPLICATE_COMMITTED"
	OutcomeDuplicateInProgress Outcome = "DUPLICATE_IN_PROGRESS"
	OutcomePortfolioBusy       Outcome = "PORTFOLIO_BUSY"
	OutcomeRevisionConflict    Outcome = "REVISION_CONFLICT"
	OutcomeAllocationFailure   Outcome = "ALLOCATION_FAILURE"
	OutcomeRuleFailure         Outcome = "RULE_FAILURE"
	OutcomeInvalidOutput       Outcome = "INVALID_OUTPUT"
	OutcomeTimedOut            Outcome = "TIMED_OUT"
	OutcomeCancelled           Outcome = "CANCELLED"
	OutcomePanic               Outcome = "PANIC"
	OutcomePublicationFailure  Outcome = "PUBLICATION_FAILURE"
	OutcomeShutdown            Outcome = "SHUTDOWN"
)

var (
	ErrDuplicateInProgress = errors.New("portfolio decision already in progress")
	ErrPortfolioBusy       = errors.New("portfolio has another decision in progress")
	ErrAllocation          = errors.New("allocation evaluation failed")
	ErrRule                = errors.New("risk rule evaluation failed")
	ErrInvalidOutput       = errors.New("invalid portfolio risk output")
	ErrTimeout             = errors.New("portfolio risk evaluation timed out")
	ErrPanic               = errors.New("portfolio risk evaluation panicked")
	ErrShutdown            = errors.New("portfolio risk runner is shut down")
)

type Request struct {
	PortfolioID             portfoliomodel.PortfolioID
	ProposalID              strategymodel.ProposalID
	ExpectedRevision        portfoliomodel.PortfolioRevision
	RiskPolicyID            riskmodel.RiskPolicyID
	InstrumentMasterVersion instrumentmaster.Version
	LogicalTime             time.Time
}

type Receipt struct {
	Outcome             Outcome
	TriggerID           riskmodel.DecisionTriggerID
	ProposalID          strategymodel.ProposalID
	PortfolioID         portfoliomodel.PortfolioID
	ExpectedRevision    portfoliomodel.PortfolioRevision
	CommittedRevision   portfoliomodel.PortfolioRevision
	DecisionID          riskmodel.PortfolioRiskDecisionID
	SnapshotID          portfoliomodel.PortfolioSnapshotID
	ReservationID       portfoliomodel.CapitalReservationID
	PublicationChecksum portfoliomodel.StateChecksum
	Diagnostic          string
}

type Config struct {
	MaxConcurrency int
	Timeout        time.Duration
}

func DefaultConfig() Config { return Config{MaxConcurrency: 4, Timeout: 100 * time.Millisecond} }

type ProposalSource interface {
	Proposal(context.Context, strategymodel.ProposalID) (strategymodel.TradeProposal, error)
}
type PortfolioConfigurationSource interface {
	Configuration(context.Context, portfoliomodel.PortfolioConfigurationID) (portfolioconfig.PortfolioConfiguration, error)
	AllocationPolicy(context.Context, portfoliomodel.AllocationPolicyID) (portfolioconfig.AllocationPolicy, error)
}
type RiskPolicySource interface {
	Policy(context.Context, riskmodel.RiskPolicyID) (riskmodel.RiskPolicy, error)
}
type MasterSource interface {
	Get(context.Context, instrumentmaster.Version) (instrumentmaster.Master, error)
}
type Allocator interface {
	Evaluate(portfolioallocation.Input) (portfoliomodel.AllocationCandidate, error)
}

type Dependencies struct {
	Proposals ProposalSource
	Portfolio PortfolioConfigurationSource
	Policies  RiskPolicySource
	Masters   MasterSource
	Runtime   riskstorage.RuntimeRepository
	Telemetry risktelemetry.Recorder
}
