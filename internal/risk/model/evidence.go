package model

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

const (
	MaximumEvidenceEntries          = 32
	MaximumEvidenceBytes            = 64 << 10
	MaximumEvidenceExplanationBytes = 1024
)

var ErrInvalidRiskEvidence = errors.New("invalid risk evidence")

type ComparisonOperator string

const (
	CompareEqual          ComparisonOperator = "EQUAL"
	CompareNotEqual       ComparisonOperator = "NOT_EQUAL"
	CompareLessThan       ComparisonOperator = "LESS_THAN"
	CompareLessOrEqual    ComparisonOperator = "LESS_OR_EQUAL"
	CompareGreaterThan    ComparisonOperator = "GREATER_THAN"
	CompareGreaterOrEqual ComparisonOperator = "GREATER_OR_EQUAL"
	CompareAvailability   ComparisonOperator = "AVAILABILITY"
)

type EvidenceValueKind string

const (
	EvidenceMoney       EvidenceValueKind = "MONEY"
	EvidenceInteger     EvidenceValueKind = "INTEGER"
	EvidenceBasisPoints EvidenceValueKind = "BASIS_POINTS"
	EvidenceRational    EvidenceValueKind = "RATIONAL"
	EvidenceState       EvidenceValueKind = "STATE"
	EvidenceTimestamp   EvidenceValueKind = "TIMESTAMP"
	EvidenceIdentity    EvidenceValueKind = "IDENTITY"
)

type EvidenceValue struct {
	Kind         EvidenceValueKind
	Availability portfoliomodel.Availability
	Money        domain.Money
	Integer      int64
	Numerator    uint64
	Denominator  uint64
	Text         string
	Timestamp    time.Time
}

func (value EvidenceValue) Validate() error {
	if value.Availability.Validate() != nil {
		return ErrInvalidRiskEvidence
	}
	switch value.Kind {
	case EvidenceMoney, EvidenceInteger, EvidenceBasisPoints, EvidenceRational,
		EvidenceState, EvidenceTimestamp, EvidenceIdentity:
	default:
		return ErrInvalidRiskEvidence
	}
	if value.Availability != portfoliomodel.AvailabilityKnown {
		if !value.Money.IsZeroValue() || value.Integer != 0 || value.Numerator != 0 ||
			value.Denominator != 0 || value.Text != "" || !value.Timestamp.IsZero() {
			return ErrInvalidRiskEvidence
		}
		return nil
	}
	switch value.Kind {
	case EvidenceMoney:
		if value.Money.IsZeroValue() || value.Integer != 0 || value.Numerator != 0 ||
			value.Denominator != 0 || value.Text != "" || !value.Timestamp.IsZero() {
			return ErrInvalidRiskEvidence
		}
	case EvidenceInteger:
		if !value.Money.IsZeroValue() || value.Numerator != 0 || value.Denominator != 0 ||
			value.Text != "" || !value.Timestamp.IsZero() {
			return ErrInvalidRiskEvidence
		}
	case EvidenceBasisPoints:
		if !value.Money.IsZeroValue() || value.Integer < 0 || value.Integer > 10000 ||
			value.Numerator != 0 || value.Denominator != 0 || value.Text != "" ||
			!value.Timestamp.IsZero() {
			return ErrInvalidRiskEvidence
		}
	case EvidenceRational:
		if !value.Money.IsZeroValue() || value.Integer != 0 || value.Denominator == 0 ||
			value.Text != "" || !value.Timestamp.IsZero() {
			return ErrInvalidRiskEvidence
		}
	case EvidenceState, EvidenceIdentity:
		if !value.Money.IsZeroValue() || value.Integer != 0 || value.Numerator != 0 ||
			value.Denominator != 0 || strings.TrimSpace(value.Text) == "" ||
			len(value.Text) > 256 || !value.Timestamp.IsZero() {
			return ErrInvalidRiskEvidence
		}
	case EvidenceTimestamp:
		if !value.Money.IsZeroValue() || value.Integer != 0 || value.Numerator != 0 ||
			value.Denominator != 0 || value.Text != "" || value.Timestamp.IsZero() {
			return ErrInvalidRiskEvidence
		}
	}
	return nil
}

type EvidenceSubject string

const (
	SubjectPortfolio     EvidenceSubject = "PORTFOLIO"
	SubjectStrategy      EvidenceSubject = "STRATEGY"
	SubjectInstrument    EvidenceSubject = "INSTRUMENT"
	SubjectUnderlying    EvidenceSubject = "UNDERLYING"
	SubjectExposureGroup EvidenceSubject = "EXPOSURE_GROUP"
	SubjectProposal      EvidenceSubject = "PROPOSAL"
	SubjectAllocation    EvidenceSubject = "ALLOCATION"
	SubjectRule          EvidenceSubject = "RULE"
)

type RiskEvidenceSpec struct {
	Code              string
	Observed          EvidenceValue
	Limit             EvidenceValue
	Projected         EvidenceValue
	RemainingHeadroom EvidenceValue
	Unit              string
	Comparison        ComparisonOperator
	SubjectType       EvidenceSubject
	SubjectIdentity   string
	SourceSnapshotID  portfoliomodel.PortfolioSnapshotID
	SourceProposalID  strategymodel.ProposalID
	FormulaVersion    string
	EvidenceAt        time.Time
	Explanation       string
}

type RiskEvidence struct {
	spec RiskEvidenceSpec
	raw  []byte
}

func NewRiskEvidence(spec RiskEvidenceSpec) (RiskEvidence, error) {
	spec.Code = strings.TrimSpace(spec.Code)
	spec.Unit = strings.TrimSpace(spec.Unit)
	spec.SubjectIdentity = strings.TrimSpace(spec.SubjectIdentity)
	spec.FormulaVersion = strings.TrimSpace(spec.FormulaVersion)
	spec.Explanation = strings.TrimSpace(spec.Explanation)
	if !rulePattern.MatchString(spec.Code) || spec.Observed.Validate() != nil ||
		spec.Limit.Validate() != nil || spec.Projected.Validate() != nil ||
		spec.RemainingHeadroom.Validate() != nil || spec.Unit == "" || len(spec.Unit) > 32 ||
		spec.SubjectIdentity == "" || len(spec.SubjectIdentity) > 256 ||
		spec.SourceSnapshotID.IsZero() || spec.SourceProposalID.IsZero() ||
		spec.FormulaVersion == "" || len(spec.FormulaVersion) > 64 ||
		spec.EvidenceAt.IsZero() || spec.Explanation == "" ||
		len(spec.Explanation) > MaximumEvidenceExplanationBytes {
		return RiskEvidence{}, ErrInvalidRiskEvidence
	}
	switch spec.Comparison {
	case CompareEqual, CompareNotEqual, CompareLessThan, CompareLessOrEqual,
		CompareGreaterThan, CompareGreaterOrEqual, CompareAvailability:
	default:
		return RiskEvidence{}, ErrInvalidRiskEvidence
	}
	switch spec.SubjectType {
	case SubjectPortfolio, SubjectStrategy, SubjectInstrument, SubjectUnderlying,
		SubjectExposureGroup, SubjectProposal, SubjectAllocation, SubjectRule:
	default:
		return RiskEvidence{}, ErrInvalidRiskEvidence
	}
	spec.EvidenceAt = spec.EvidenceAt.UTC()
	raw, err := canonicalEvidence(spec)
	if err != nil || len(raw) > MaximumEvidenceBytes {
		return RiskEvidence{}, ErrInvalidRiskEvidence
	}
	return RiskEvidence{spec: spec, raw: raw}, nil
}

func (value RiskEvidence) Spec() RiskEvidenceSpec { return value.spec }
func (value RiskEvidence) CanonicalJSON() []byte  { return append([]byte(nil), value.raw...) }
func (value RiskEvidence) Code() string           { return value.spec.Code }

type evidenceValueWire struct {
	Kind, Availability, Currency, Text, Timestamp string
	MoneyMinor, Integer                           int64
	Numerator, Denominator                        uint64
}

func evidenceValue(value EvidenceValue) evidenceValueWire {
	result := evidenceValueWire{
		Kind: string(value.Kind), Availability: string(value.Availability),
		Integer: value.Integer, Numerator: value.Numerator, Denominator: value.Denominator,
		Text: value.Text,
	}
	if !value.Money.IsZeroValue() {
		result.MoneyMinor = value.Money.MinorUnits()
		result.Currency = value.Money.Currency().String()
	}
	if !value.Timestamp.IsZero() {
		result.Timestamp = value.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	return result
}

func canonicalEvidence(spec RiskEvidenceSpec) ([]byte, error) {
	return json.Marshal(struct {
		Code, Unit, Comparison, SubjectType, SubjectIdentity            string
		SnapshotID, ProposalID, FormulaVersion, EvidenceAt, Explanation string
		Observed, Limit, Projected, Remaining                           evidenceValueWire
	}{
		Code: spec.Code, Unit: spec.Unit, Comparison: string(spec.Comparison),
		SubjectType: string(spec.SubjectType), SubjectIdentity: spec.SubjectIdentity,
		SnapshotID: spec.SourceSnapshotID.String(), ProposalID: spec.SourceProposalID.String(),
		FormulaVersion: spec.FormulaVersion, EvidenceAt: spec.EvidenceAt.Format(time.RFC3339Nano),
		Explanation: spec.Explanation, Observed: evidenceValue(spec.Observed),
		Limit: evidenceValue(spec.Limit), Projected: evidenceValue(spec.Projected),
		Remaining: evidenceValue(spec.RemainingHeadroom),
	})
}

type RuleResultStatus string

const (
	RulePass                 RuleResultStatus = "PASS"
	RuleViolation            RuleResultStatus = "VIOLATION"
	RuleModificationRequired RuleResultStatus = "MODIFICATION_REQUIRED"
	RuleDefer                RuleResultStatus = "DEFER"
	RuleError                RuleResultStatus = "ERROR"
)

type RuleResultSpec struct {
	RuleID            RiskRuleID
	RuleVersion       RiskRuleVersion
	ConfigurationHash RiskConfigurationHash
	Status            RuleResultStatus
	ReasonCode        string
	Severity          RuleSeverity
	Effect            RuleEffect
	Evidence          []RiskEvidence
	Adjustment        *RuleAdjustment
	EvaluatedAt       time.Time
}

type RuleAdjustment struct {
	MaximumCapital domain.Money
	LegBounds      []portfoliomodel.AllocationLegBound
	Constraints    []portfoliomodel.AllocationConstraint
	ValidUntil     time.Time
}

func (value RuleAdjustment) Validate() error {
	if value.MaximumCapital.IsZeroValue() || value.MaximumCapital.MinorUnits() < 0 ||
		len(value.LegBounds) == 0 || len(value.LegBounds) > portfoliomodel.MaximumAllocationLegs ||
		len(value.Constraints) == 0 || len(value.Constraints) > portfoliomodel.MaximumAllocationConstraints ||
		value.ValidUntil.IsZero() {
		return ErrInvalidRiskEvidence
	}
	for _, leg := range value.LegBounds {
		if leg.Validate() != nil {
			return ErrInvalidRiskEvidence
		}
	}
	for _, constraint := range value.Constraints {
		if constraint.Validate() != nil {
			return ErrInvalidRiskEvidence
		}
	}
	return nil
}

type RuleResult struct{ spec RuleResultSpec }

func NewRuleResult(spec RuleResultSpec) (RuleResult, error) {
	spec.ReasonCode = strings.TrimSpace(spec.ReasonCode)
	if spec.RuleID.Validate() != nil || spec.RuleVersion.Validate() != nil ||
		spec.ConfigurationHash.IsZero() || !rulePattern.MatchString(spec.ReasonCode) ||
		spec.Severity.Validate() != nil || spec.Effect.Validate() != nil ||
		spec.EvaluatedAt.IsZero() || len(spec.Evidence) > MaximumEvidenceEntries {
		return RuleResult{}, ErrInvalidRiskEvidence
	}
	switch spec.Status {
	case RulePass:
		if spec.Effect != EffectNone || spec.Adjustment != nil {
			return RuleResult{}, ErrInvalidRiskEvidence
		}
	case RuleViolation:
		if spec.Effect != EffectReject && spec.Effect != EffectTripCircuitBreaker &&
			spec.Effect != EffectActivateKillSwitch || spec.Adjustment != nil {
			return RuleResult{}, ErrInvalidRiskEvidence
		}
	case RuleModificationRequired:
		if spec.Effect != EffectModify || spec.Adjustment == nil || spec.Adjustment.Validate() != nil {
			return RuleResult{}, ErrInvalidRiskEvidence
		}
	case RuleDefer:
		if spec.Effect != EffectDefer || spec.Adjustment != nil {
			return RuleResult{}, ErrInvalidRiskEvidence
		}
	case RuleError:
		if spec.Effect != EffectDefer || spec.Adjustment != nil {
			return RuleResult{}, ErrInvalidRiskEvidence
		}
	default:
		return RuleResult{}, ErrInvalidRiskEvidence
	}
	evidence := append([]RiskEvidence(nil), spec.Evidence...)
	seen := make(map[string]struct{}, len(evidence))
	for _, value := range evidence {
		if len(value.raw) == 0 {
			return RuleResult{}, ErrInvalidRiskEvidence
		}
		if _, exists := seen[value.Code()]; exists {
			return RuleResult{}, ErrInvalidRiskEvidence
		}
		seen[value.Code()] = struct{}{}
	}
	sort.Slice(evidence, func(i, j int) bool { return evidence[i].Code() < evidence[j].Code() })
	spec.Evidence = evidence
	if spec.Adjustment != nil {
		adjustment := *spec.Adjustment
		adjustment.LegBounds = append([]portfoliomodel.AllocationLegBound(nil), adjustment.LegBounds...)
		adjustment.Constraints = append([]portfoliomodel.AllocationConstraint(nil), adjustment.Constraints...)
		sort.Slice(adjustment.LegBounds, func(i, j int) bool {
			return adjustment.LegBounds[i].InstrumentID.String() < adjustment.LegBounds[j].InstrumentID.String()
		})
		sort.Slice(adjustment.Constraints, func(i, j int) bool {
			return adjustment.Constraints[i].Code < adjustment.Constraints[j].Code
		})
		for index := 1; index < len(adjustment.LegBounds); index++ {
			if adjustment.LegBounds[index-1].InstrumentID == adjustment.LegBounds[index].InstrumentID {
				return RuleResult{}, ErrInvalidRiskEvidence
			}
		}
		for index := 1; index < len(adjustment.Constraints); index++ {
			if adjustment.Constraints[index-1].Code == adjustment.Constraints[index].Code {
				return RuleResult{}, ErrInvalidRiskEvidence
			}
		}
		adjustment.ValidUntil = adjustment.ValidUntil.UTC()
		spec.Adjustment = &adjustment
	}
	spec.EvaluatedAt = spec.EvaluatedAt.UTC()
	return RuleResult{spec: spec}, nil
}

func (value RuleResult) Spec() RuleResultSpec {
	result := value.spec
	result.Evidence = append([]RiskEvidence(nil), result.Evidence...)
	if result.Adjustment != nil {
		adjustment := *result.Adjustment
		adjustment.LegBounds = append([]portfoliomodel.AllocationLegBound(nil), adjustment.LegBounds...)
		adjustment.Constraints = append([]portfoliomodel.AllocationConstraint(nil), adjustment.Constraints...)
		result.Adjustment = &adjustment
	}
	return result
}
func (value RuleResult) RuleID() RiskRuleID       { return value.spec.RuleID }
func (value RuleResult) Status() RuleResultStatus { return value.spec.Status }

type RiskRuleInputSpec struct {
	SchemaVersion       string
	Proposal            strategymodel.TradeProposal
	PortfolioSnapshot   portfoliomodel.PortfolioSnapshot
	AllocationCandidate portfoliomodel.AllocationCandidate
	StrategyAllocation  portfoliomodel.StrategyAllocation
	TradingDate         domain.CivilDate
	SessionContext      string
	RiskPolicyID        RiskPolicyID
	RiskPolicyVersion   RiskPolicyVersion
	RuleConfiguration   RiskRuleConfiguration
	EvaluatedAt         time.Time
}

type RiskRuleInput struct{ spec RiskRuleInputSpec }

func NewRiskRuleInput(spec RiskRuleInputSpec) (RiskRuleInput, error) {
	if strings.TrimSpace(spec.SchemaVersion) == "" || spec.Proposal.IsZero() ||
		spec.PortfolioSnapshot.ID().IsZero() || spec.AllocationCandidate.ID().IsZero() ||
		spec.StrategyAllocation.ID().IsZero() || spec.TradingDate.IsZero() ||
		strings.TrimSpace(spec.SessionContext) == "" || len(spec.SessionContext) > 128 ||
		spec.RiskPolicyID.IsZero() || spec.RiskPolicyVersion.Validate() != nil ||
		spec.EvaluatedAt.IsZero() ||
		spec.AllocationCandidate.ProposalID() != spec.Proposal.ID() ||
		spec.AllocationCandidate.PortfolioSnapshotID() != spec.PortfolioSnapshot.ID() ||
		spec.AllocationCandidate.PortfolioRevision() != spec.PortfolioSnapshot.Revision() {
		return RiskRuleInput{}, ErrInvalidRiskEvidence
	}
	configuration, err := NewRiskRuleConfiguration(spec.RuleConfiguration)
	if err != nil {
		return RiskRuleInput{}, err
	}
	spec.RuleConfiguration = configuration
	spec.EvaluatedAt = spec.EvaluatedAt.UTC()
	return RiskRuleInput{spec: spec}, nil
}

func (value RiskRuleInput) Spec() RiskRuleInputSpec { return value.spec }
