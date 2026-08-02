package runner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	portfolioallocation "github.com/bibhuyash/tradeedge/internal/portfolio/allocation"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	"github.com/bibhuyash/tradeedge/internal/risk/rules"
	riskstorage "github.com/bibhuyash/tradeedge/internal/risk/storage"
	risktelemetry "github.com/bibhuyash/tradeedge/internal/risk/telemetry"
)

type Runner struct {
	deps      Dependencies
	allocator Allocator
	rules     *rules.Registry
	config    Config
	semaphore chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	running   map[portfoliomodel.PortfolioID]riskmodel.DecisionTriggerID
	closed    bool
	inFlight  atomic.Int64
	wait      sync.WaitGroup
	stopOnce  sync.Once
	stopped   chan struct{}
}

func New(deps Dependencies, allocator Allocator, registry *rules.Registry, config Config) (*Runner, error) {
	if deps.Proposals == nil || deps.Portfolio == nil || deps.Policies == nil || deps.Masters == nil ||
		deps.Runtime == nil || allocator == nil || registry == nil || config.MaxConcurrency <= 0 ||
		config.MaxConcurrency > 64 || config.Timeout <= 0 || config.Timeout > time.Minute {
		return nil, ErrInvalidOutput
	}
	ctx, cancel := context.WithCancel(context.Background())
	if deps.Telemetry == nil {
		deps.Telemetry = risktelemetry.NopRecorder{}
	}
	return &Runner{deps: deps, allocator: allocator, rules: registry, config: config,
		semaphore: make(chan struct{}, config.MaxConcurrency), ctx: ctx, cancel: cancel,
		running: make(map[portfoliomodel.PortfolioID]riskmodel.DecisionTriggerID),
		stopped: make(chan struct{})}, nil
}

// Health exposes bounded process-local runner state for read-only operations.
func (runner *Runner) Health() (closed bool, inFlight int, keyedPortfolios int, maximum int, timeout time.Duration) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.closed, int(runner.inFlight.Load()), len(runner.running),
		runner.config.MaxConcurrency, runner.config.Timeout
}

func (runner *Runner) EvaluateProposal(ctx context.Context, request Request) (receipt Receipt, err error) {
	started := time.Now()
	inFlightStarted := runner.inFlight.Add(1)
	defer func() {
		remaining := runner.inFlight.Add(-1)
		runner.deps.Telemetry.Record(risktelemetry.Event{Outcome: string(receipt.Outcome),
			Duration: time.Since(started), InFlight: int(remaining)})
	}()
	receipt = Receipt{ProposalID: request.ProposalID, PortfolioID: request.PortfolioID,
		ExpectedRevision: request.ExpectedRevision}
	runner.deps.Telemetry.Record(risktelemetry.Event{InFlight: int(inFlightStarted)})
	if err := ctx.Err(); err != nil {
		receipt.Outcome = OutcomeCancelled
		return receipt, err
	}
	if request.PortfolioID.IsZero() || request.ProposalID.IsZero() ||
		request.ExpectedRevision.Validate() != nil || request.RiskPolicyID.IsZero() ||
		request.InstrumentMasterVersion == "" || request.LogicalTime.IsZero() {
		return receipt, ErrInvalidOutput
	}
	proposal, err := runner.deps.Proposals.Proposal(ctx, request.ProposalID)
	if err != nil || proposal.ID() != request.ProposalID {
		return receipt, errOr(err, ErrInvalidOutput)
	}
	checkpoint, err := runner.deps.Runtime.PortfolioCheckpoint(ctx, request.PortfolioID, request.ExpectedRevision)
	if err != nil {
		current, currentErr := runner.deps.Runtime.CurrentPortfolioCheckpoint(ctx, request.PortfolioID)
		if currentErr == nil {
			receipt.Outcome = OutcomeRevisionConflict
			return receipt, &riskstorage.PortfolioRevisionConflictError{PortfolioID: request.PortfolioID,
				Expected: request.ExpectedRevision, Actual: current.Snapshot.Revision()}
		}
		return receipt, err
	}
	snapshotSpec := checkpoint.Snapshot.Spec()
	configuration, err := runner.deps.Portfolio.Configuration(ctx, snapshotSpec.ConfigurationID)
	if err != nil || configuration.Hash() != snapshotSpec.ConfigurationHash {
		return receipt, errOr(err, ErrInvalidOutput)
	}
	var allocationID portfoliomodel.AllocationPolicyID
	for _, value := range checkpoint.Snapshot.StrategyAllocations() {
		if value.Spec().InstanceID == proposal.Metadata().InstanceID {
			allocationID = value.Spec().PolicyID
			break
		}
	}
	allocationPolicy, err := runner.deps.Portfolio.AllocationPolicy(ctx, allocationID)
	if err != nil {
		return receipt, err
	}
	policy, err := runner.deps.Policies.Policy(ctx, request.RiskPolicyID)
	if err != nil {
		return receipt, err
	}
	master, err := runner.deps.Masters.Get(ctx, request.InstrumentMasterVersion)
	if err != nil {
		return receipt, err
	}
	trigger, err := riskmodel.NewDecisionTriggerID("portfolio-risk-runner/v1", proposal.ID().String(),
		checkpoint.Snapshot.ID().String(), fmt.Sprint(checkpoint.Snapshot.Revision()),
		configuration.Hash().String(), allocationPolicy.ConfigurationHash.String(),
		policy.ID().String(), policy.ConfigurationHash().String(), string(master.Version()),
		request.LogicalTime.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return receipt, err
	}
	receipt.TriggerID = trigger
	if committed, committedErr := runner.deps.Runtime.CommittedPublication(ctx, trigger); committedErr == nil {
		return receiptFromPublication(receipt, committed, OutcomeDuplicateCommitted), nil
	} else if !errors.Is(committedErr, riskstorage.ErrNotFound) {
		return receipt, committedErr
	}
	current, err := runner.deps.Runtime.CurrentPortfolioCheckpoint(ctx, request.PortfolioID)
	if err != nil {
		return receipt, err
	}
	if current.Snapshot.Revision() != request.ExpectedRevision || current.Snapshot.ID() != checkpoint.Snapshot.ID() ||
		current.CheckpointChecksum != checkpoint.CheckpointChecksum {
		receipt.Outcome = OutcomeRevisionConflict
		return receipt, &riskstorage.PortfolioRevisionConflictError{PortfolioID: request.PortfolioID,
			Expected: request.ExpectedRevision, Actual: current.Snapshot.Revision()}
	}
	if outcome := runner.reserve(request.PortfolioID, trigger); outcome != "" {
		receipt.Outcome = outcome
		if outcome == OutcomeShutdown {
			return receipt, ErrShutdown
		}
		if outcome == OutcomeDuplicateInProgress {
			return receipt, ErrDuplicateInProgress
		}
		return receipt, ErrPortfolioBusy
	}
	defer runner.release(request.PortfolioID)
	select {
	case runner.semaphore <- struct{}{}:
		defer func() { <-runner.semaphore }()
	case <-ctx.Done():
		receipt.Outcome = OutcomeCancelled
		return receipt, ctx.Err()
	case <-runner.ctx.Done():
		receipt.Outcome = OutcomeShutdown
		return receipt, ErrShutdown
	}

	evaluationCtx, cancel := context.WithTimeout(ctx, runner.config.Timeout)
	defer cancel()
	candidate, panicDiagnostic, err := invokeAllocation(runner.allocator, portfolioallocation.Input{
		Proposal: proposal, Snapshot: checkpoint.Snapshot, Policy: allocationPolicy,
		Master: master, LogicalTime: request.LogicalTime.UTC(),
	})
	if panicDiagnostic != "" {
		receipt.Outcome, receipt.Diagnostic = OutcomePanic, panicDiagnostic
		return receipt, ErrPanic
	}
	if err != nil {
		receipt.Outcome = OutcomeAllocationFailure
		return receipt, fmt.Errorf("%w: %v", ErrAllocation, err)
	}
	if err := evaluationCtx.Err(); err != nil {
		return timeoutReceipt(receipt, err)
	}

	results := make([]riskmodel.RuleResult, 0, len(policy.Rules()))
	technical := make([]riskmodel.TechnicalRuleError, 0)
	for _, configured := range policy.Rules() {
		rule, resolveErr := runner.rules.Resolve(configured)
		if resolveErr != nil {
			receipt.Outcome = OutcomeRuleFailure
			return receipt, resolveErr
		}
		strategyAllocation, found := allocationFor(candidate, checkpoint.Snapshot)
		if !found {
			receipt.Outcome = OutcomeInvalidOutput
			return receipt, ErrInvalidOutput
		}
		input, inputErr := riskmodel.NewRiskRuleInput(riskmodel.RiskRuleInputSpec{
			SchemaVersion: "risk-rule-input/v2", Proposal: proposal, PortfolioSnapshot: checkpoint.Snapshot,
			AllocationCandidate: candidate, StrategyAllocation: strategyAllocation,
			TradingDate: snapshotSpec.TradingDate, SessionContext: "PORTFOLIO_DECISION",
			RiskPolicyID: policy.ID(), RiskPolicyVersion: policy.Version(),
			RuleConfiguration: configured, EvaluatedAt: request.LogicalTime.UTC(),
		})
		if inputErr != nil {
			return receipt, inputErr
		}
		result, diagnostic := invokeRule(evaluationCtx, rule, input)
		if diagnostic != "" {
			receipt.Outcome, receipt.Diagnostic = OutcomePanic, diagnostic
			return receipt, ErrPanic
		}
		if err := evaluationCtx.Err(); err != nil {
			return timeoutReceipt(receipt, err)
		}
		validated, validateErr := riskmodel.NewRuleResult(result.Spec())
		if validateErr != nil || validated.RuleID() != configured.Descriptor.ID ||
			validated.Spec().RuleVersion != configured.Descriptor.Version ||
			validated.Spec().ConfigurationHash != configured.ConfigurationHash ||
			!validated.Spec().EvaluatedAt.Equal(request.LogicalTime.UTC()) {
			receipt.Outcome = OutcomeInvalidOutput
			return receipt, ErrInvalidOutput
		}
		results = append(results, validated)
		resultSpec := validated.Spec()
		runner.deps.Telemetry.Record(risktelemetry.Event{RuleID: validated.RuleID(),
			Status: validated.Status(), Effect: resultSpec.Effect, Severity: resultSpec.Severity,
			InFlight: int(runner.inFlight.Load())})
		if validated.Status() == riskmodel.RuleError {
			technical = append(technical, riskmodel.TechnicalRuleError{RuleID: validated.RuleID(),
				RuleVersion: validated.Spec().RuleVersion, Code: riskmodel.TechnicalRuleFailure,
				OccurredAt: request.LogicalTime.UTC()})
		}
	}

	evaluation, err := buildEvaluation(request, proposal, checkpoint.Snapshot, candidate, policy, results, technical)
	if err != nil {
		return receipt, err
	}
	decision, err := buildDecision(request.LogicalTime.UTC(), proposal, checkpoint.Snapshot,
		configuration.ID(), policy, candidate, evaluation)
	if err != nil {
		return receipt, err
	}
	reservation, next, err := nextCheckpoint(request.LogicalTime.UTC(), checkpoint, candidate, decision, trigger)
	if err != nil {
		return receipt, err
	}
	publication, err := riskstorage.NewPortfolioDecisionPublication(riskstorage.PortfolioDecisionPublication{
		TriggerID: trigger, PortfolioID: request.PortfolioID, ExpectedSnapshotID: checkpoint.Snapshot.ID(),
		ExpectedRevision: checkpoint.Snapshot.Revision(), ExpectedCheckpoint: checkpoint.CheckpointChecksum,
		Candidate: candidate, Evaluation: evaluation, Decision: decision, Reservation: reservation,
		NextCheckpoint: next,
	})
	if err != nil {
		return receipt, err
	}
	if err := evaluationCtx.Err(); err != nil {
		return timeoutReceipt(receipt, err)
	}
	publishStarted := time.Now()
	published, err := runner.deps.Runtime.PublishPortfolioDecision(evaluationCtx, publication)
	runner.deps.Telemetry.Record(risktelemetry.Event{Publish: time.Since(publishStarted),
		InFlight: int(runner.inFlight.Load())})
	if err != nil {
		if contextErr := evaluationCtx.Err(); contextErr != nil {
			return timeoutReceipt(receipt, contextErr)
		}
		if errors.Is(err, riskstorage.ErrStaleRevision) {
			receipt.Outcome = OutcomeRevisionConflict
		} else {
			receipt.Outcome = OutcomePublicationFailure
		}
		return receipt, err
	}
	outcome := outcomeForDecision(decision.Outcome())
	if published.Status == riskstorage.RuntimePublicationIdempotent {
		outcome = OutcomeDuplicateCommitted
	}
	return receiptFromPublication(receipt, published, outcome), nil
}
