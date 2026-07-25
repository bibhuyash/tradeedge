package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

const (
	MaximumAllocationConstraints = 32
	MaximumAllocationReasons     = 16
	MaximumAllocationLegs        = strategymodel.MaximumProposalLegs
	MaximumExplanationBytes      = 1024
)

var (
	ErrInvalidAllocationCandidate = errors.New("invalid allocation candidate")
	ErrAllocationImpossible       = errors.New("portfolio allocation impossible")
	ErrUnsupportedProposal        = errors.New("unsupported advisory trade proposal")
)

type AllocationOutcome string

const (
	AllocationCandidateCreated AllocationOutcome = "ALLOCATION_CANDIDATE_CREATED"
	AllocationRejected         AllocationOutcome = "ALLOCATION_REJECTED"
	AllocationDeferred         AllocationOutcome = "ALLOCATION_DEFERRED"
	AllocationInvalid          AllocationOutcome = "ALLOCATION_INVALID"
)

type AllocationReason string

const (
	ReasonInsufficientAvailableCapital AllocationReason = "INSUFFICIENT_AVAILABLE_CAPITAL"
	ReasonStrategyAllocationExhausted  AllocationReason = "STRATEGY_ALLOCATION_EXHAUSTED"
	ReasonPortfolioAllocationExhausted AllocationReason = "PORTFOLIO_ALLOCATION_EXHAUSTED"
	ReasonInstrumentLimitExceeded      AllocationReason = "INSTRUMENT_LIMIT_EXCEEDED"
	ReasonUnderlyingLimitExceeded      AllocationReason = "UNDERLYING_LIMIT_EXCEEDED"
	ReasonExposureGroupLimitExceeded   AllocationReason = "EXPOSURE_GROUP_LIMIT_EXCEEDED"
	ReasonReserveRequirementNotMet     AllocationReason = "RESERVE_REQUIREMENT_NOT_MET"
	ReasonInvalidSizingIntent          AllocationReason = "INVALID_SIZING_INTENT"
	ReasonMissingInstrumentMetadata    AllocationReason = "MISSING_INSTRUMENT_METADATA"
	ReasonUnsupportedProposalStructure AllocationReason = "UNSUPPORTED_PROPOSAL_STRUCTURE"
	ReasonUnknownMaximumLoss           AllocationReason = "UNKNOWN_MAXIMUM_LOSS"
	ReasonConfigurationDisabled        AllocationReason = "CONFIGURATION_DISABLED"
	ReasonStrategyDisabled             AllocationReason = "STRATEGY_DISABLED"
	ReasonPortfolioDisabled            AllocationReason = "PORTFOLIO_DISABLED"
	ReasonPolicyNotEffective           AllocationReason = "POLICY_NOT_EFFECTIVE"
	ReasonStalePortfolioSnapshot       AllocationReason = "STALE_PORTFOLIO_SNAPSHOT"
	ReasonArithmeticOverflow           AllocationReason = "ARITHMETIC_OVERFLOW"
)

func (value AllocationReason) Validate() error {
	switch value {
	case ReasonInsufficientAvailableCapital, ReasonStrategyAllocationExhausted,
		ReasonPortfolioAllocationExhausted, ReasonInstrumentLimitExceeded,
		ReasonUnderlyingLimitExceeded, ReasonExposureGroupLimitExceeded,
		ReasonReserveRequirementNotMet, ReasonInvalidSizingIntent,
		ReasonMissingInstrumentMetadata, ReasonUnsupportedProposalStructure,
		ReasonUnknownMaximumLoss, ReasonConfigurationDisabled, ReasonStrategyDisabled,
		ReasonPortfolioDisabled, ReasonPolicyNotEffective, ReasonStalePortfolioSnapshot,
		ReasonArithmeticOverflow:
		return nil
	default:
		return ErrInvalidAllocationCandidate
	}
}

type AllocationConstraint struct {
	Code        AllocationReason
	Before      domain.Money
	After       domain.Money
	Explanation string
}

func (value AllocationConstraint) Validate() error {
	if value.Code.Validate() != nil || value.Before.IsZeroValue() ||
		value.After.IsZeroValue() || value.Before.Currency() != value.After.Currency() ||
		value.Before.MinorUnits() < 0 || value.After.MinorUnits() < 0 ||
		value.After.MinorUnits() > value.Before.MinorUnits() ||
		strings.TrimSpace(value.Explanation) == "" ||
		len(value.Explanation) > MaximumExplanationBytes {
		return ErrInvalidAllocationCandidate
	}
	return nil
}

type QuantityResolution string

const (
	QuantityResolved      QuantityResolution = "RESOLVED"
	QuantityUnavailable   QuantityResolution = "UNAVAILABLE"
	QuantityNotApplicable QuantityResolution = "NOT_APPLICABLE"
)

type AllocationLegBound struct {
	InstrumentID domain.InstrumentID
	Side         domain.Side
	Ratio        ContractRatio
	Resolution   QuantityResolution
	MaximumUnits domain.Quantity
	LotSize      domain.Quantity
}

func (value AllocationLegBound) Validate() error {
	if value.InstrumentID.IsZero() || (value.Side != domain.SideBuy && value.Side != domain.SideSell) ||
		value.Ratio == 0 {
		return ErrInvalidAllocationCandidate
	}
	switch value.Resolution {
	case QuantityResolved:
		if !value.MaximumUnits.IsValid() || !value.LotSize.IsValid() ||
			value.MaximumUnits.Int64()%value.LotSize.Int64() != 0 {
			return ErrInvalidAllocationCandidate
		}
	case QuantityUnavailable, QuantityNotApplicable:
		if value.MaximumUnits.IsValid() || value.LotSize.IsValid() {
			return ErrInvalidAllocationCandidate
		}
	default:
		return ErrInvalidAllocationCandidate
	}
	return nil
}

type RoundingEvidence struct {
	RequestedMinor int64
	ApprovedMinor  int64
	RemainderMinor int64
	Method         string
}

func (value RoundingEvidence) Validate() error {
	if value.RequestedMinor < 0 || value.ApprovedMinor < 0 ||
		value.ApprovedMinor > value.RequestedMinor ||
		value.RemainderMinor != value.RequestedMinor-value.ApprovedMinor ||
		value.Method != "FLOOR_TO_BOUNDED_UNITS" {
		return ErrInvalidAllocationCandidate
	}
	return nil
}

type AllocationCandidateSpec struct {
	SchemaVersion        string
	Proposal             strategymodel.TradeProposal
	PortfolioID          PortfolioID
	PortfolioSnapshotID  PortfolioSnapshotID
	PortfolioRevision    PortfolioRevision
	StrategyAllocationID StrategyAllocationID
	PolicyID             AllocationPolicyID
	PolicyVersion        AllocationPolicyVersion
	RequestedSizing      strategymodel.SizingIntent
	CandidateCapital     domain.Money
	CandidatePremium     MeasuredMoney
	CandidateRiskBudget  MeasuredMoney
	LegBounds            []AllocationLegBound
	IncrementalExposure  []ExposureRecord
	ProjectedExposure    []ExposureRecord
	ReserveImpact        domain.Money
	Rounding             RoundingEvidence
	Constraints          []AllocationConstraint
	Reasons              []AllocationReason
	ConfigurationHash    ConfigurationHash
	GeneratedAt          time.Time
	ValidFrom            time.Time
	ExpiresAt            time.Time
}

type AllocationCandidate struct {
	id   AllocationCandidateID
	spec AllocationCandidateSpec
	raw  []byte
}

func NewAllocationCandidate(spec AllocationCandidateSpec) (AllocationCandidate, error) {
	spec.SchemaVersion = strings.TrimSpace(spec.SchemaVersion)
	if spec.SchemaVersion == "" || spec.Proposal.IsZero() || spec.PortfolioID.IsZero() ||
		spec.PortfolioSnapshotID.IsZero() || spec.PortfolioRevision.Validate() != nil ||
		spec.StrategyAllocationID.IsZero() || spec.PolicyID.IsZero() ||
		spec.PolicyVersion.Validate() != nil || spec.RequestedSizing.Validate() != nil ||
		spec.CandidateCapital.IsZeroValue() || spec.CandidateCapital.MinorUnits() < 0 ||
		spec.CandidatePremium.Validate() != nil || spec.CandidateRiskBudget.Validate() != nil ||
		spec.ReserveImpact.IsZeroValue() || spec.ReserveImpact.MinorUnits() < 0 ||
		spec.ReserveImpact.Currency() != spec.CandidateCapital.Currency() ||
		spec.Rounding.Validate() != nil || spec.ConfigurationHash.IsZero() ||
		spec.GeneratedAt.IsZero() || spec.ValidFrom.IsZero() ||
		!spec.ExpiresAt.After(spec.ValidFrom) || spec.GeneratedAt.Before(spec.ValidFrom) ||
		!spec.GeneratedAt.Before(spec.ExpiresAt) ||
		len(spec.LegBounds) == 0 || len(spec.LegBounds) > MaximumAllocationLegs ||
		len(spec.Constraints) > MaximumAllocationConstraints ||
		len(spec.Reasons) > MaximumAllocationReasons {
		return AllocationCandidate{}, ErrInvalidAllocationCandidate
	}
	metadata := spec.Proposal.Metadata()
	if metadata.InstanceRevisionID.IsZero() ||
		spec.ValidFrom.Before(spec.Proposal.Draft().ValidFrom) ||
		spec.ExpiresAt.After(spec.Proposal.Draft().ExpiresAt) {
		return AllocationCandidate{}, ErrInvalidAllocationCandidate
	}
	legs := append([]AllocationLegBound(nil), spec.LegBounds...)
	seenLegs := make(map[domain.InstrumentID]struct{}, len(legs))
	for _, leg := range legs {
		if leg.Validate() != nil {
			return AllocationCandidate{}, ErrInvalidAllocationCandidate
		}
		if _, exists := seenLegs[leg.InstrumentID]; exists {
			return AllocationCandidate{}, ErrInvalidAllocationCandidate
		}
		seenLegs[leg.InstrumentID] = struct{}{}
	}
	sort.Slice(legs, func(i, j int) bool {
		return legs[i].InstrumentID.String() < legs[j].InstrumentID.String()
	})
	incremental, err := normalizeExposures(spec.IncrementalExposure)
	if err != nil {
		return AllocationCandidate{}, err
	}
	projected, err := normalizeExposures(spec.ProjectedExposure)
	if err != nil || len(incremental) != len(projected) {
		return AllocationCandidate{}, ErrInvalidAllocationCandidate
	}
	for index := range incremental {
		if incremental[index].Dimension() != projected[index].Dimension() ||
			incremental[index].Subject() != projected[index].Subject() {
			return AllocationCandidate{}, ErrInvalidAllocationCandidate
		}
	}
	constraints := append([]AllocationConstraint(nil), spec.Constraints...)
	for _, constraint := range constraints {
		if constraint.Validate() != nil {
			return AllocationCandidate{}, ErrInvalidAllocationCandidate
		}
	}
	sort.Slice(constraints, func(i, j int) bool {
		return string(constraints[i].Code) < string(constraints[j].Code)
	})
	reasons, err := normalizeAllocationReasons(spec.Reasons)
	if err != nil {
		return AllocationCandidate{}, err
	}
	spec.LegBounds = legs
	spec.IncrementalExposure = incremental
	spec.ProjectedExposure = projected
	spec.Constraints = constraints
	spec.Reasons = reasons
	spec.GeneratedAt = spec.GeneratedAt.UTC()
	spec.ValidFrom = spec.ValidFrom.UTC()
	spec.ExpiresAt = spec.ExpiresAt.UTC()
	raw, err := canonicalAllocationCandidate(spec)
	if err != nil {
		return AllocationCandidate{}, ErrInvalidAllocationCandidate
	}
	id, _ := NewAllocationCandidateID(spec.Proposal.ID().String(),
		spec.PortfolioSnapshotID.String(), fmt.Sprint(spec.PortfolioRevision),
		spec.PolicyID.String(), fmt.Sprint(spec.PolicyVersion), string(raw))
	return AllocationCandidate{id: id, spec: spec, raw: raw}, nil
}

func normalizeAllocationReasons(values []AllocationReason) ([]AllocationReason, error) {
	result := append([]AllocationReason(nil), values...)
	seen := make(map[AllocationReason]struct{}, len(result))
	for _, value := range result {
		if value.Validate() != nil {
			return nil, ErrInvalidAllocationCandidate
		}
		if _, exists := seen[value]; exists {
			return nil, ErrInvalidAllocationCandidate
		}
		seen[value] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func (value AllocationCandidate) ID() AllocationCandidateID { return value.id }
func (value AllocationCandidate) ProposalID() strategymodel.ProposalID {
	return value.spec.Proposal.ID()
}
func (value AllocationCandidate) PortfolioSnapshotID() PortfolioSnapshotID {
	return value.spec.PortfolioSnapshotID
}
func (value AllocationCandidate) PortfolioRevision() PortfolioRevision {
	return value.spec.PortfolioRevision
}
func (value AllocationCandidate) CandidateCapital() domain.Money {
	return value.spec.CandidateCapital
}
func (value AllocationCandidate) Constraints() []AllocationConstraint {
	return append([]AllocationConstraint(nil), value.spec.Constraints...)
}
func (value AllocationCandidate) Spec() AllocationCandidateSpec {
	result := value.spec
	result.LegBounds = append([]AllocationLegBound(nil), result.LegBounds...)
	result.IncrementalExposure = append([]ExposureRecord(nil), result.IncrementalExposure...)
	result.ProjectedExposure = append([]ExposureRecord(nil), result.ProjectedExposure...)
	result.Constraints = append([]AllocationConstraint(nil), result.Constraints...)
	result.Reasons = append([]AllocationReason(nil), result.Reasons...)
	return result
}
func (value AllocationCandidate) CanonicalJSON() []byte { return append([]byte(nil), value.raw...) }

type AllocationAssessment struct {
	Outcome     AllocationOutcome
	Candidate   *AllocationCandidate
	Reasons     []AllocationReason
	Explanation string
}

func NewAllocationAssessment(input AllocationAssessment) (AllocationAssessment, error) {
	input.Explanation = strings.TrimSpace(input.Explanation)
	if input.Explanation == "" || len(input.Explanation) > MaximumExplanationBytes ||
		len(input.Reasons) == 0 || len(input.Reasons) > MaximumAllocationReasons {
		return AllocationAssessment{}, ErrInvalidAllocationCandidate
	}
	reasons, err := normalizeAllocationReasons(input.Reasons)
	if err != nil {
		return AllocationAssessment{}, err
	}
	switch input.Outcome {
	case AllocationCandidateCreated:
		if input.Candidate == nil {
			return AllocationAssessment{}, ErrInvalidAllocationCandidate
		}
	case AllocationRejected, AllocationDeferred, AllocationInvalid:
		if input.Candidate != nil {
			return AllocationAssessment{}, ErrInvalidAllocationCandidate
		}
	default:
		return AllocationAssessment{}, ErrInvalidAllocationCandidate
	}
	input.Reasons = reasons
	return input, nil
}

func canonicalAllocationCandidate(spec AllocationCandidateSpec) ([]byte, error) {
	type legWire struct {
		InstrumentID, Side, Resolution string
		Ratio                          uint32
		MaximumUnits, LotSize          int64
	}
	legs := make([]legWire, len(spec.LegBounds))
	for index, leg := range spec.LegBounds {
		legs[index] = legWire{InstrumentID: leg.InstrumentID.String(), Side: string(leg.Side),
			Ratio: uint32(leg.Ratio), Resolution: string(leg.Resolution),
			MaximumUnits: leg.MaximumUnits.Int64(), LotSize: leg.LotSize.Int64()}
	}
	reasons := make([]string, len(spec.Reasons))
	for index, reason := range spec.Reasons {
		reasons[index] = string(reason)
	}
	type constraintWire struct {
		Code, Explanation string
		Before, After     moneyWire
	}
	constraints := make([]constraintWire, len(spec.Constraints))
	for index, constraint := range spec.Constraints {
		constraints[index] = constraintWire{
			Code: string(constraint.Code), Explanation: constraint.Explanation,
			Before: moneyValue(constraint.Before), After: moneyValue(constraint.After),
		}
	}
	incremental := make([]string, len(spec.IncrementalExposure))
	for index, value := range spec.IncrementalExposure {
		incremental[index] = exposureRecordKey(value)
	}
	projected := make([]string, len(spec.ProjectedExposure))
	for index, value := range spec.ProjectedExposure {
		projected[index] = exposureRecordKey(value)
	}
	return json.Marshal(struct {
		SchemaVersion, ProposalID, PortfolioID, SnapshotID, StrategyAllocationID string
		PolicyID, ConfigurationHash, GeneratedAt, ValidFrom, ExpiresAt           string
		PortfolioRevision, PolicyVersion                                         uint64
		SizingKind                                                               string
		SizingBPS                                                                int32
		CandidateCapital, ReserveImpact                                          moneyWire
		CandidatePremium, CandidateRiskBudget                                    string
		Legs                                                                     []legWire
		Reasons                                                                  []string
		Constraints                                                              []constraintWire
		IncrementalExposure, ProjectedExposure                                   []string
		Rounding                                                                 RoundingEvidence
	}{
		SchemaVersion: spec.SchemaVersion, ProposalID: spec.Proposal.ID().String(),
		PortfolioID: spec.PortfolioID.String(), SnapshotID: spec.PortfolioSnapshotID.String(),
		PortfolioRevision:    uint64(spec.PortfolioRevision),
		StrategyAllocationID: spec.StrategyAllocationID.String(), PolicyID: spec.PolicyID.String(),
		PolicyVersion: uint64(spec.PolicyVersion), SizingKind: string(spec.RequestedSizing.Kind),
		SizingBPS: spec.RequestedSizing.ValueBPS, CandidateCapital: moneyValue(spec.CandidateCapital),
		ReserveImpact:       moneyValue(spec.ReserveImpact),
		CandidatePremium:    measuredKey(spec.CandidatePremium),
		CandidateRiskBudget: measuredKey(spec.CandidateRiskBudget),
		Legs:                legs, Reasons: reasons, Constraints: constraints,
		IncrementalExposure: incremental, ProjectedExposure: projected, Rounding: spec.Rounding,
		ConfigurationHash: spec.ConfigurationHash.String(),
		GeneratedAt:       spec.GeneratedAt.Format(time.RFC3339Nano),
		ValidFrom:         spec.ValidFrom.Format(time.RFC3339Nano),
		ExpiresAt:         spec.ExpiresAt.Format(time.RFC3339Nano),
	})
}

func exposureRecordKey(value ExposureRecord) string {
	spec := value.spec
	return strings.Join([]string{
		string(spec.Dimension), spec.Subject, measuredKey(spec.Gross),
		measuredKey(spec.NetDirectional), measuredKey(spec.PremiumAtRisk),
		measuredKey(spec.Long), measuredKey(spec.Short), measuredKey(spec.PremiumPaid),
		measuredKey(spec.PremiumReceived), measuredKey(spec.MaximumLoss),
		string(spec.LossBound), fmt.Sprint(spec.Overnight),
	}, "|")
}
