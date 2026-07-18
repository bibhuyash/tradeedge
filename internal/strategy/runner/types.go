package runner

import (
	"context"
	"errors"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
	strategystorage "github.com/bibhuyash/tradeedge/internal/strategy/storage"
)

type Outcome string

const (
	OutcomeStarted             Outcome = "STARTED"
	OutcomeNoAction            Outcome = "COMMITTED_NO_ACTION"
	OutcomeObservation         Outcome = "COMMITTED_OBSERVATION"
	OutcomeTradeProposal       Outcome = "COMMITTED_TRADE_PROPOSAL"
	OutcomeReadinessBlocked    Outcome = "READINESS_BLOCKED"
	OutcomeDuplicateCommitted  Outcome = "DUPLICATE_COMMITTED"
	OutcomeDuplicateInProgress Outcome = "DUPLICATE_IN_PROGRESS"
	OutcomeInstanceBusy        Outcome = "INSTANCE_BUSY"
	OutcomeLifecycleIneligible Outcome = "LIFECYCLE_INELIGIBLE"
	OutcomeTimedOut            Outcome = "TIMED_OUT"
	OutcomeCancelled           Outcome = "CANCELLED"
	OutcomePanic               Outcome = "STRATEGY_PANIC"
	OutcomeInvalid             Outcome = "INVALID_OUTPUT"
	OutcomeRevisionConflict    Outcome = "REVISION_CONFLICT"
	OutcomePublicationFailure  Outcome = "PUBLICATION_FAILURE"
	OutcomeInternalFailure     Outcome = "INTERNAL_FAILURE"
	OutcomeShutdown            Outcome = "SHUTDOWN"
)

var (
	ErrReadinessBlocked = errors.New("strategy evaluation blocked by market-data readiness")
	ErrDuplicateTrigger = errors.New("duplicate strategy trigger")
	ErrInstanceBusy     = errors.New("strategy instance evaluation already running")
	ErrLifecycle        = errors.New("strategy lifecycle is ineligible")
	ErrStrategyTimeout  = errors.New("strategy evaluation timed out")
	ErrStrategyPanic    = errors.New("strategy evaluation panicked")
	ErrInvalidOutput    = errors.New("invalid strategy output")
	ErrRunnerShutdown   = errors.New("strategy runner is shut down")
)

type Receipt struct {
	Outcome       Outcome
	TriggerID     strategymodel.TriggerID
	EvaluationID  strategymodel.EvaluationID
	ProposalID    strategymodel.ProposalID
	StateRevision uint64
	Reasons       []string
	Diagnostic    string
}

type Clock interface{ Now() time.Time }

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }

type ReadinessGate interface {
	Evidence(context.Context, strategymodel.StrategyInstance, strategymodel.CandleFrame) strategymodel.ReadinessEvidence
}

type Repository interface {
	strategystorage.InstanceRepository
	strategystorage.CheckpointRepository
	strategystorage.EvaluationRecordRepository
	strategystorage.EvaluationPublisher
}

type Config struct {
	MaxConcurrency int
	Timeout        time.Duration
}

func DefaultConfig() Config {
	return Config{MaxConcurrency: 4, Timeout: 100 * time.Millisecond}
}

type Failure struct {
	At         time.Time
	InstanceID domain.StrategyID
	Outcome    Outcome
	Diagnostic string
}
