package tradingruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/accounting/valuation"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/notification"
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
	NewExposureBlocked  bool   `json:"new_exposure_blocked"`
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
	r.emitReadiness("start", readiness, r.clock())
	r.emitControls(controls, r.clock())
	if !readiness.Ready {
		return ErrNotReady
	}
	return nil
}

func (r *Runtime) Refresh(ctx context.Context, dependencies []Dependency) ReadinessSnapshot {
	r.mu.Lock()
	previousReady := r.readiness.Ready
	previousControls := r.controls
	r.mu.Unlock()
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
	if previousReady != readiness.Ready {
		r.emitReadiness("refresh", readiness, r.clock())
	}
	if previousControls.EvidenceRevision != controls.EvidenceRevision {
		r.emitControls(controls, r.clock())
	}
	r.emitEndOfDay()
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
	r.emitSession(event)
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
		instrument := ""
		if legs := proposal.Value.Draft().Legs; len(legs) > 0 {
			instrument = legs[0].InstrumentID.String()
		}
		r.emit(notification.EventSpec{SourceID: proposal.Value.ID().String(), OccurredAt: proposal.Value.Metadata().GeneratedAt, Category: notification.CategoryProposal, Kind: notification.KindProposalGenerated, Severity: notification.SeverityInfo, Details: notification.Details{StrategyID: string(proposal.StrategyID), InstrumentID: instrument, ReferenceID: proposal.Value.ID().String()}}, event)
		strategy, found := strategyByID[string(proposal.StrategyID)]
		if !found || !strategyAllows(strategy, proposal.Effect, r.session.Snapshot().Regime) {
			receipt.Outcome = OutcomeRestricted
			r.emit(notification.EventSpec{SourceID: proposal.Value.ID().String() + "|restricted", OccurredAt: r.clock(), Category: notification.CategoryStrategy, Kind: notification.KindStrategyRestricted, Severity: notification.SeverityWarning, Details: notification.Details{StrategyID: string(proposal.StrategyID), Reason: "SESSION_OR_CAS_RESTRICTED"}}, event)
			continue
		}
		if proposal.Effect == ExposureIncrease && (r.controls.GlobalBlocked || r.controls.NewExposureBlocked) {
			return receipt, ErrExposureBlocked
		}
		decision, decideErr := r.deps.Risk.Decide(opctx, proposal)
		if decideErr != nil {
			r.fail(RuntimeDegraded, decideErr)
			return receipt, decideErr
		}
		receipt.DecisionCount++
		decisionOutcome := decision.Spec().Outcome
		kind, severity := notification.KindRiskRejected, notification.SeverityWarning
		if decisionOutcome == riskmodel.DecisionApproved {
			kind, severity = notification.KindRiskApproved, notification.SeverityInfo
		} else if decisionOutcome == riskmodel.DecisionModified {
			kind = notification.KindRiskModified
		}
		r.emit(notification.EventSpec{SourceID: decision.ID().String(), OccurredAt: decision.Spec().GeneratedAt, Category: notification.CategoryRisk, Kind: kind, Severity: severity, Details: notification.Details{StrategyID: string(proposal.StrategyID), InstrumentID: instrument, PortfolioID: decision.Spec().PortfolioID.String(), Reason: string(decision.Spec().PrimaryReason), ReferenceID: decision.ID().String()}}, event)
		if decisionOutcome != riskmodel.DecisionApproved && decisionOutcome != riskmodel.DecisionModified {
			receipt.Outcome = OutcomeRiskRejected
			continue
		}
		if proposal.Effect == ExposureIncrease && (r.controls.NewExposureBlocked || r.controls.Portfolios[decision.Spec().PortfolioID]) {
			return receipt, ErrExposureBlocked
		}
		execution, executeErr := r.deps.Execution.Execute(opctx, decision)
		if executeErr != nil {
			r.fail(RuntimeDegraded, executeErr)
			return receipt, executeErr
		}
		receipt.PlanCount++
		execKind := notification.KindPaperSubmitted
		if r.config.Mode == ModeShadow {
			execKind = notification.KindShadowTrade
		}
		r.emit(notification.EventSpec{SourceID: execution.Plan.ID().String(), OccurredAt: execution.Plan.Spec().CreatedAt, Category: notification.CategoryExecution, Kind: execKind, Severity: notification.SeverityInfo, Details: notification.Details{StrategyID: string(proposal.StrategyID), InstrumentID: instrument, ReferenceID: execution.Plan.ID().String()}}, event)
		receipt.FillCount += len(execution.Fills)
		for _, fill := range execution.Fills {
			spec := fill.Spec()
			r.emit(notification.EventSpec{SourceID: fill.ID().String(), OccurredAt: spec.OccurredAt, Category: notification.CategoryExecution, Kind: notification.KindPaperFill, Severity: notification.SeverityInfo, Details: notification.Details{StrategyID: string(proposal.StrategyID), InstrumentID: instrument, ReferenceID: fill.ID().String(), Quantity: spec.Quantity.Int64(), PriceMinor: spec.Price.MinorUnits(), Currency: spec.Price.Currency().String()}}, event)
		}
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
		r.emitFinancial(financial, event)
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

func (r *Runtime) emit(spec notification.EventSpec, source marketmodel.Event) {
	if r.deps.Observer == nil {
		return
	}
	location, _ := time.LoadLocation("Asia/Kolkata")
	at := spec.OccurredAt
	if at.IsZero() {
		at = r.clock()
	}
	local := at.In(location)
	spec.TradingDate = fmt.Sprintf("%04d-%02d-%02d", local.Year(), local.Month(), local.Day())
	spec.Mode = string(r.config.Mode)
	value, err := notification.NewEvent(spec)
	if err != nil {
		return
	}
	defer func() { _ = recover() }()
	r.deps.Observer.Observe(value)
}
func (r *Runtime) emitReadiness(source string, value ReadinessSnapshot, at time.Time) {
	kind, severity := notification.KindReadinessLost, notification.SeverityWarning
	if value.Ready {
		kind, severity = notification.KindReadinessRestored, notification.SeverityInfo
	}
	reason := ""
	if len(value.Reasons) > 0 {
		reason = value.Reasons[0]
	}
	r.emit(notification.EventSpec{SourceID: fmt.Sprintf("readiness|%s|%s|%d", source, value.State, at.UnixNano()), OccurredAt: at, Category: notification.CategoryReadiness, Kind: kind, Severity: severity, Details: notification.Details{State: string(value.State), Reason: reason}}, nil)
}
func (r *Runtime) emitControls(value ControlSnapshot, at time.Time) {
	if value.GlobalBlocked {
		r.emit(notification.EventSpec{SourceID: "kill|" + value.EvidenceRevision, OccurredAt: at, Category: notification.CategoryControl, Kind: notification.KindKillSwitch, Severity: notification.SeverityCritical, Details: notification.Details{Reason: "GLOBAL_CONTROL_BLOCKED"}}, nil)
	}
	for id, open := range value.CircuitOpen {
		if open {
			r.emit(notification.EventSpec{SourceID: "circuit|" + value.EvidenceRevision + "|" + id, OccurredAt: at, Category: notification.CategoryControl, Kind: notification.KindCircuitBreaker, Severity: notification.SeverityWarning, Details: notification.Details{Subject: id}}, nil)
		}
	}
}
func (r *Runtime) emitSession(source marketmodel.Event) {
	snapshot := r.session.Snapshot()
	if len(snapshot.Transitions) == 0 {
		return
	}
	transition := snapshot.Transitions[len(snapshot.Transitions)-1]
	var kind notification.Kind
	switch transition.To {
	case SessionPreCAS:
		kind = notification.KindPreCAS
	case SessionCASActive:
		kind = notification.KindCASActive
	case SessionPostCAS:
		kind = notification.KindPostCAS
	default:
		return
	}
	instrument := ""
	price := int64(0)
	currency := ""
	if source != nil {
		instrument = source.InstrumentID().String()
		if quote, ok := source.(marketmodel.QuoteEvent); ok {
			price = quote.LastPrice().MinorUnits()
			currency = quote.LastPrice().Currency().String()
		}
	}
	strategies := r.strategies.Snapshots()
	configSum := sha256.Sum256([]byte(fmt.Sprintf("runtime/v1|%s|%s", r.config.Mode, r.config.Exchange)))
	configChecksum := hex.EncodeToString(configSum[:])
	if len(strategies) == 0 {
		r.emit(notification.EventSpec{SourceID: fmt.Sprintf("session|%s|%s|%s", transition.CalendarVersion, transition.To, transition.At.Format(time.RFC3339Nano)), OccurredAt: transition.At, Category: notification.CategoryCAS, Kind: kind, Severity: notification.SeverityInfo, Details: notification.Details{State: string(transition.To), Reason: transition.Reason, InstrumentID: instrument, PriceMinor: price, Currency: currency, CalendarVersion: transition.CalendarVersion, ConfigurationVersion: "runtime/v1", ConfigurationChecksum: configChecksum}}, source)
		return
	}
	for _, strategy := range strategies {
		r.emit(notification.EventSpec{SourceID: fmt.Sprintf("session|%s|%s|%s|%s", transition.CalendarVersion, transition.To, transition.At.Format(time.RFC3339Nano), strategy.Strategy), OccurredAt: transition.At, Category: notification.CategoryCAS, Kind: kind, Severity: notification.SeverityInfo, Details: notification.Details{State: string(strategy.State), Reason: transition.Reason, StrategyID: strategy.Strategy, InstrumentID: instrument, PriceMinor: price, Currency: currency, CalendarVersion: transition.CalendarVersion, ConfigurationVersion: "runtime/v1", ConfigurationChecksum: configChecksum}}, source)
	}
}

func (r *Runtime) emitEndOfDay() {
	snapshot := r.session.Snapshot()
	if snapshot.State != SessionClosed || len(snapshot.Transitions) == 0 {
		return
	}
	transition := snapshot.Transitions[len(snapshot.Transitions)-1]
	r.emit(notification.EventSpec{SourceID: fmt.Sprintf("eod|%s|%s", transition.CalendarVersion, transition.At.Format(time.RFC3339Nano)), OccurredAt: transition.At, Category: notification.CategoryReporting, Kind: notification.KindEndOfDay, Severity: notification.SeverityInfo, Details: notification.Details{State: "FINAL"}}, nil)
}

func (r *Runtime) emitFinancial(value valuation.PortfolioFinancialSnapshot, source marketmodel.Event) {
	kind, severity := notification.KindFinancialSnapshot, notification.SeverityInfo
	switch value.Status {
	case valuation.StatusPartial:
		kind, severity = notification.KindValuationPartial, notification.SeverityWarning
	case valuation.StatusStale:
		kind, severity = notification.KindValuationStale, notification.SeverityWarning
	case valuation.StatusUnavailable:
		kind, severity = notification.KindValuationUnavailable, notification.SeverityWarning
	}
	sourceID := value.Checksum.String()
	if value.Checksum.IsZero() {
		sourceID = fmt.Sprintf("financial|%s|%d", source.ID(), value.Revision)
	}
	reason := ""
	if len(value.Reasons) > 0 {
		reason = string(value.Reasons[0])
	}
	details := notification.Details{PortfolioID: value.PortfolioID.String(), State: string(value.Status), Reason: reason, ValuationStatus: string(value.Status), FinancialChecksum: value.Checksum.String(), Currency: value.Currency.String(), RealizedAvailability: string(value.RealizedPnL.Availability), UnrealizedAvailability: string(value.UnrealizedPnL.Availability), TotalAvailability: string(value.TotalPnL.Availability)}
	if value.RealizedPnL.Known() {
		details.RealizedMinor = value.RealizedPnL.Value.MinorUnits()
	}
	if value.UnrealizedPnL.Known() {
		details.UnrealizedMinor = value.UnrealizedPnL.Value.MinorUnits()
	}
	if value.TotalPnL.Known() {
		details.TotalMinor = value.TotalPnL.Value.MinorUnits()
	}
	r.emit(notification.EventSpec{SourceID: sourceID, OccurredAt: value.GeneratedAt, Category: notification.CategoryValuation, Kind: kind, Severity: severity, Details: details}, source)
}

func (r *Runtime) Snapshot() RuntimeSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return RuntimeSnapshot{Mode: r.config.Mode, State: r.state, Session: r.session.Snapshot(), Readiness: r.readiness, Strategies: r.strategies.Snapshots(), Controls: summarizeControls(r.controls), InFlight: r.inFlight, Capacity: cap(r.semaphore), Restored: r.restored, LastError: r.lastError}
}

func summarizeControls(value ControlSnapshot) ControlSummary {
	result := ControlSummary{NewExposureBlocked: value.NewExposureBlocked, GlobalBlocked: value.GlobalBlocked, EvidenceRevision: value.EvidenceRevision}
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
	changed := false
	if r.state != RuntimeDraining && r.state != RuntimeStopped {
		r.state, r.lastError = state, err.Error()
		changed = true
	}
	r.mu.Unlock()
	if changed {
		kind, severity := notification.KindRuntimeDegraded, notification.SeverityWarning
		if state == RuntimeHalted {
			kind, severity = notification.KindRuntimeHalted, notification.SeverityCritical
		}
		at := r.clock()
		r.emit(notification.EventSpec{SourceID: fmt.Sprintf("runtime|%s|%d", state, at.UnixNano()), OccurredAt: at, Category: notification.CategoryRuntime, Kind: kind, Severity: severity, Details: notification.Details{State: string(state), Reason: "RUNTIME_FAILURE"}}, nil)
	}
}
