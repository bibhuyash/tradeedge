// Package tradingruntime composes the released TradeEdge domain runtimes. It
// owns scheduling and containment, never strategy, risk, OMS, or accounting
// authority.
package tradingruntime

import (
	"context"
	"errors"
	"time"

	"github.com/bibhuyash/tradeedge/internal/accounting/valuation"
	"github.com/bibhuyash/tradeedge/internal/domain"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/notification"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

var (
	ErrInvalid             = errors.New("invalid trading runtime configuration")
	ErrNotReady            = errors.New("trading runtime is not ready")
	ErrBackpressure        = errors.New("trading runtime admission capacity exhausted")
	ErrShutdown            = errors.New("trading runtime is draining or stopped")
	ErrRestoreRequired     = errors.New("authoritative restoration is incomplete")
	ErrCheckpointCorrupt   = errors.New("runtime checkpoint manifest is corrupt")
	ErrInvalidTransition   = errors.New("invalid runtime state transition")
	ErrExposureBlocked     = errors.New("new exposure is blocked")
	ErrUnsupportedLiveMode = errors.New("live trading is unavailable")
)

type Mode string

const (
	ModeOffline      Mode = "OFFLINE"
	ModePaper        Mode = "PAPER"
	ModeShadow       Mode = "SHADOW"
	ModeLiveDisabled Mode = "LIVE_DISABLED"
)

func (m Mode) Validate() error {
	switch m {
	case ModeOffline, ModePaper, ModeShadow, ModeLiveDisabled:
		return nil
	default:
		return ErrInvalid
	}
}

func (m Mode) permitsPipeline() bool { return m == ModePaper || m == ModeShadow }

type RuntimeState string

const (
	RuntimeStarting RuntimeState = "STARTING"
	RuntimeRunning  RuntimeState = "RUNNING"
	RuntimeDegraded RuntimeState = "DEGRADED"
	RuntimeHalted   RuntimeState = "HALTED"
	RuntimeDraining RuntimeState = "DRAINING"
	RuntimeStopped  RuntimeState = "STOPPED"
)

type ExposureEffect string

const (
	ExposureIncrease ExposureEffect = "INCREASE"
	ExposureReduce   ExposureEffect = "REDUCE"
)

type Proposal struct {
	StrategyID domain.StrategyID
	Value      strategymodel.TradeProposal
	Effect     ExposureEffect
}

type StrategyStage interface {
	Evaluate(context.Context, marketmodel.Event, []StrategySnapshot) ([]Proposal, error)
}

type RiskStage interface {
	Decide(context.Context, Proposal) (riskmodel.PortfolioRiskDecision, error)
	UpdateFinancial(context.Context, valuation.PortfolioFinancialSnapshot) error
}

type ExecutionResult struct {
	Plan  executionmodel.OrderPlan
	Fills []executionmodel.Fill
}

type ExecutionStage interface {
	Execute(context.Context, riskmodel.PortfolioRiskDecision) (ExecutionResult, error)
	Health(context.Context) Dependency
}

type AccountingStage interface {
	Ingest(context.Context, ExecutionResult) (valuation.PortfolioFinancialSnapshot, error)
	Health(context.Context) Dependency
}

type Restorer interface {
	Restore(context.Context, CheckpointManifest) error
}

type Checkpointer interface {
	Checkpoint(context.Context) (CheckpointManifest, error)
}

type Drainer interface {
	Drain(context.Context) error
}

type Shutdowner interface {
	Shutdown(context.Context) error
}

// OperationalObserver receives committed operational facts. Implementations
// are best-effort and must never participate in trading authority.
type OperationalObserver interface{ Observe(notification.Event) }

type ControlSnapshot struct {
	GlobalBlocked    bool
	Portfolios       map[portfoliomodel.PortfolioID]bool
	Strategies       map[domain.StrategyID]bool
	CircuitOpen      map[string]bool
	EvidenceRevision string
}

type ControlSource interface {
	Controls(context.Context) (ControlSnapshot, error)
}

type PipelineDependencies struct {
	Strategy     StrategyStage
	Risk         RiskStage
	Execution    ExecutionStage
	Accounting   AccountingStage
	Controls     ControlSource
	Restorer     Restorer
	Checkpointer Checkpointer
	Drainer      Drainer
	Shutdowner   Shutdowner
	Observer     OperationalObserver
}

type Config struct {
	Mode             Mode
	Exchange         domain.Exchange
	AdmissionLimit   int
	OperationTimeout time.Duration
	DrainTimeout     time.Duration
}

func DefaultConfig() Config {
	return Config{Mode: ModePaper, Exchange: domain.ExchangeNSE, AdmissionLimit: 32, OperationTimeout: time.Second, DrainTimeout: 10 * time.Second}
}

type EventOutcome string

const (
	OutcomeCompleted          EventOutcome = "COMPLETED"
	OutcomeNoProposal         EventOutcome = "NO_PROPOSAL"
	OutcomeRestricted         EventOutcome = "RESTRICTED"
	OutcomeRiskRejected       EventOutcome = "RISK_REJECTED"
	OutcomeFailed             EventOutcome = "FAILED"
	OutcomeDuplicateCommitted EventOutcome = "DUPLICATE_COMMITTED"
)

type EventReceipt struct {
	EventID            string       `json:"event_id"`
	Outcome            EventOutcome `json:"outcome"`
	ProposalCount      int          `json:"proposal_count"`
	DecisionCount      int          `json:"decision_count"`
	PlanCount          int          `json:"plan_count"`
	FillCount          int          `json:"fill_count"`
	FinancialRevisions []uint64     `json:"financial_revisions"`
}
