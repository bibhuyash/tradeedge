package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

const (
	MaximumDecisionReasons = 16
	MaximumDecisionBytes   = 256 << 10
)

var ErrInvalidPortfolioRiskDecision = errors.New("invalid portfolio risk decision")

type DecisionOutcome string

const (
	DecisionApproved DecisionOutcome = "APPROVED"
	DecisionRejected DecisionOutcome = "REJECTED"
	DecisionModified DecisionOutcome = "MODIFIED"
	DecisionDeferred DecisionOutcome = "DEFERRED"
)

type DecisionReason string

const (
	DecisionAllRulesPassed        DecisionReason = "ALL_RULES_PASSED"
	DecisionAllocationModified    DecisionReason = "ALLOCATION_MODIFIED"
	DecisionRiskViolation         DecisionReason = "RISK_VIOLATION"
	DecisionRiskDataUnavailable   DecisionReason = "RISK_DATA_UNAVAILABLE"
	DecisionRuleError             DecisionReason = "RULE_ERROR"
	DecisionConfigurationDisabled DecisionReason = "CONFIGURATION_DISABLED"
	DecisionPortfolioDisabled     DecisionReason = "PORTFOLIO_DISABLED"
	DecisionKillSwitchActive      DecisionReason = "KILL_SWITCH_ACTIVE"
	DecisionCircuitBreakerOpen    DecisionReason = "CIRCUIT_BREAKER_OPEN"
	DecisionRevisionStale         DecisionReason = "PORTFOLIO_REVISION_STALE"
)

func (value DecisionReason) Validate() error {
	switch value {
	case DecisionAllRulesPassed, DecisionAllocationModified, DecisionRiskViolation,
		DecisionRiskDataUnavailable, DecisionRuleError, DecisionConfigurationDisabled,
		DecisionPortfolioDisabled, DecisionKillSwitchActive, DecisionCircuitBreakerOpen,
		DecisionRevisionStale:
		return nil
	default:
		return ErrInvalidPortfolioRiskDecision
	}
}

type ApprovedAllocationBounds struct {
	CandidateID    portfoliomodel.AllocationCandidateID
	MaximumCapital domain.Money
	LegBounds      []portfoliomodel.AllocationLegBound
	Constraints    []portfoliomodel.AllocationConstraint
	ValidUntil     time.Time
}

func newApprovedBounds(input ApprovedAllocationBounds, candidate portfoliomodel.AllocationCandidate) (ApprovedAllocationBounds, error) {
	if input.CandidateID.IsZero() || input.CandidateID != candidate.ID() ||
		input.MaximumCapital.IsZeroValue() || input.MaximumCapital.MinorUnits() < 0 ||
		input.MaximumCapital.Currency() != candidate.CandidateCapital().Currency() ||
		input.MaximumCapital.MinorUnits() > candidate.CandidateCapital().MinorUnits() ||
		input.ValidUntil.IsZero() || len(input.LegBounds) == 0 ||
		len(input.LegBounds) > portfoliomodel.MaximumAllocationLegs ||
		len(input.Constraints) > portfoliomodel.MaximumAllocationConstraints {
		return ApprovedAllocationBounds{}, ErrInvalidPortfolioRiskDecision
	}
	input.LegBounds = append([]portfoliomodel.AllocationLegBound(nil), input.LegBounds...)
	for _, leg := range input.LegBounds {
		if leg.Validate() != nil {
			return ApprovedAllocationBounds{}, ErrInvalidPortfolioRiskDecision
		}
	}
	sort.Slice(input.LegBounds, func(i, j int) bool {
		return input.LegBounds[i].InstrumentID.String() < input.LegBounds[j].InstrumentID.String()
	})
	input.Constraints = append([]portfoliomodel.AllocationConstraint(nil), input.Constraints...)
	for _, constraint := range input.Constraints {
		if constraint.Validate() != nil {
			return ApprovedAllocationBounds{}, ErrInvalidPortfolioRiskDecision
		}
	}
	sort.Slice(input.Constraints, func(i, j int) bool {
		return input.Constraints[i].Code < input.Constraints[j].Code
	})
	input.ValidUntil = input.ValidUntil.UTC()
	return input, nil
}

func approvedBoundsWithinCandidate(bounds ApprovedAllocationBounds,
	candidate portfoliomodel.AllocationCandidate) (equal bool, strictlySmaller bool) {
	candidateSpec := candidate.Spec()
	if len(bounds.LegBounds) != len(candidateSpec.LegBounds) {
		return false, false
	}
	equal = bounds.MaximumCapital.MinorUnits() == candidateSpec.CandidateCapital.MinorUnits()
	strictlySmaller = bounds.MaximumCapital.MinorUnits() < candidateSpec.CandidateCapital.MinorUnits()
	for index := range bounds.LegBounds {
		approved := bounds.LegBounds[index]
		candidateLeg := candidateSpec.LegBounds[index]
		if approved.InstrumentID != candidateLeg.InstrumentID || approved.Side != candidateLeg.Side ||
			approved.Ratio != candidateLeg.Ratio || approved.Resolution != candidateLeg.Resolution ||
			approved.LotSize != candidateLeg.LotSize ||
			approved.MaximumUnits.Int64() > candidateLeg.MaximumUnits.Int64() {
			return false, false
		}
		if approved.MaximumUnits != candidateLeg.MaximumUnits {
			equal = false
			strictlySmaller = true
		}
	}
	return equal, strictlySmaller
}

func evaluationSupportsOutcome(evaluation RiskEvaluation, outcome DecisionOutcome) bool {
	results := evaluation.RuleResults()
	technical := evaluation.Spec().TechnicalErrors
	hasModification := false
	hasViolation := false
	hasDefer := len(technical) > 0
	for _, result := range results {
		switch result.Status() {
		case RulePass:
		case RuleModificationRequired:
			hasModification = true
		case RuleViolation:
			hasViolation = true
		case RuleDefer, RuleError:
			hasDefer = true
		default:
			return false
		}
	}
	switch outcome {
	case DecisionApproved:
		return !hasModification && !hasViolation && !hasDefer
	case DecisionModified:
		return hasModification && !hasViolation && !hasDefer
	case DecisionRejected:
		return hasViolation && !hasDefer
	case DecisionDeferred:
		return hasDefer
	default:
		return false
	}
}

type PortfolioRiskDecisionSpec struct {
	SchemaVersion              string
	Proposal                   strategymodel.TradeProposal
	PortfolioID                portfoliomodel.PortfolioID
	PortfolioSnapshotID        portfoliomodel.PortfolioSnapshotID
	ExpectedPortfolioRevision  portfoliomodel.PortfolioRevision
	AllocationCandidate        portfoliomodel.AllocationCandidate
	RiskEvaluation             RiskEvaluation
	Outcome                    DecisionOutcome
	PrimaryReason              DecisionReason
	SecondaryReasons           []DecisionReason
	Violations                 []RiskViolation
	OriginalSizing             strategymodel.SizingIntent
	ApprovedAllocation         *ApprovedAllocationBounds
	PortfolioConfigurationID   portfoliomodel.PortfolioConfigurationID
	PortfolioConfigurationHash portfoliomodel.ConfigurationHash
	RiskPolicyID               RiskPolicyID
	RiskPolicyVersion          RiskPolicyVersion
	RiskConfigurationHash      RiskConfigurationHash
	GeneratedAt                time.Time
	ExpiresAt                  time.Time
	SourceEvidenceChecksum     EvidenceChecksum
}

type PortfolioRiskDecision struct {
	id       PortfolioRiskDecisionID
	checksum DecisionChecksum
	spec     PortfolioRiskDecisionSpec
	raw      []byte
}

func NewPortfolioRiskDecision(spec PortfolioRiskDecisionSpec) (PortfolioRiskDecision, error) {
	spec.SchemaVersion = strings.TrimSpace(spec.SchemaVersion)
	if spec.SchemaVersion == "" || spec.Proposal.IsZero() || spec.PortfolioID.IsZero() ||
		spec.PortfolioSnapshotID.IsZero() || spec.ExpectedPortfolioRevision.Validate() != nil ||
		spec.AllocationCandidate.ID().IsZero() || spec.RiskEvaluation.ID().IsZero() ||
		spec.PrimaryReason.Validate() != nil || spec.OriginalSizing.Validate() != nil ||
		spec.PortfolioConfigurationID.IsZero() || spec.PortfolioConfigurationHash.IsZero() ||
		spec.RiskPolicyID.IsZero() || spec.RiskPolicyVersion.Validate() != nil ||
		spec.RiskConfigurationHash.IsZero() || spec.GeneratedAt.IsZero() ||
		!spec.ExpiresAt.After(spec.GeneratedAt) || spec.SourceEvidenceChecksum.IsZero() ||
		len(spec.SecondaryReasons) > MaximumDecisionReasons ||
		len(spec.Violations) > MaximumViolationsPerEvaluation ||
		spec.AllocationCandidate.ProposalID() != spec.Proposal.ID() ||
		spec.AllocationCandidate.Spec().PortfolioID != spec.PortfolioID ||
		spec.AllocationCandidate.PortfolioSnapshotID() != spec.PortfolioSnapshotID ||
		spec.AllocationCandidate.PortfolioRevision() != spec.ExpectedPortfolioRevision ||
		spec.RiskEvaluation.ProposalID() != spec.Proposal.ID() ||
		spec.RiskEvaluation.AllocationCandidateID() != spec.AllocationCandidate.ID() ||
		spec.RiskEvaluation.Spec().PortfolioSnapshotID != spec.PortfolioSnapshotID ||
		spec.RiskEvaluation.Spec().PortfolioRevision != spec.ExpectedPortfolioRevision ||
		spec.RiskEvaluation.Spec().RiskPolicyID != spec.RiskPolicyID ||
		spec.RiskEvaluation.Spec().RiskPolicyVersion != spec.RiskPolicyVersion ||
		spec.RiskEvaluation.Spec().ConfigurationHash != spec.RiskConfigurationHash ||
		spec.OriginalSizing != spec.Proposal.Draft().Sizing ||
		spec.GeneratedAt.Before(spec.AllocationCandidate.Spec().ValidFrom) ||
		spec.ExpiresAt.After(spec.Proposal.Draft().ExpiresAt) ||
		spec.ExpiresAt.After(spec.AllocationCandidate.Spec().ExpiresAt) ||
		!evaluationSupportsOutcome(spec.RiskEvaluation, spec.Outcome) ||
		spec.RiskEvaluation.EvidenceChecksum() != spec.SourceEvidenceChecksum {
		return PortfolioRiskDecision{}, ErrInvalidPortfolioRiskDecision
	}
	secondary, err := normalizeDecisionReasons(spec.SecondaryReasons, spec.PrimaryReason)
	if err != nil {
		return PortfolioRiskDecision{}, err
	}
	violations := append([]RiskViolation(nil), spec.Violations...)
	evaluationViolations := spec.RiskEvaluation.Violations()
	if len(violations) != len(evaluationViolations) {
		return PortfolioRiskDecision{}, ErrInvalidPortfolioRiskDecision
	}
	for index := range violations {
		if violations[index].ID() != evaluationViolations[index].ID() {
			return PortfolioRiskDecision{}, ErrInvalidPortfolioRiskDecision
		}
	}
	var approved *ApprovedAllocationBounds
	switch spec.Outcome {
	case DecisionApproved:
		if spec.PrimaryReason != DecisionAllRulesPassed || spec.ApprovedAllocation == nil {
			return PortfolioRiskDecision{}, ErrInvalidPortfolioRiskDecision
		}
		value, boundsErr := newApprovedBounds(*spec.ApprovedAllocation, spec.AllocationCandidate)
		equal, _ := approvedBoundsWithinCandidate(value, spec.AllocationCandidate)
		if boundsErr != nil || !equal || len(value.Constraints) != 0 ||
			value.ValidUntil != spec.ExpiresAt {
			return PortfolioRiskDecision{}, ErrInvalidPortfolioRiskDecision
		}
		approved = &value
	case DecisionModified:
		if spec.PrimaryReason != DecisionAllocationModified || spec.ApprovedAllocation == nil {
			return PortfolioRiskDecision{}, ErrInvalidPortfolioRiskDecision
		}
		value, boundsErr := newApprovedBounds(*spec.ApprovedAllocation, spec.AllocationCandidate)
		_, strictlySmaller := approvedBoundsWithinCandidate(value, spec.AllocationCandidate)
		if boundsErr != nil || !strictlySmaller || len(value.Constraints) == 0 ||
			value.ValidUntil.After(spec.ExpiresAt) {
			return PortfolioRiskDecision{}, ErrInvalidPortfolioRiskDecision
		}
		approved = &value
	case DecisionRejected:
		if spec.ApprovedAllocation != nil || len(violations) == 0 {
			return PortfolioRiskDecision{}, ErrInvalidPortfolioRiskDecision
		}
	case DecisionDeferred:
		if spec.ApprovedAllocation != nil {
			return PortfolioRiskDecision{}, ErrInvalidPortfolioRiskDecision
		}
	default:
		return PortfolioRiskDecision{}, ErrInvalidPortfolioRiskDecision
	}
	spec.SecondaryReasons = secondary
	spec.Violations = violations
	spec.ApprovedAllocation = approved
	spec.GeneratedAt = spec.GeneratedAt.UTC()
	spec.ExpiresAt = spec.ExpiresAt.UTC()
	raw, err := canonicalDecision(spec)
	if err != nil || len(raw) > MaximumDecisionBytes {
		return PortfolioRiskDecision{}, ErrInvalidPortfolioRiskDecision
	}
	id, _ := NewPortfolioRiskDecisionID(spec.Proposal.ID().String(),
		spec.PortfolioID.String(), spec.PortfolioSnapshotID.String(),
		fmt.Sprint(spec.ExpectedPortfolioRevision), spec.AllocationCandidate.ID().String(),
		spec.RiskEvaluation.ID().String(), string(raw))
	checksum, _ := NewDecisionChecksum(raw)
	return PortfolioRiskDecision{id: id, checksum: checksum, spec: spec, raw: raw}, nil
}

func normalizeDecisionReasons(values []DecisionReason, primary DecisionReason) ([]DecisionReason, error) {
	result := append([]DecisionReason(nil), values...)
	seen := map[DecisionReason]struct{}{primary: {}}
	for _, value := range result {
		if value.Validate() != nil {
			return nil, ErrInvalidPortfolioRiskDecision
		}
		if _, exists := seen[value]; exists {
			return nil, ErrInvalidPortfolioRiskDecision
		}
		seen[value] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func (value PortfolioRiskDecision) ID() PortfolioRiskDecisionID { return value.id }
func (value PortfolioRiskDecision) Checksum() DecisionChecksum  { return value.checksum }
func (value PortfolioRiskDecision) ProposalID() strategymodel.ProposalID {
	return value.spec.Proposal.ID()
}
func (value PortfolioRiskDecision) Outcome() DecisionOutcome { return value.spec.Outcome }
func (value PortfolioRiskDecision) ApprovedAllocation() (ApprovedAllocationBounds, bool) {
	if value.spec.ApprovedAllocation == nil {
		return ApprovedAllocationBounds{}, false
	}
	result := *value.spec.ApprovedAllocation
	result.LegBounds = append([]portfoliomodel.AllocationLegBound(nil), result.LegBounds...)
	result.Constraints = append([]portfoliomodel.AllocationConstraint(nil), result.Constraints...)
	return result, true
}
func (value PortfolioRiskDecision) Spec() PortfolioRiskDecisionSpec {
	result := value.spec
	result.SecondaryReasons = append([]DecisionReason(nil), result.SecondaryReasons...)
	result.Violations = append([]RiskViolation(nil), result.Violations...)
	if result.ApprovedAllocation != nil {
		copyValue, _ := value.ApprovedAllocation()
		result.ApprovedAllocation = &copyValue
	}
	return result
}
func (value PortfolioRiskDecision) CanonicalJSON() []byte {
	return append([]byte(nil), value.raw...)
}

func canonicalDecision(spec PortfolioRiskDecisionSpec) ([]byte, error) {
	secondary := make([]string, len(spec.SecondaryReasons))
	for index, value := range spec.SecondaryReasons {
		secondary[index] = string(value)
	}
	violations := make([]string, len(spec.Violations))
	for index, value := range spec.Violations {
		violations[index] = value.ID().String()
	}
	var approved any
	if spec.ApprovedAllocation != nil {
		type legWire struct {
			InstrumentID, Side, Resolution string
			Ratio                          uint32
			MaximumUnits, LotSize          int64
		}
		type constraintWire struct {
			Code, Explanation string
			Before, After     decisionMoneyWire
		}
		legs := make([]legWire, len(spec.ApprovedAllocation.LegBounds))
		for index, leg := range spec.ApprovedAllocation.LegBounds {
			legs[index] = legWire{
				InstrumentID: leg.InstrumentID.String(), Side: string(leg.Side),
				Resolution: string(leg.Resolution), Ratio: uint32(leg.Ratio),
				MaximumUnits: leg.MaximumUnits.Int64(), LotSize: leg.LotSize.Int64(),
			}
		}
		constraints := make([]constraintWire, len(spec.ApprovedAllocation.Constraints))
		for index, constraint := range spec.ApprovedAllocation.Constraints {
			constraints[index] = constraintWire{
				Code: string(constraint.Code), Explanation: constraint.Explanation,
				Before: decisionMoneyValue(constraint.Before), After: decisionMoneyValue(constraint.After),
			}
		}
		approved = struct {
			CandidateID    string
			MaximumCapital decisionMoneyWire
			LegBounds      []legWire
			Constraints    []constraintWire
			ValidUntil     string
		}{
			CandidateID:    spec.ApprovedAllocation.CandidateID.String(),
			MaximumCapital: decisionMoneyValue(spec.ApprovedAllocation.MaximumCapital),
			LegBounds:      legs,
			Constraints:    constraints,
			ValidUntil:     spec.ApprovedAllocation.ValidUntil.Format(time.RFC3339Nano),
		}
	}
	return json.Marshal(struct {
		SchemaVersion, ProposalID, PortfolioID, SnapshotID, CandidateID, EvaluationID string
		Outcome, PrimaryReason                                                        string
		SecondaryReasons, Violations                                                  []string
		ExpectedRevision                                                              uint64
		SizingKind                                                                    string
		SizingBPS                                                                     int32
		Approved                                                                      any
		PortfolioConfigurationID, PortfolioConfigurationHash                          string
		RiskPolicyID, RiskConfigurationHash, EvidenceChecksum                         string
		RiskPolicyVersion                                                             uint64
		GeneratedAt, ExpiresAt                                                        string
	}{
		SchemaVersion: spec.SchemaVersion, ProposalID: spec.Proposal.ID().String(),
		PortfolioID: spec.PortfolioID.String(), SnapshotID: spec.PortfolioSnapshotID.String(),
		CandidateID: spec.AllocationCandidate.ID().String(), EvaluationID: spec.RiskEvaluation.ID().String(),
		Outcome: string(spec.Outcome), PrimaryReason: string(spec.PrimaryReason),
		SecondaryReasons: secondary, Violations: violations,
		ExpectedRevision: uint64(spec.ExpectedPortfolioRevision),
		SizingKind:       string(spec.OriginalSizing.Kind), SizingBPS: spec.OriginalSizing.ValueBPS,
		Approved: approved, PortfolioConfigurationID: spec.PortfolioConfigurationID.String(),
		PortfolioConfigurationHash: spec.PortfolioConfigurationHash.String(),
		RiskPolicyID:               spec.RiskPolicyID.String(), RiskPolicyVersion: uint64(spec.RiskPolicyVersion),
		RiskConfigurationHash: spec.RiskConfigurationHash.String(),
		EvidenceChecksum:      spec.SourceEvidenceChecksum.String(),
		GeneratedAt:           spec.GeneratedAt.Format(time.RFC3339Nano),
		ExpiresAt:             spec.ExpiresAt.Format(time.RFC3339Nano),
	})
}

type decisionMoneyWire struct {
	Minor    int64  `json:"minor"`
	Currency string `json:"currency"`
}

func decisionMoneyValue(value domain.Money) decisionMoneyWire {
	return decisionMoneyWire{Minor: value.MinorUnits(), Currency: value.Currency().String()}
}
