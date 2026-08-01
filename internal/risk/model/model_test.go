package model

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

func TestRiskIdentitiesUseFramedStableInputs(t *testing.T) {
	left, _ := NewRiskPolicyID("a", "bc")
	right, _ := NewRiskPolicyID("ab", "c")
	repeated, _ := NewRiskPolicyID("a", "bc")
	if left == right || left != repeated {
		t.Fatal("risk identity framing is ambiguous or unstable")
	}
	if _, err := NewRiskRuleID("unknown-rule"); !errors.Is(err, ErrInvalidRiskIdentity) {
		t.Fatalf("rule ID error = %v", err)
	}
}

func TestRiskPolicyOrderingAndDuplicateRejection(t *testing.T) {
	first := ruleConfiguration(t, "MAX_DAILY_LOSS", 2, EffectReject)
	second := ruleConfiguration(t, "KILL_SWITCH", 1, EffectReject)
	hash, _ := NewRiskConfigurationHash([]byte(`{"policy":1}`))
	id, _ := NewRiskPolicyID("policy")
	spec := RiskPolicySpec{
		ID: id, Version: 1, SchemaVersion: "risk-policy/v1", Lifecycle: PolicyActive,
		FailPosture: FailClosed, EffectiveFrom: fixtureTime().Add(-time.Hour),
		EffectiveUntil: fixtureTime().Add(time.Hour), Rules: []RiskRuleConfiguration{first, second},
		ConfigurationHash: hash,
	}
	policy, err := NewRiskPolicy(spec)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Rules()[0].Descriptor.ID != "KILL_SWITCH" {
		t.Fatal("policy did not retain deterministic configured order")
	}
	spec.Rules = []RiskRuleConfiguration{second, second}
	if _, err := NewRiskPolicy(spec); !errors.Is(err, ErrInvalidRiskPolicy) {
		t.Fatalf("duplicate rule error = %v", err)
	}
}

func TestEvidenceAndRuleResultAreTypedBoundedAndExclusive(t *testing.T) {
	fixture := riskFixture(t, RuleViolation)
	evidence := fixture.evidence
	raw := evidence.CanonicalJSON()
	if len(raw) == 0 || !bytes.Contains(raw, []byte(`"MoneyMinor":100`)) {
		t.Fatalf("canonical evidence omitted authoritative money: %s", raw)
	}
	raw[0] = 'x'
	if evidence.CanonicalJSON()[0] == 'x' {
		t.Fatal("evidence returned mutable canonical bytes")
	}
	result := fixture.result
	copyEvidence := result.Spec().Evidence
	copyEvidence[0] = RiskEvidence{}
	if result.Spec().Evidence[0].Code() == "" {
		t.Fatal("rule result returned mutable evidence slice")
	}
	spec := result.Spec()
	spec.Status = RulePass
	spec.Effect = EffectReject
	if _, err := NewRuleResult(spec); !errors.Is(err, ErrInvalidRiskEvidence) {
		t.Fatalf("exclusive result error = %v", err)
	}
}

func TestEvaluationViolationAndDecisionDeterminism(t *testing.T) {
	approvedFixture := riskFixture(t, RulePass)
	approved := decisionFixture(t, approvedFixture, DecisionApproved)
	repeated := decisionFixture(t, approvedFixture, DecisionApproved)
	if approved.ID() != repeated.ID() || approved.Checksum() != repeated.Checksum() ||
		!bytes.Equal(approved.CanonicalJSON(), repeated.CanonicalJSON()) {
		t.Fatal("approved decision is not deterministic")
	}
	if _, ok := approved.ApprovedAllocation(); !ok {
		t.Fatal("approved decision has no bounded allocation")
	}
	modifiedFixture := riskFixture(t, RuleModificationRequired)
	modified := decisionFixture(t, modifiedFixture, DecisionModified)
	if modified.Outcome() != DecisionModified {
		t.Fatalf("modified outcome = %s", modified.Outcome())
	}
	rejectedFixture := riskFixture(t, RuleViolation)
	rejected := decisionFixture(t, rejectedFixture, DecisionRejected)
	if _, ok := rejected.ApprovedAllocation(); ok {
		t.Fatal("rejected decision contains approved allocation")
	}
	deferredFixture := riskFixture(t, RuleDefer)
	deferred := decisionFixture(t, deferredFixture, DecisionDeferred)
	if _, ok := deferred.ApprovedAllocation(); ok {
		t.Fatal("deferred decision contains approved allocation")
	}
	if approved.ID() == modified.ID() || rejected.ID() == deferred.ID() {
		t.Fatal("materially different decisions retained an identity")
	}
}

func TestDecisionInvariantFailures(t *testing.T) {
	fixture := riskFixture(t, RulePass)
	base := decisionSpec(t, fixture, DecisionApproved)
	base.ApprovedAllocation = nil
	if _, err := NewPortfolioRiskDecision(base); !errors.Is(err, ErrInvalidPortfolioRiskDecision) {
		t.Fatalf("approved without bounds error = %v", err)
	}
	base = decisionSpec(t, fixture, DecisionDeferred)
	base.ExpiresAt = base.GeneratedAt
	if _, err := NewPortfolioRiskDecision(base); !errors.Is(err, ErrInvalidPortfolioRiskDecision) {
		t.Fatalf("invalid expiry error = %v", err)
	}
	base = decisionSpec(t, riskFixture(t, RuleModificationRequired), DecisionModified)
	base.ApprovedAllocation.MaximumCapital = fixture.candidate.CandidateCapital()
	if _, err := NewPortfolioRiskDecision(base); !errors.Is(err, ErrInvalidPortfolioRiskDecision) {
		t.Fatalf("unmodified MODIFIED error = %v", err)
	}
	base = decisionSpec(t, fixture, DecisionApproved)
	reduced, _ := domain.NewQuantity(50)
	base.ApprovedAllocation.LegBounds[0].MaximumUnits = reduced
	if _, err := NewPortfolioRiskDecision(base); !errors.Is(err, ErrInvalidPortfolioRiskDecision) {
		t.Fatalf("approved with reduced leg bound error = %v", err)
	}
	base = decisionSpec(t, riskFixture(t, RuleModificationRequired), DecisionApproved)
	if _, err := NewPortfolioRiskDecision(base); !errors.Is(err, ErrInvalidPortfolioRiskDecision) {
		t.Fatalf("approved with modifying evaluation error = %v", err)
	}
}

func TestModifiedDecisionMayReduceOnlyLegAuthority(t *testing.T) {
	fixture := riskFixture(t, RuleModificationRequired)
	spec := decisionSpec(t, fixture, DecisionModified)
	spec.ApprovedAllocation.MaximumCapital = fixture.candidate.CandidateCapital()
	reduced, _ := domain.NewQuantity(50)
	spec.ApprovedAllocation.LegBounds[0].MaximumUnits = reduced
	decision, err := NewPortfolioRiskDecision(spec)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome() != DecisionModified {
		t.Fatalf("outcome = %s", decision.Outcome())
	}
}

func TestDecisionCanonicalOutputIncludesAllApprovedAuthority(t *testing.T) {
	fixture := riskFixture(t, RuleModificationRequired)
	first := decisionSpec(t, fixture, DecisionModified)
	firstDecision, err := NewPortfolioRiskDecision(first)
	if err != nil {
		t.Fatal(err)
	}
	second := decisionSpec(t, fixture, DecisionModified)
	second.ApprovedAllocation.MaximumCapital = riskMoney(t, 40)
	second.ApprovedAllocation.Constraints[0].After = riskMoney(t, 40)
	secondDecision, err := NewPortfolioRiskDecision(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDecision.ID() == secondDecision.ID() ||
		bytes.Equal(firstDecision.CanonicalJSON(), secondDecision.CanonicalJSON()) {
		t.Fatal("changed approved authority retained canonical output or identity")
	}
	raw := firstDecision.CanonicalJSON()
	for _, field := range [][]byte{[]byte(`"LegBounds"`), []byte(`"Constraints"`), []byte(`"MaximumUnits"`)} {
		if !bytes.Contains(raw, field) {
			t.Fatalf("canonical decision omitted authority field %s: %s", field, raw)
		}
	}
}

func TestEvaluationRejectsViolationRuleMismatch(t *testing.T) {
	fixture := riskFixture(t, RuleViolation)
	spec := fixture.evaluation.Spec()
	violationSpec := spec.Violations[0].Spec()
	otherRule, _ := NewRiskRuleID("OTHER_RULE")
	violationSpec.RuleID = otherRule
	mismatched, err := NewRiskViolation(violationSpec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Violations = []RiskViolation{mismatched}
	if _, err := NewRiskEvaluation(spec); !errors.Is(err, ErrInvalidRiskEvaluation) {
		t.Fatalf("mismatched violation error = %v", err)
	}
}

func FuzzIdentityDerivationFraming(f *testing.F) {
	f.Add("a", "bc")
	f.Add("proposal", "snapshot")
	f.Fuzz(func(t *testing.T, left, right string) {
		if len(left) > 256 || len(right) > 256 || left == "" || right == "" {
			return
		}
		first, err := NewRiskEvaluationID(left, right)
		if err != nil {
			return
		}
		second, err := NewRiskEvaluationID(left, right)
		if err != nil || first != second {
			t.Fatal("identity derivation was not stable")
		}
	})
}

func FuzzPortfolioRiskDecisionValidation(f *testing.F) {
	f.Add(uint8(0), int64(60))
	f.Add(uint8(3), int64(-1))
	f.Fuzz(func(t *testing.T, rawOutcome uint8, expirySeconds int64) {
		fixture := riskFixture(t, RulePass)
		outcomes := []DecisionOutcome{DecisionApproved, DecisionModified, DecisionRejected, DecisionDeferred}
		outcome := outcomes[int(rawOutcome)%len(outcomes)]
		spec := decisionSpec(t, fixture, outcome)
		spec.ExpiresAt = spec.GeneratedAt.Add(time.Duration(expirySeconds) * time.Second)
		decision, err := NewPortfolioRiskDecision(spec)
		if err == nil {
			repeated, repeatErr := NewPortfolioRiskDecision(decision.Spec())
			if repeatErr != nil || repeated.ID() != decision.ID() ||
				!bytes.Equal(repeated.CanonicalJSON(), decision.CanonicalJSON()) {
				t.Fatalf("valid decision was not stable: %v", repeatErr)
			}
		}
	})
}

func FuzzRiskEvidenceValidation(f *testing.F) {
	f.Add(int64(100), "bounded evidence")
	f.Add(int64(-1), "")
	f.Fuzz(func(t *testing.T, observedMinor int64, explanation string) {
		fixture := riskFixture(t, RulePass)
		spec := fixture.evidence.Spec()
		spec.Observed.Money = riskMoney(t, observedMinor)
		if len(explanation) > MaximumEvidenceExplanationBytes+1 {
			explanation = explanation[:MaximumEvidenceExplanationBytes+1]
		}
		spec.Explanation = explanation
		value, err := NewRiskEvidence(spec)
		if err == nil {
			repeated, repeatErr := NewRiskEvidence(value.Spec())
			if repeatErr != nil ||
				!bytes.Equal(repeated.CanonicalJSON(), value.CanonicalJSON()) {
				t.Fatalf("valid evidence was not stable: %v", repeatErr)
			}
		}
	})
}

type fixture struct {
	proposal   strategymodel.TradeProposal
	snapshot   portfoliomodel.PortfolioSnapshot
	allocation portfoliomodel.StrategyAllocation
	candidate  portfoliomodel.AllocationCandidate
	rule       RiskRuleConfiguration
	evidence   RiskEvidence
	result     RuleResult
	evaluation RiskEvaluation
	policyID   RiskPolicyID
	configHash RiskConfigurationHash
}

func riskFixture(t *testing.T, status RuleResultStatus) fixture {
	t.Helper()
	now := fixtureTime()
	proposal, versionID, instanceRevision := riskProposal(t)
	portfolioID, _ := portfoliomodel.NewPortfolioID("primary")
	configID, _ := portfoliomodel.NewPortfolioConfigurationID("portfolio-config")
	portfolioHash, _ := portfoliomodel.NewConfigurationHash([]byte(`{"portfolio":1}`))
	source, _ := portfoliomodel.NewStateChecksum([]byte(`{"source":1}`))
	policyID, _ := portfoliomodel.NewAllocationPolicyID("allocation-policy")
	allocationID, _ := portfoliomodel.NewStrategyAllocationID("allocation")
	allocation, err := portfoliomodel.NewStrategyAllocation(portfoliomodel.StrategyAllocationSpec{
		ID: allocationID, DefinitionID: proposal.Metadata().DefinitionID, VersionID: versionID,
		InstanceID: proposal.Metadata().InstanceID, InstanceRevisionID: instanceRevision,
		PolicyID: policyID, PolicyVersion: 1, Limit: riskMoney(t, 500),
		Deployed: riskMoney(t, 100), Reserved: riskMoney(t, 0), Remaining: riskMoney(t, 400),
		DailyLoss: riskMoney(t, -5), State: portfoliomodel.StrategyAllocationEnabled,
		EffectiveFrom: now.Add(-time.Hour), EffectiveUntil: now.Add(time.Hour),
		ConfigurationHash: portfolioHash, SchemaVersion: "strategy-allocation/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	capital, _ := portfoliomodel.NewCapitalState(
		riskMoney(t, 1000), riskMoney(t, 900), riskMoney(t, 0), riskMoney(t, 100))
	date, _ := domain.NewCivilDate(2026, 7, 18)
	snapshot, err := portfoliomodel.NewPortfolioSnapshot(portfoliomodel.PortfolioSnapshotSpec{
		SchemaVersion: "portfolio-snapshot/v1", PortfolioID: portfolioID, Revision: 1,
		AsOfExchangeTime: now.Add(-time.Second), GeneratedAt: now, TradingDate: date,
		BaseCurrency: "INR", State: portfoliomodel.PortfolioEnabled,
		ConfigurationID: configID, ConfigurationVersion: 1, ConfigurationHash: portfolioHash,
		Capital: capital, RealizedPnL: riskMoney(t, 0), UnrealizedPnL: riskMoney(t, 0),
		DailyRealizedPnL: riskMoney(t, 0), DailyUnrealizedPnL: riskMoney(t, 0),
		WeeklyRealizedPnL: riskMoney(t, 0), HighWaterMark: riskMoney(t, 1000),
		CurrentEquity: riskMoney(t, 1000), StrategyAllocations: []portfoliomodel.StrategyAllocation{allocation},
		SourceStateChecksum: source,
	})
	if err != nil {
		t.Fatal(err)
	}
	known, _ := portfoliomodel.NewKnownMoney(riskMoney(t, 100))
	unknown, _ := portfoliomodel.NewUnavailableMoney(portfoliomodel.AvailabilityUnknown)
	ratio, _ := portfoliomodel.NewContractRatio(1)
	quantity, _ := domain.NewQuantity(100)
	lotSize, _ := domain.NewQuantity(50)
	candidate, err := portfoliomodel.NewAllocationCandidate(portfoliomodel.AllocationCandidateSpec{
		SchemaVersion: "allocation-candidate/v1", Proposal: proposal,
		PortfolioID: portfolioID, PortfolioSnapshotID: snapshot.ID(), PortfolioRevision: 1,
		StrategyAllocationID: allocation.ID(), PolicyID: policyID, PolicyVersion: 1,
		RequestedSizing: proposal.Draft().Sizing, CandidateCapital: riskMoney(t, 100),
		CandidatePremium: known, CandidateRiskBudget: unknown,
		LegBounds: []portfoliomodel.AllocationLegBound{{
			InstrumentID: proposal.Draft().Legs[0].InstrumentID, Side: domain.SideBuy,
			Ratio: ratio, Resolution: portfoliomodel.QuantityResolved,
			MaximumUnits: quantity, LotSize: lotSize,
		}},
		ReserveImpact: riskMoney(t, 0),
		Rounding: portfoliomodel.RoundingEvidence{
			RequestedMinor: 100, ApprovedMinor: 100, Method: "FLOOR_TO_BOUNDED_UNITS",
		},
		Reasons:           []portfoliomodel.AllocationReason{portfoliomodel.ReasonInsufficientAvailableCapital},
		ConfigurationHash: portfolioHash, GeneratedAt: now,
		ValidFrom: proposal.Draft().ValidFrom, ExpiresAt: proposal.Draft().ExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	rule := ruleConfiguration(t, "MAX_DAILY_LOSS", 1, effectForStatus(status))
	riskPolicyID, _ := NewRiskPolicyID("risk-policy")
	observed := EvidenceValue{
		Kind: EvidenceMoney, Availability: portfoliomodel.AvailabilityKnown,
		Money: riskMoney(t, 100),
	}
	limit := observed
	limit.Money = riskMoney(t, 200)
	headroom := observed
	evidence, err := NewRiskEvidence(RiskEvidenceSpec{
		Code: "DAILY_LOSS", Observed: observed, Limit: limit, Projected: observed,
		RemainingHeadroom: headroom, Unit: "INR_MINOR", Comparison: CompareLessOrEqual,
		SubjectType: SubjectPortfolio, SubjectIdentity: portfolioID.String(),
		SourceSnapshotID: snapshot.ID(), SourceProposalID: proposal.ID(),
		FormulaVersion: "daily-loss/v1", EvidenceAt: now, Explanation: "bounded loss evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	resultSpec := RuleResultSpec{
		RuleID: rule.Descriptor.ID, RuleVersion: rule.Descriptor.Version,
		ConfigurationHash: rule.ConfigurationHash, Status: status,
		ReasonCode: reasonForStatus(status), Severity: SeverityBlocking,
		Effect: effectForStatus(status), Evidence: []RiskEvidence{evidence}, EvaluatedAt: now,
	}
	if status == RuleModificationRequired {
		resultSpec.Adjustment = &RuleAdjustment{
			MaximumCapital: riskMoney(t, 50), LegBounds: candidate.Spec().LegBounds,
			Constraints: []portfoliomodel.AllocationConstraint{{
				Code: portfoliomodel.ReasonInstrumentLimitExceeded, Before: riskMoney(t, 100),
				After: riskMoney(t, 50), Explanation: "rule modification",
			}}, ValidUntil: proposal.Draft().ExpiresAt,
		}
	}
	result, err := NewRuleResult(resultSpec)
	if err != nil {
		t.Fatal(err)
	}
	configHash, _ := NewRiskConfigurationHash([]byte(`{"risk":1}`))
	evaluationSpec := RiskEvaluationSpec{
		SchemaVersion: "risk-evaluation/v1", ProposalID: proposal.ID(),
		AllocationCandidateID: candidate.ID(), PortfolioSnapshotID: snapshot.ID(),
		PortfolioRevision: 1, RiskPolicyID: riskPolicyID, RiskPolicyVersion: 1,
		ConfigurationHash: configHash, RuleResults: []RuleResult{result},
		StartedAt: now, CompletedAt: now,
	}
	if status == RuleError || status == RuleDefer {
		evaluationSpec.TechnicalErrors = []TechnicalRuleError{{
			RuleID: rule.Descriptor.ID, RuleVersion: rule.Descriptor.Version,
			Code: TechnicalUnavailableInput, OccurredAt: now,
		}}
	}
	evaluationID, err := DeriveRiskEvaluationID(evaluationSpec)
	if err != nil {
		t.Fatal(err)
	}
	evaluationSpec.ID = evaluationID
	if status == RuleViolation || status == RuleModificationRequired {
		reason, _ := NewViolationReason(reasonForStatus(status))
		effect := EffectReject
		if status == RuleModificationRequired {
			effect = EffectModify
		}
		violation, violationErr := NewRiskViolation(RiskViolationSpec{
			SchemaVersion: "risk-violation/v1", EvaluationID: evaluationID,
			RuleID: rule.Descriptor.ID, RuleVersion: rule.Descriptor.Version,
			ReasonCode: reason, Severity: SeverityBlocking, Effect: effect,
			Evidence: []RiskEvidence{evidence}, GeneratedAt: now,
			ConfigurationHash: rule.ConfigurationHash,
		})
		if violationErr != nil {
			t.Fatal(violationErr)
		}
		evaluationSpec.Violations = []RiskViolation{violation}
	}
	evaluation, err := NewRiskEvaluation(evaluationSpec)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{
		proposal: proposal, snapshot: snapshot, allocation: allocation, candidate: candidate,
		rule: rule, evidence: evidence, result: result, evaluation: evaluation,
		policyID: riskPolicyID, configHash: configHash,
	}
}

func decisionFixture(t *testing.T, fixture fixture, outcome DecisionOutcome) PortfolioRiskDecision {
	t.Helper()
	value, err := NewPortfolioRiskDecision(decisionSpec(t, fixture, outcome))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func decisionSpec(t *testing.T, fixture fixture, outcome DecisionOutcome) PortfolioRiskDecisionSpec {
	t.Helper()
	now := fixtureTime()
	portfolioSpec := fixture.snapshot.Spec()
	spec := PortfolioRiskDecisionSpec{
		SchemaVersion: "portfolio-risk-decision/v1", Proposal: fixture.proposal,
		PortfolioID: fixture.snapshot.PortfolioID(), PortfolioSnapshotID: fixture.snapshot.ID(),
		ExpectedPortfolioRevision: fixture.snapshot.Revision(),
		AllocationCandidate:       fixture.candidate, RiskEvaluation: fixture.evaluation,
		Outcome: outcome, OriginalSizing: fixture.proposal.Draft().Sizing,
		Violations:                 fixture.evaluation.Violations(),
		PortfolioConfigurationID:   portfolioSpec.ConfigurationID,
		PortfolioConfigurationHash: fixture.snapshot.ConfigurationHash(),
		RiskPolicyID:               fixture.policyID, RiskPolicyVersion: 1,
		RiskConfigurationHash: fixture.configHash, GeneratedAt: now,
		ExpiresAt:              fixture.proposal.Draft().ExpiresAt,
		SourceEvidenceChecksum: fixture.evaluation.EvidenceChecksum(),
	}
	switch outcome {
	case DecisionApproved:
		spec.PrimaryReason = DecisionAllRulesPassed
		spec.ApprovedAllocation = &ApprovedAllocationBounds{
			CandidateID:    fixture.candidate.ID(),
			MaximumCapital: fixture.candidate.CandidateCapital(),
			LegBounds:      fixture.candidate.Spec().LegBounds,
			ValidUntil:     fixture.proposal.Draft().ExpiresAt,
		}
	case DecisionModified:
		spec.PrimaryReason = DecisionAllocationModified
		spec.ApprovedAllocation = &ApprovedAllocationBounds{
			CandidateID: fixture.candidate.ID(), MaximumCapital: riskMoney(t, 50),
			LegBounds: fixture.candidate.Spec().LegBounds,
			Constraints: []portfoliomodel.AllocationConstraint{{
				Code:   portfoliomodel.ReasonInstrumentLimitExceeded,
				Before: riskMoney(t, 100), After: riskMoney(t, 50),
				Explanation: "instrument capital limit",
			}},
			ValidUntil: fixture.proposal.Draft().ExpiresAt,
		}
	case DecisionRejected:
		spec.PrimaryReason = DecisionRiskViolation
	case DecisionDeferred:
		spec.PrimaryReason = DecisionRiskDataUnavailable
	}
	return spec
}

func ruleConfiguration(t *testing.T, idValue string, order uint16,
	effect RuleEffect) RiskRuleConfiguration {
	t.Helper()
	id, _ := NewRiskRuleID(idValue)
	canonical := []byte(`{"limit_minor":200}`)
	hash, _ := NewRiskConfigurationHash(canonical)
	value, err := NewRiskRuleConfiguration(RiskRuleConfiguration{
		Descriptor: RiskRuleDescriptor{
			ID: id, Version: 1, Name: idValue, Description: "deterministic fixture rule",
			SchemaVersion: "risk-rule/v1",
		},
		Order: order, Severity: SeverityBlocking, Effect: effect,
		ConfigurationHash: hash, CanonicalJSON: canonical,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func effectForStatus(status RuleResultStatus) RuleEffect {
	switch status {
	case RulePass:
		return EffectNone
	case RuleViolation:
		return EffectReject
	case RuleModificationRequired:
		return EffectModify
	default:
		return EffectDefer
	}
}

func reasonForStatus(status RuleResultStatus) string {
	switch status {
	case RulePass:
		return "WITHIN_LIMIT"
	case RuleViolation:
		return "DAILY_LOSS_EXCEEDED"
	case RuleModificationRequired:
		return "MODIFICATION_REQUIRED"
	case RuleDefer:
		return "DATA_UNAVAILABLE"
	default:
		return "RULE_ERROR"
	}
}

func riskProposal(t *testing.T) (strategymodel.TradeProposal,
	strategymodel.VersionID, strategymodel.InstanceRevisionID) {
	t.Helper()
	instrument, _ := domain.InstrumentIDFromCanonicalKey("risk-instrument")
	price, _ := domain.NewPrice(100, "INR")
	definitionID, _ := strategymodel.NewDefinitionID("risk-fixture")
	manifest := strategymodel.VersionManifest{
		DefinitionID: definitionID, ImplementationVersion: "1", InputContractVersion: "1",
		ConfigurationSchemaVersion: "1", StateSchemaVersion: "1",
		ResultSchemaVersion: "1", ProposalSchemaVersion: "1",
	}
	versionID, _ := strategymodel.NewVersionID(manifest)
	configuration, _ := strategymodel.NewStrategyConfiguration("1", []byte(`{"fixture":1}`))
	instanceRevision, _ := strategymodel.NewInstanceRevisionID("risk-instance", versionID,
		configuration.Hash(), 1)
	evaluationID, _ := strategymodel.NewEvaluationID("strategy-evaluation")
	frameID, _ := strategymodel.NewFrameID("frame")
	eventID, _ := marketmodel.NewEventID("event")
	now := fixtureTime()
	draft, err := strategymodel.NewProposalDraft(strategymodel.ProposalDraft{
		SchemaVersion: "proposal/v1",
		Legs: []strategymodel.ProposalLeg{{
			InstrumentID: instrument, Side: domain.SideBuy, Ratio: 1,
			ReferencePrice: price, MaxDeviationBPS: 100,
		}},
		Sizing: strategymodel.SizingIntent{
			Kind: strategymodel.SizingStrategyBudgetBPS, ValueBPS: 1000,
		},
		ValidFrom: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
		RationaleCode: "FIXTURE", Explanation: "risk fixture",
		Evidence: []strategymodel.Evidence{{
			Code: "SIGNAL", SourceEventIDs: []marketmodel.EventID{eventID},
			Value: 1, Unit: "COUNT", Explanation: "fixture evidence",
		}},
		ExitPolicyReference: "DAY_ONLY",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := strategymodel.NewTradeProposal(strategymodel.ProposalMetadata{
		DefinitionID: definitionID, VersionID: versionID, InstanceID: "risk-instance",
		InstanceRevisionID: instanceRevision, EvaluationID: evaluationID, FrameID: frameID,
		GeneratedAt: now, SourceEventIDs: []marketmodel.EventID{eventID},
		RequiredInstrumentIDs: []domain.InstrumentID{instrument},
	}, draft)
	if err != nil {
		t.Fatal(err)
	}
	return proposal, versionID, instanceRevision
}

func riskMoney(t *testing.T, value int64) domain.Money {
	t.Helper()
	result, err := domain.NewMoney(value, "INR")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func fixtureTime() time.Time {
	return time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
}
