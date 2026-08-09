package tradingruntime

import (
	"context"
	"errors"
	"sync"
	"time"

	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
)

type RuntimeSnapshot struct {
	Mode       Mode               `json:"mode"`
	State      RuntimeState       `json:"state"`
	Session    SessionSnapshot    `json:"session"`
	Readiness  ReadinessSnapshot  `json:"readiness"`
	Strategies []StrategySnapshot `json:"strategies"`
	Controls   ControlSummary     `json:"controls"`
	InFlight   int                `json:"in_flight"`
	Capacity   int                `json:"capacity"`
	Restored   bool               `json:"restored"`
	LastError  string             `json:"last_error,omitempty"`
}

type ControlSummary struct {
	GlobalBlocked       bool   `json:"global_blocked"`
	BlockedPortfolios   int    `json:"blocked_portfolios"`
	BlockedStrategies   int    `json:"blocked_strategies"`
	OpenCircuitBreakers int    `json:"open_circuit_breakers"`
	EvidenceRevision    string `json:"evidence_revision,omitempty"`
}

type Runtime struct {
	config     Config
	session    *SessionCoordinator
	strategies *StrategyManager
	deps       PipelineDependencies
	clock      func() time.Time
	semaphore  chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	state      RuntimeState
	readiness  ReadinessSnapshot
	controls   ControlSnapshot
	restored   bool
	inFlight   int
	lastError  string
	wait       sync.WaitGroup
	stopOnce   sync.Once
}

func New(config Config, session *SessionCoordinator, strategies *StrategyManager, deps PipelineDependencies, clock func() time.Time) (*Runtime, error) {
	if config.Mode.Validate() != nil || config.Exchange == "" || config.AdmissionLimit <= 0 || config.AdmissionLimit > 1024 || config.OperationTimeout <= 0 || config.DrainTimeout <= 0 || session == nil || strategies == nil || deps.Strategy == nil || deps.Risk == nil || deps.Execution == nil || deps.Accounting == nil || deps.Controls == nil || deps.Restorer == nil || deps.Checkpointer == nil {
		return nil, ErrInvalid
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runtime{config: config, session: session, strategies: strategies, deps: deps, clock: clock, semaphore: make(chan struct{}, config.AdmissionLimit), ctx: ctx, cancel: cancel, state: RuntimeStarting}, nil
}

func (r *Runtime) Start(ctx context.Context, manifest CheckpointManifest, dependencies []Dependency) error {
	if manifest.Verify() != nil || manifest.Mode != r.config.Mode || manifest.CalendarVersion != r.session.Snapshot().CalendarVersion {
		r.fail(RuntimeHalted, ErrCheckpointCorrupt)
		return ErrCheckpointCorrupt
	}
	if err := r.deps.Restorer.Restore(ctx, manifest); err != nil {
		r.fail(RuntimeHalted, err)
		return errors.Join(ErrRestoreRequired, err)
	}
	controls, err := r.deps.Controls.Controls(ctx)
	if err != nil {
		r.fail(RuntimeHalted, err)
		return err
	}
	readiness := AggregateReadiness(r.config.Mode, append(dependencies, r.deps.Execution.Health(ctx), r.deps.Accounting.Health(ctx)), r.clock())
	r.mu.Lock()
	r.restored, r.controls, r.readiness = true, controls, readiness
	if readiness.Ready {
		r.state = RuntimeRunning
	} else {
		r.state = RuntimeDegraded
	}
	r.mu.Unlock()
	if readiness.Ready {
		_, _ = r.session.Advance(ctx, r.clock(), true)
	} else {
		_, _ = r.session.Degrade(r.clock(), "READINESS_LOST")
	}
	r.strategies.Reconcile(r.session.Snapshot(), readiness, controls, r.clock())
	if !readiness.Ready {
		return ErrNotReady
	}
	return nil
}

func (r *Runtime) Refresh(ctx context.Context, dependencies []Dependency) ReadinessSnapshot {
	controls, err := r.deps.Controls.Controls(ctx)
	if err != nil {
		controls = ControlSnapshot{GlobalBlocked: true}
	}
	readiness := AggregateReadiness(r.config.Mode, append(dependencies, r.deps.Execution.Health(ctx), r.deps.Accounting.Health(ctx)), r.clock())
	if controls.GlobalBlocked {
		readiness.Ready, readiness.State, readiness.Reasons = false, HealthBlocked, append(readiness.Reasons, "GLOBAL_CONTROL_BLOCKED")
	}
	r.mu.Lock()
	r.controls, r.readiness = controls, readiness
	if r.state != RuntimeDraining && r.state != RuntimeStopped && r.state != RuntimeHalted {
		if readiness.Ready {
			r.state = RuntimeRunning
		} else {
			r.state = RuntimeDegraded
		}
	}
	r.mu.Unlock()
	if readiness.Ready {
		_, _ = r.session.Advance(ctx, r.clock(), true)
	} else {
		_, _ = r.session.Degrade(r.clock(), "READINESS_LOST")
	}
	r.strategies.Reconcile(r.session.Snapshot(), readiness, controls, r.clock())
	return readiness
}

func (r *Runtime) Process(ctx context.Context, event marketmodel.Event) (EventReceipt, error) {
	if event == nil || event.ID().IsZero() {
		return EventReceipt{}, ErrInvalid
	}
	r.mu.Lock()
	if !r.restored {
		r.mu.Unlock()
		return EventReceipt{}, ErrRestoreRequired
	}
	if r.state == RuntimeDraining || r.state == RuntimeStopped {
		r.mu.Unlock()
		return EventReceipt{}, ErrShutdown
	}
	if r.state != RuntimeRunning || !r.readiness.Ready {
		r.mu.Unlock()
		return EventReceipt{}, ErrNotReady
	}
	r.mu.Unlock()
	select {
	case r.semaphore <- struct{}{}:
	case <-ctx.Done():
		return EventReceipt{}, ctx.Err()
	case <-r.ctx.Done():
		return EventReceipt{}, ErrShutdown
	default:
		r.fail(RuntimeDegraded, ErrBackpressure)
		_, _ = r.session.Degrade(r.clock(), "BACKPRESSURE")
		return EventReceipt{}, ErrBackpressure
	}
	r.mu.Lock()
	r.inFlight++
	r.wait.Add(1)
	r.mu.Unlock()
	defer func() { <-r.semaphore; r.mu.Lock(); r.inFlight--; r.wait.Done(); r.mu.Unlock() }()
	opctx, cancel := context.WithTimeout(ctx, r.config.OperationTimeout)
	defer cancel()
	stopCancellation := context.AfterFunc(r.ctx, cancel)
	defer stopCancellation()
	receipt := EventReceipt{EventID: event.ID().String()}
	proposals, err := r.deps.Strategy.Evaluate(opctx, event, r.strategies.Snapshots())
	if err != nil {
		r.fail(RuntimeDegraded, err)
		receipt.Outcome = OutcomeFailed
		return receipt, err
	}
	receipt.ProposalCount = len(proposals)
	if len(proposals) == 0 {
		receipt.Outcome = OutcomeNoProposal
		return receipt, nil
	}
	strategyByID := map[string]StrategySnapshot{}
	for _, value := range r.strategies.Snapshots() {
		strategyByID[value.Strategy] = value
	}
	for _, proposal := range proposals {
		strategy, found := strategyByID[string(proposal.StrategyID)]
		if !found || !strategyAllows(strategy, proposal.Effect, r.session.Snapshot().Regime) {
			receipt.Outcome = OutcomeRestricted
			continue
		}
		if proposal.Effect == ExposureIncrease && r.controls.GlobalBlocked {
			return receipt, ErrExposureBlocked
		}
		decision, decideErr := r.deps.Risk.Decide(opctx, proposal)
		if decideErr != nil {
			r.fail(RuntimeDegraded, decideErr)
			return receipt, decideErr
		}
		receipt.DecisionCount++
		decisionOutcome := decision.Spec().Outcome
		if decisionOutcome != riskmodel.DecisionApproved && decisionOutcome != riskmodel.DecisionModified {
			receipt.Outcome = OutcomeRiskRejected
			continue
		}
		if proposal.Effect == ExposureIncrease && r.controls.Portfolios[decision.Spec().PortfolioID] {
			return receipt, ErrExposureBlocked
		}
		execution, executeErr := r.deps.Execution.Execute(opctx, decision)
		if executeErr != nil {
			r.fail(RuntimeDegraded, executeErr)
			return receipt, executeErr
		}
		receipt.PlanCount++
		receipt.FillCount += len(execution.Fills)
		if len(execution.Fills) == 0 {
			continue
		}
		financial, accountingErr := r.deps.Accounting.Ingest(opctx, execution)
		if accountingErr != nil {
			r.fail(RuntimeHalted, accountingErr)
			_, _ = r.session.Halt(r.clock(), "ACCOUNTING_FAILURE")
			return receipt, accountingErr
		}
		receipt.FinancialRevisions = append(receipt.FinancialRevisions, financial.Revision)
		if feedbackErr := r.deps.Risk.UpdateFinancial(opctx, financial); feedbackErr != nil {
			r.fail(RuntimeDegraded, feedbackErr)
			return receipt, feedbackErr
		}
	}
	if receipt.Outcome == "" {
		receipt.Outcome = OutcomeCompleted
	}
	return receipt, nil
}

func (r *Runtime) Snapshot() RuntimeSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return RuntimeSnapshot{Mode: r.config.Mode, State: r.state, Session: r.session.Snapshot(), Readiness: r.readiness, Strategies: r.strategies.Snapshots(), Controls: summarizeControls(r.controls), InFlight: r.inFlight, Capacity: cap(r.semaphore), Restored: r.restored, LastError: r.lastError}
}

func summarizeControls(value ControlSnapshot) ControlSummary {
	result := ControlSummary{GlobalBlocked: value.GlobalBlocked, EvidenceRevision: value.EvidenceRevision}
	for _, blocked := range value.Portfolios {
		if blocked {
			result.BlockedPortfolios++
		}
	}
	for _, blocked := range value.Strategies {
		if blocked {
			result.BlockedStrategies++
		}
	}
	for _, open := range value.CircuitOpen {
		if open {
			result.OpenCircuitBreakers++
		}
	}
	return result
}

func (r *Runtime) Checkpoint(ctx context.Context) (CheckpointManifest, error) {
	return r.deps.Checkpointer.Checkpoint(ctx)
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	var result error
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.state = RuntimeDraining
		r.mu.Unlock()
		_, _ = r.session.Drain(r.clock())
		r.strategies.Stop(r.clock())
		r.cancel()
		drainCtx, cancel := context.WithTimeout(ctx, r.config.DrainTimeout)
		defer cancel()
		done := make(chan struct{})
		go func() { r.wait.Wait(); close(done) }()
		select {
		case <-done:
		case <-drainCtx.Done():
			result = drainCtx.Err()
		}
		if result == nil && r.deps.Drainer != nil {
			result = r.deps.Drainer.Drain(drainCtx)
		}
		if result == nil {
			if manifest, checkpointErr := r.deps.Checkpointer.Checkpoint(drainCtx); checkpointErr != nil {
				result = checkpointErr
			} else if manifest.Verify() != nil {
				result = ErrCheckpointCorrupt
			}
		}
		if r.deps.Shutdowner != nil {
			result = errors.Join(result, r.deps.Shutdowner.Shutdown(drainCtx))
		}
		r.mu.Lock()
		r.state = RuntimeStopped
		r.mu.Unlock()
		_, _ = r.session.Stop(r.clock())
	})
	return result
}

func (r *Runtime) fail(state RuntimeState, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != RuntimeDraining && r.state != RuntimeStopped {
		r.state, r.lastError = state, err.Error()
	}
}
