// Package testfixture provides deterministic, non-production Phase 3 contract
// fixtures. It is not composed into the TradeEdge application.
package testfixture

import (
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

func ApprovedDecision() (riskmodel.PortfolioRiskDecision, error) {
	return decision(false, false)
}

func ModifiedDecision() (riskmodel.PortfolioRiskDecision, error) {
	return decision(true, false)
}

// ApprovedMultiLegDecision returns a deterministic protective BUY plus exposure-increasing SELL fixture.
func ApprovedMultiLegDecision() (riskmodel.PortfolioRiskDecision, error) {
	return decision(false, true)
}

func decision(modified, multiLeg bool) (riskmodel.PortfolioRiskDecision, error) {
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	proposal, err := proposal(now, multiLeg)
	if err != nil {
		return riskmodel.PortfolioRiskDecision{}, err
	}
	portfolioID, _ := portfoliomodel.NewPortfolioID("fixture")
	configurationID, _ := portfoliomodel.NewPortfolioConfigurationID("fixture")
	portfolioHash, _ := portfoliomodel.NewConfigurationHash([]byte(`{"portfolio":1}`))
	source, _ := portfoliomodel.NewStateChecksum([]byte(`{"source":1}`))
	capital, _ := portfoliomodel.NewCapitalState(
		money(1000), money(1000), money(0), money(0))
	tradingDate, _ := domain.NewCivilDate(2026, 7, 18)
	snapshot, err := portfoliomodel.NewPortfolioSnapshot(portfoliomodel.PortfolioSnapshotSpec{
		SchemaVersion: "portfolio-snapshot/v1", PortfolioID: portfolioID, Revision: 1,
		AsOfExchangeTime: now, GeneratedAt: now, TradingDate: tradingDate,
		BaseCurrency: "INR", State: portfoliomodel.PortfolioEnabled,
		ConfigurationID: configurationID, ConfigurationVersion: 1,
		ConfigurationHash: portfolioHash, Capital: capital,
		RealizedPnL: money(0), UnrealizedPnL: money(0), DailyRealizedPnL: money(0),
		DailyUnrealizedPnL: money(0), WeeklyRealizedPnL: money(0),
		HighWaterMark: money(1000), CurrentEquity: money(1000),
		SourceStateChecksum: source,
	})
	if err != nil {
		return riskmodel.PortfolioRiskDecision{}, err
	}
	known, _ := portfoliomodel.NewKnownMoney(money(100))
	unknown, _ := portfoliomodel.NewUnavailableMoney(portfoliomodel.AvailabilityUnknown)
	allocationID, _ := portfoliomodel.NewStrategyAllocationID("fixture")
	policyID, _ := portfoliomodel.NewAllocationPolicyID("fixture")
	quantity, _ := domain.NewQuantity(50)
	legBounds := make([]portfoliomodel.AllocationLegBound, len(proposal.Draft().Legs))
	for index, proposalLeg := range proposal.Draft().Legs {
		legRatio, _ := portfoliomodel.NewContractRatio(proposalLeg.Ratio)
		legBounds[index] = portfoliomodel.AllocationLegBound{
			InstrumentID: proposalLeg.InstrumentID, Side: proposalLeg.Side,
			Ratio: legRatio, Resolution: portfoliomodel.QuantityResolved,
			MaximumUnits: quantity, LotSize: quantity,
		}
	}
	candidate, err := portfoliomodel.NewAllocationCandidate(portfoliomodel.AllocationCandidateSpec{
		SchemaVersion: "allocation-candidate/v1", Proposal: proposal,
		PortfolioID: portfolioID, PortfolioSnapshotID: snapshot.ID(), PortfolioRevision: 1,
		StrategyAllocationID: allocationID, PolicyID: policyID, PolicyVersion: 1,
		RequestedSizing: proposal.Draft().Sizing, CandidateCapital: money(100),
		CandidatePremium: known, CandidateRiskBudget: unknown,
		LegBounds:     legBounds,
		ReserveImpact: money(0), Rounding: portfoliomodel.RoundingEvidence{
			RequestedMinor: 100, ApprovedMinor: 100, Method: "FLOOR_TO_BOUNDED_UNITS",
		},
		Reasons:           []portfoliomodel.AllocationReason{portfoliomodel.ReasonInsufficientAvailableCapital},
		ConfigurationHash: portfolioHash, GeneratedAt: now,
		ValidFrom: proposal.Draft().ValidFrom, ExpiresAt: proposal.Draft().ExpiresAt,
	})
	if err != nil {
		return riskmodel.PortfolioRiskDecision{}, err
	}
	ruleID, _ := riskmodel.NewRiskRuleID("FIXTURE_RULE")
	ruleHash, _ := riskmodel.NewRiskConfigurationHash([]byte(`{"limit":1}`))
	resultStatus := riskmodel.RulePass
	resultReason := "WITHIN_LIMIT"
	resultEffect := riskmodel.EffectNone
	if modified {
		resultStatus = riskmodel.RuleModificationRequired
		resultReason = "MODIFICATION_REQUIRED"
		resultEffect = riskmodel.EffectModify
	}
	resultSpec := riskmodel.RuleResultSpec{
		RuleID: ruleID, RuleVersion: 1, ConfigurationHash: ruleHash,
		Status: resultStatus, ReasonCode: resultReason,
		Severity: riskmodel.SeverityInfo, Effect: resultEffect, EvaluatedAt: now,
	}
	if modified {
		resultSpec.Adjustment = &riskmodel.RuleAdjustment{
			MaximumCapital: money(50), LegBounds: candidate.Spec().LegBounds,
			Constraints: []portfoliomodel.AllocationConstraint{{
				Code: portfoliomodel.ReasonInstrumentLimitExceeded, Before: money(100),
				After: money(50), Explanation: "fixture limit",
			}}, ValidUntil: proposal.Draft().ExpiresAt,
		}
	}
	result, err := riskmodel.NewRuleResult(resultSpec)
	if err != nil {
		return riskmodel.PortfolioRiskDecision{}, err
	}
	riskPolicyID, _ := riskmodel.NewRiskPolicyID("fixture")
	evaluationSpec := riskmodel.RiskEvaluationSpec{
		SchemaVersion: "risk-evaluation/v1", ProposalID: proposal.ID(),
		AllocationCandidateID: candidate.ID(), PortfolioSnapshotID: snapshot.ID(),
		PortfolioRevision: 1, RiskPolicyID: riskPolicyID, RiskPolicyVersion: 1,
		ConfigurationHash: ruleHash, RuleResults: []riskmodel.RuleResult{result},
		StartedAt: now, CompletedAt: now,
	}
	evaluationSpec.ID, err = riskmodel.DeriveRiskEvaluationID(evaluationSpec)
	if err != nil {
		return riskmodel.PortfolioRiskDecision{}, err
	}
	evaluation, err := riskmodel.NewRiskEvaluation(evaluationSpec)
	if err != nil {
		return riskmodel.PortfolioRiskDecision{}, err
	}
	outcome := riskmodel.DecisionApproved
	reason := riskmodel.DecisionAllRulesPassed
	maximumCapital := candidate.CandidateCapital()
	var constraints []portfoliomodel.AllocationConstraint
	if modified {
		outcome = riskmodel.DecisionModified
		reason = riskmodel.DecisionAllocationModified
		maximumCapital = money(50)
		constraints = []portfoliomodel.AllocationConstraint{{
			Code:   portfoliomodel.ReasonInstrumentLimitExceeded,
			Before: money(100), After: money(50), Explanation: "fixture limit",
		}}
	}
	return riskmodel.NewPortfolioRiskDecision(riskmodel.PortfolioRiskDecisionSpec{
		SchemaVersion: "portfolio-risk-decision/v1", Proposal: proposal,
		PortfolioID: portfolioID, PortfolioSnapshotID: snapshot.ID(),
		ExpectedPortfolioRevision: 1, AllocationCandidate: candidate,
		RiskEvaluation: evaluation, Outcome: outcome,
		PrimaryReason: reason, OriginalSizing: proposal.Draft().Sizing,
		ApprovedAllocation: &riskmodel.ApprovedAllocationBounds{
			CandidateID: candidate.ID(), MaximumCapital: maximumCapital,
			LegBounds: candidate.Spec().LegBounds, Constraints: constraints,
			ValidUntil: proposal.Draft().ExpiresAt,
		},
		PortfolioConfigurationID:   configurationID,
		PortfolioConfigurationHash: portfolioHash, RiskPolicyID: riskPolicyID,
		RiskPolicyVersion: 1, RiskConfigurationHash: ruleHash,
		GeneratedAt: now, ExpiresAt: proposal.Draft().ExpiresAt,
		SourceEvidenceChecksum: evaluation.EvidenceChecksum(),
	})
}

func proposal(now time.Time, multiLeg bool) (strategymodel.TradeProposal, error) {
	instrument, _ := domain.InstrumentIDFromCanonicalKey("fixture")
	sellInstrument, _ := domain.InstrumentIDFromCanonicalKey("fixture-sell")
	price, _ := domain.NewPrice(100, "INR")
	definitionID, _ := strategymodel.NewDefinitionID("fixture")
	manifest := strategymodel.VersionManifest{
		DefinitionID: definitionID, ImplementationVersion: "1", InputContractVersion: "1",
		ConfigurationSchemaVersion: "1", StateSchemaVersion: "1",
		ResultSchemaVersion: "1", ProposalSchemaVersion: "1",
	}
	versionID, _ := strategymodel.NewVersionID(manifest)
	configuration, _ := strategymodel.NewStrategyConfiguration("1", []byte(`{"fixture":1}`))
	instanceRevision, _ := strategymodel.NewInstanceRevisionID("fixture", versionID,
		configuration.Hash(), 1)
	evaluationID, _ := strategymodel.NewEvaluationID("fixture")
	frameID, _ := strategymodel.NewFrameID("fixture")
	eventID, _ := marketmodel.NewEventID("fixture")
	legs := []strategymodel.ProposalLeg{{
		InstrumentID: instrument, Side: domain.SideBuy, Ratio: 1,
		ReferencePrice: price, MaxDeviationBPS: 100,
	}}
	if multiLeg {
		legs = append(legs, strategymodel.ProposalLeg{
			InstrumentID: sellInstrument, Side: domain.SideSell, Ratio: 1,
			ReferencePrice: price, MaxDeviationBPS: 100,
		})
	}
	draft, err := strategymodel.NewProposalDraft(strategymodel.ProposalDraft{
		SchemaVersion: "proposal/v1",
		Legs:          legs,
		Sizing: strategymodel.SizingIntent{
			Kind: strategymodel.SizingStrategyBudgetBPS, ValueBPS: 1000,
		},
		ValidFrom: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
		RationaleCode: "FIXTURE", Explanation: "non-production contract fixture",
		Evidence: []strategymodel.Evidence{{
			Code: "SIGNAL", SourceEventIDs: []marketmodel.EventID{eventID},
			Value: 1, Unit: "COUNT", Explanation: "fixture evidence",
		}},
		ExitPolicyReference: "DAY_ONLY",
	})
	if err != nil {
		return strategymodel.TradeProposal{}, err
	}
	return strategymodel.NewTradeProposal(strategymodel.ProposalMetadata{
		DefinitionID: definitionID, VersionID: versionID, InstanceID: "fixture",
		InstanceRevisionID: instanceRevision, EvaluationID: evaluationID, FrameID: frameID,
		GeneratedAt: now, SourceEventIDs: []marketmodel.EventID{eventID},
		RequiredInstrumentIDs: func() []domain.InstrumentID {
			if multiLeg {
				return []domain.InstrumentID{instrument, sellInstrument}
			}
			return []domain.InstrumentID{instrument}
		}(),
	}, draft)
}

func money(value int64) domain.Money {
	result, _ := domain.NewMoney(value, "INR")
	return result
}
