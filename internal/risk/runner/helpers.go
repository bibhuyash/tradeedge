package runner

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	portfolioallocation "github.com/bibhuyash/tradeedge/internal/portfolio/allocation"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	"github.com/bibhuyash/tradeedge/internal/risk/rules"
	riskstorage "github.com/bibhuyash/tradeedge/internal/risk/storage"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

func buildEvaluation(request Request, proposal strategymodel.TradeProposal,
	snapshot portfoliomodel.PortfolioSnapshot, candidate portfoliomodel.AllocationCandidate,
	policy riskmodel.RiskPolicy, results []riskmodel.RuleResult,
	technical []riskmodel.TechnicalRuleError) (riskmodel.RiskEvaluation, error) {
	spec := riskmodel.RiskEvaluationSpec{
		SchemaVersion: "risk-evaluation/v2", ProposalID: proposal.ID(),
		AllocationCandidateID: candidate.ID(), PortfolioSnapshotID: snapshot.ID(),
		PortfolioRevision: snapshot.Revision(), RiskPolicyID: policy.ID(),
		RiskPolicyVersion: policy.Version(), ConfigurationHash: policy.ConfigurationHash(),
		RuleResults: results, TechnicalErrors: technical,
		StartedAt: request.LogicalTime.UTC(), CompletedAt: request.LogicalTime.UTC(),
	}
	id, err := riskmodel.DeriveRiskEvaluationID(spec)
	if err != nil {
		return riskmodel.RiskEvaluation{}, err
	}
	spec.ID = id
	for _, result := range results {
		if result.Status() != riskmodel.RuleViolation && result.Status() != riskmodel.RuleModificationRequired {
			continue
		}
		item := result.Spec()
		reason, err := riskmodel.NewViolationReason(item.ReasonCode)
		if err != nil {
			return riskmodel.RiskEvaluation{}, err
		}
		violation, err := riskmodel.NewRiskViolation(riskmodel.RiskViolationSpec{
			SchemaVersion: "risk-violation/v2", EvaluationID: id, RuleID: item.RuleID,
			RuleVersion: item.RuleVersion, ReasonCode: reason, Severity: item.Severity,
			Effect: item.Effect, Evidence: item.Evidence, GeneratedAt: request.LogicalTime.UTC(),
			ConfigurationHash: item.ConfigurationHash,
		})
		if err != nil {
			return riskmodel.RiskEvaluation{}, err
		}
		spec.Violations = append(spec.Violations, violation)
	}
	return riskmodel.NewRiskEvaluation(spec)
}

func buildDecision(logical time.Time, proposal strategymodel.TradeProposal,
	snapshot portfoliomodel.PortfolioSnapshot, configurationID portfoliomodel.PortfolioConfigurationID,
	policy riskmodel.RiskPolicy, candidate portfoliomodel.AllocationCandidate,
	evaluation riskmodel.RiskEvaluation) (riskmodel.PortfolioRiskDecision, error) {
	outcome := riskmodel.DecisionApproved
	primary := riskmodel.DecisionAllRulesPassed
	approved := &riskmodel.ApprovedAllocationBounds{
		CandidateID: candidate.ID(), MaximumCapital: candidate.CandidateCapital(),
		LegBounds: candidate.Spec().LegBounds, ValidUntil: candidate.Spec().ExpiresAt,
	}
	for _, result := range evaluation.RuleResults() {
		switch result.Status() {
		case riskmodel.RuleError, riskmodel.RuleDefer:
			outcome, primary, approved = riskmodel.DecisionDeferred, riskmodel.DecisionRiskDataUnavailable, nil
		case riskmodel.RuleViolation:
			if outcome != riskmodel.DecisionDeferred {
				outcome, primary, approved = riskmodel.DecisionRejected, riskmodel.DecisionRiskViolation, nil
			}
		case riskmodel.RuleModificationRequired:
			if outcome == riskmodel.DecisionApproved || outcome == riskmodel.DecisionModified {
				outcome, primary = riskmodel.DecisionModified, riskmodel.DecisionAllocationModified
				if err := intersectAdjustment(approved, result.Spec().Adjustment); err != nil {
					return riskmodel.PortfolioRiskDecision{}, err
				}
			}
		}
	}
	expires := candidate.Spec().ExpiresAt
	if approved != nil && approved.ValidUntil.Before(expires) {
		expires = approved.ValidUntil
	}
	return riskmodel.NewPortfolioRiskDecision(riskmodel.PortfolioRiskDecisionSpec{
		SchemaVersion: "portfolio-risk-decision/v2", Proposal: proposal,
		PortfolioID: snapshot.PortfolioID(), PortfolioSnapshotID: snapshot.ID(),
		ExpectedPortfolioRevision: snapshot.Revision(), AllocationCandidate: candidate,
		RiskEvaluation: evaluation, Outcome: outcome, PrimaryReason: primary,
		Violations: evaluation.Violations(), OriginalSizing: proposal.Draft().Sizing,
		ApprovedAllocation: approved, PortfolioConfigurationID: configurationID,
		PortfolioConfigurationHash: snapshot.ConfigurationHash(), RiskPolicyID: policy.ID(),
		RiskPolicyVersion: policy.Version(), RiskConfigurationHash: policy.ConfigurationHash(),
		GeneratedAt: logical, ExpiresAt: expires, SourceEvidenceChecksum: evaluation.EvidenceChecksum(),
	})
}

func intersectAdjustment(current *riskmodel.ApprovedAllocationBounds, adjustment *riskmodel.RuleAdjustment) error {
	if current == nil || adjustment == nil || adjustment.Validate() != nil ||
		adjustment.MaximumCapital.Currency() != current.MaximumCapital.Currency() {
		return ErrInvalidOutput
	}
	if adjustment.MaximumCapital.MinorUnits() < current.MaximumCapital.MinorUnits() {
		current.MaximumCapital = adjustment.MaximumCapital
	}
	if len(adjustment.LegBounds) != len(current.LegBounds) {
		return ErrInvalidOutput
	}
	for index := range current.LegBounds {
		left, right := current.LegBounds[index], adjustment.LegBounds[index]
		if left.InstrumentID != right.InstrumentID || left.Side != right.Side || left.Ratio != right.Ratio ||
			left.Resolution != right.Resolution || left.LotSize != right.LotSize ||
			right.MaximumUnits.Int64() > left.MaximumUnits.Int64() {
			return ErrInvalidOutput
		}
		if right.MaximumUnits.Int64() < left.MaximumUnits.Int64() {
			current.LegBounds[index].MaximumUnits = right.MaximumUnits
		}
	}
	current.Constraints = append(current.Constraints, adjustment.Constraints...)
	sort.Slice(current.Constraints, func(i, j int) bool { return current.Constraints[i].Code < current.Constraints[j].Code })
	if adjustment.ValidUntil.Before(current.ValidUntil) {
		current.ValidUntil = adjustment.ValidUntil
	}
	return nil
}

func nextCheckpoint(logical time.Time, current riskstorage.PortfolioCheckpoint,
	candidate portfoliomodel.AllocationCandidate, decision riskmodel.PortfolioRiskDecision,
	trigger riskmodel.DecisionTriggerID) (*portfoliomodel.CapitalReservation, riskstorage.PortfolioCheckpoint, error) {
	spec := current.Snapshot.Spec()
	spec.Revision++
	spec.GeneratedAt = logical
	spec.AsOfExchangeTime = logical
	var reservation *portfoliomodel.CapitalReservation
	if bounds, ok := decision.ApprovedAllocation(); ok {
		amount := bounds.MaximumCapital
		available, err := portfoliomodel.CheckedMoneySubtract(spec.Capital.Available, amount)
		if err != nil || available.MinorUnits() < 0 {
			return nil, riskstorage.PortfolioCheckpoint{}, ErrInvalidOutput
		}
		reserved, err := portfoliomodel.CheckedMoneyAdd(spec.Capital.Reserved, amount)
		if err != nil {
			return nil, riskstorage.PortfolioCheckpoint{}, err
		}
		spec.Capital, err = portfoliomodel.NewCapitalState(spec.Capital.Total, available, reserved, spec.Capital.Deployed)
		if err != nil {
			return nil, riskstorage.PortfolioCheckpoint{}, err
		}
		found := false
		for index, allocation := range spec.StrategyAllocations {
			item := allocation.Spec()
			if item.ID != candidate.Spec().StrategyAllocationID {
				continue
			}
			item.Remaining, err = portfoliomodel.CheckedMoneySubtract(item.Remaining, amount)
			if err != nil || item.Remaining.MinorUnits() < 0 {
				return nil, riskstorage.PortfolioCheckpoint{}, ErrInvalidOutput
			}
			item.Reserved, err = portfoliomodel.CheckedMoneyAdd(item.Reserved, amount)
			if err != nil {
				return nil, riskstorage.PortfolioCheckpoint{}, err
			}
			if item.Remaining.MinorUnits() == 0 {
				item.State = portfoliomodel.StrategyAllocationExhausted
			}
			spec.StrategyAllocations[index], err = portfoliomodel.NewStrategyAllocation(item)
			if err != nil {
				return nil, riskstorage.PortfolioCheckpoint{}, err
			}
			found = true
		}
		if !found {
			return nil, riskstorage.PortfolioCheckpoint{}, ErrInvalidOutput
		}
		spec.Exposures = mergeExposures(spec.Exposures, candidate.Spec().ProjectedExposure)
		reservationID, _ := portfoliomodel.NewCapitalReservationID(trigger.String(), decision.ID().String(),
			candidate.ID().String(), fmt.Sprint(spec.Revision))
		value, err := portfoliomodel.NewCapitalReservation(portfoliomodel.CapitalReservationSpec{
			SchemaVersion: "capital-reservation/v1", ID: reservationID, PortfolioID: spec.PortfolioID,
			PortfolioRevision: spec.Revision, CandidateID: candidate.ID(),
			StrategyAllocationID: candidate.Spec().StrategyAllocationID, Amount: amount,
			CreatedAt: logical, ExpiresAt: bounds.ValidUntil,
		})
		if err != nil {
			return nil, riskstorage.PortfolioCheckpoint{}, err
		}
		reservation = &value
	}
	if err := applyControlEffects(logical, decision, &spec); err != nil {
		return nil, riskstorage.PortfolioCheckpoint{}, err
	}
	source, _ := portfoliomodel.NewStateChecksum([]byte(strings.Join([]string{
		current.Snapshot.ID().String(), current.CheckpointChecksum.String(), decision.ID().String(),
	}, "|")))
	spec.SourceStateChecksum = source
	nextSnapshot, err := portfoliomodel.NewPortfolioSnapshot(spec)
	if err != nil {
		return nil, riskstorage.PortfolioCheckpoint{}, err
	}
	checkpoint := riskstorage.PortfolioCheckpoint{
		Snapshot: nextSnapshot, ParentSnapshotID: current.Snapshot.ID(), ParentChecksum: current.CheckpointChecksum,
		TriggerID: trigger, ProposalID: decision.ProposalID(), DecisionID: decision.ID(),
	}
	if reservation != nil {
		checkpoint.ReservationID = reservation.ID()
	}
	next, err := riskstorage.NewPortfolioCheckpoint(checkpoint)
	return reservation, next, err
}

func applyControlEffects(logical time.Time, decision riskmodel.PortfolioRiskDecision,
	snapshot *portfoliomodel.PortfolioSnapshotSpec) error {
	evidence, _ := portfoliomodel.NewStateChecksum(decision.CanonicalJSON())
	activateKill, tripCircuit := false, false
	var reason portfoliomodel.ControlReason
	for _, violation := range decision.Spec().Violations {
		switch violation.Effect() {
		case riskmodel.EffectActivateKillSwitch:
			activateKill = true
			if reason == "" {
				reason, _ = portfoliomodel.NewControlReason(string(violation.Spec().ReasonCode))
			}
		case riskmodel.EffectTripCircuitBreaker:
			tripCircuit = true
			if reason == "" {
				reason, _ = portfoliomodel.NewControlReason(string(violation.Spec().ReasonCode))
			}
		}
	}
	if activateKill {
		updated := false
		for index, control := range snapshot.KillSwitches {
			item := control.Spec()
			if !controlEffectApplies(item.Scope, item.ScopeSubject, decision) ||
				item.State == portfoliomodel.KillSwitchDisabledByConfiguration {
				continue
			}
			item.State, item.ReasonCode, item.ActivationEvidence = portfoliomodel.KillSwitchActive, reason, evidence
			item.ActivatedAt, item.ExpiresAt, item.StateRevision = logical, time.Time{}, item.StateRevision+1
			value, err := portfoliomodel.NewKillSwitch(item)
			if err != nil {
				return ErrInvalidOutput
			}
			snapshot.KillSwitches[index], updated = value, true
		}
		if !updated {
			return ErrInvalidOutput
		}
	}
	if tripCircuit {
		updated := false
		for index, control := range snapshot.CircuitBreakers {
			item := control.Spec()
			if !controlEffectApplies(item.Scope, item.ScopeSubject, decision) ||
				item.State == portfoliomodel.CircuitBreakerDisabled {
				continue
			}
			item.State, item.ReasonCode, item.Evidence = portfoliomodel.CircuitBreakerOpen, reason, evidence
			item.ChangedAt, item.StateRevision = logical, item.StateRevision+1
			value, err := portfoliomodel.NewCircuitBreaker(item)
			if err != nil {
				return ErrInvalidOutput
			}
			snapshot.CircuitBreakers[index], updated = value, true
		}
		if !updated {
			return ErrInvalidOutput
		}
	}
	return nil
}

func controlEffectApplies(scope portfoliomodel.ControlScope, subject string,
	decision riskmodel.PortfolioRiskDecision) bool {
	spec := decision.Spec()
	switch scope {
	case portfoliomodel.ScopeGlobal:
		return true
	case portfoliomodel.ScopePortfolio:
		return subject == spec.PortfolioID.String()
	case portfoliomodel.ScopeStrategyDefinition:
		return subject == spec.Proposal.Metadata().DefinitionID.String()
	case portfoliomodel.ScopeStrategyInstance:
		return subject == string(spec.Proposal.Metadata().InstanceID)
	case portfoliomodel.ScopeInstrument:
		for _, leg := range spec.Proposal.Draft().Legs {
			if subject == leg.InstrumentID.String() {
				return true
			}
		}
	case portfoliomodel.ScopeUnderlying, portfoliomodel.ScopeExposureGroup:
		dimension := portfoliomodel.ExposureUnderlying
		if scope == portfoliomodel.ScopeExposureGroup {
			dimension = portfoliomodel.ExposureGroup
		}
		for _, exposure := range spec.AllocationCandidate.Spec().ProjectedExposure {
			if exposure.Dimension() == dimension && exposure.Subject() == subject {
				return true
			}
		}
	}
	return false
}

func mergeExposures(current, projected []portfoliomodel.ExposureRecord) []portfoliomodel.ExposureRecord {
	result := append([]portfoliomodel.ExposureRecord(nil), current...)
	for _, update := range projected {
		found := false
		for index := range result {
			if result[index].Dimension() == update.Dimension() && result[index].Subject() == update.Subject() {
				result[index], found = update, true
				break
			}
		}
		if !found {
			result = append(result, update)
		}
	}
	return result
}

func allocationFor(candidate portfoliomodel.AllocationCandidate,
	snapshot portfoliomodel.PortfolioSnapshot) (portfoliomodel.StrategyAllocation, bool) {
	for _, value := range snapshot.StrategyAllocations() {
		if value.ID() == candidate.Spec().StrategyAllocationID {
			return value, true
		}
	}
	return portfoliomodel.StrategyAllocation{}, false
}

func invokeAllocation(allocator Allocator, input portfolioallocation.Input) (result portfoliomodel.AllocationCandidate, diagnostic string, err error) {
	defer func() {
		if value := recover(); value != nil {
			diagnostic = panicDiagnostic(value)
		}
	}()
	result, err = allocator.Evaluate(input)
	return
}

func invokeRule(ctx context.Context, rule rules.Rule, input riskmodel.RiskRuleInput) (result riskmodel.RuleResult, diagnostic string) {
	defer func() {
		if value := recover(); value != nil {
			diagnostic = panicDiagnostic(value)
		}
	}()
	result = rule.Evaluate(ctx, input)
	return
}

func panicDiagnostic(value any) string {
	diagnostic := fmt.Sprint(value)
	if len(diagnostic) > 256 {
		diagnostic = diagnostic[:256]
	}
	stack := debug.Stack()
	if len(stack) > 8192 {
		stack = stack[:8192]
	}
	return diagnostic + "\n" + string(stack)
}

func (runner *Runner) reserve(portfolio portfoliomodel.PortfolioID, trigger riskmodel.DecisionTriggerID) Outcome {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.closed {
		return OutcomeShutdown
	}
	if running, found := runner.running[portfolio]; found {
		if running == trigger {
			return OutcomeDuplicateInProgress
		}
		return OutcomePortfolioBusy
	}
	runner.running[portfolio] = trigger
	runner.wait.Add(1)
	return ""
}

func (runner *Runner) release(portfolio portfoliomodel.PortfolioID) {
	runner.mu.Lock()
	delete(runner.running, portfolio)
	runner.mu.Unlock()
	runner.wait.Done()
}

func (runner *Runner) Shutdown(ctx context.Context) error {
	runner.stopOnce.Do(func() {
		runner.mu.Lock()
		runner.closed = true
		runner.cancel()
		runner.mu.Unlock()
		go func() {
			runner.wait.Wait()
			close(runner.stopped)
		}()
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-runner.stopped:
		return nil
	}
}

func timeoutReceipt(receipt Receipt, err error) (Receipt, error) {
	if errors.Is(err, context.DeadlineExceeded) {
		receipt.Outcome = OutcomeTimedOut
		return receipt, ErrTimeout
	}
	receipt.Outcome = OutcomeCancelled
	return receipt, err
}

func receiptFromPublication(base Receipt, value riskstorage.RuntimePublicationReceipt, outcome Outcome) Receipt {
	base.Outcome, base.TriggerID, base.DecisionID = outcome, value.TriggerID, value.DecisionID
	base.SnapshotID, base.CommittedRevision = value.SnapshotID, value.Revision
	base.ReservationID, base.PublicationChecksum = value.ReservationID, value.PublicationChecksum
	return base
}

func outcomeForDecision(value riskmodel.DecisionOutcome) Outcome {
	switch value {
	case riskmodel.DecisionApproved:
		return OutcomeApproved
	case riskmodel.DecisionModified:
		return OutcomeModified
	case riskmodel.DecisionRejected:
		return OutcomeRejected
	default:
		return OutcomeDeferred
	}
}

func errOr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
