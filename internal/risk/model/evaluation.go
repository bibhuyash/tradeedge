package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

const (
	MaximumViolationsPerEvaluation = 50
	MaximumTechnicalErrors         = 50
	MaximumEvaluationBytes         = 256 << 10
)

var (
	ErrInvalidRiskEvaluation = errors.New("invalid risk evaluation")
	ErrInvalidRuleResult     = errors.New("invalid risk rule result")
)

type ViolationReason string

func NewViolationReason(value string) (ViolationReason, error) {
	value = strings.TrimSpace(value)
	if !rulePattern.MatchString(value) {
		return "", ErrInvalidRiskEvaluation
	}
	return ViolationReason(value), nil
}

type RiskViolationSpec struct {
	SchemaVersion     string
	EvaluationID      RiskEvaluationID
	RuleID            RiskRuleID
	RuleVersion       RiskRuleVersion
	ReasonCode        ViolationReason
	Severity          RuleSeverity
	Effect            RuleEffect
	Evidence          []RiskEvidence
	GeneratedAt       time.Time
	ConfigurationHash RiskConfigurationHash
}

type RiskViolation struct {
	id   RiskViolationID
	spec RiskViolationSpec
	raw  []byte
}

func NewRiskViolation(spec RiskViolationSpec) (RiskViolation, error) {
	if strings.TrimSpace(spec.SchemaVersion) == "" || spec.EvaluationID.IsZero() ||
		spec.RuleID.Validate() != nil || spec.RuleVersion.Validate() != nil ||
		spec.Severity.Validate() != nil || spec.Effect.Validate() != nil ||
		spec.GeneratedAt.IsZero() || spec.ConfigurationHash.IsZero() ||
		len(spec.Evidence) == 0 || len(spec.Evidence) > MaximumEvidenceEntries {
		return RiskViolation{}, ErrInvalidRiskEvaluation
	}
	if _, err := NewViolationReason(string(spec.ReasonCode)); err != nil ||
		spec.Effect == EffectNone {
		return RiskViolation{}, ErrInvalidRiskEvaluation
	}
	evidence := append([]RiskEvidence(nil), spec.Evidence...)
	sort.Slice(evidence, func(i, j int) bool { return evidence[i].Code() < evidence[j].Code() })
	spec.Evidence = evidence
	spec.GeneratedAt = spec.GeneratedAt.UTC()
	raw, err := canonicalViolation(spec)
	if err != nil {
		return RiskViolation{}, ErrInvalidRiskEvaluation
	}
	id, _ := NewRiskViolationID(spec.EvaluationID.String(), string(spec.RuleID),
		fmt.Sprint(spec.RuleVersion), string(spec.ReasonCode), string(raw))
	return RiskViolation{id: id, spec: spec, raw: raw}, nil
}

func (value RiskViolation) ID() RiskViolationID            { return value.id }
func (value RiskViolation) EvaluationID() RiskEvaluationID { return value.spec.EvaluationID }
func (value RiskViolation) RuleID() RiskRuleID             { return value.spec.RuleID }
func (value RiskViolation) Effect() RuleEffect             { return value.spec.Effect }
func (value RiskViolation) Spec() RiskViolationSpec {
	result := value.spec
	result.Evidence = append([]RiskEvidence(nil), result.Evidence...)
	return result
}
func (value RiskViolation) CanonicalJSON() []byte { return append([]byte(nil), value.raw...) }

type TechnicalErrorCode string

const (
	TechnicalRuleFailure       TechnicalErrorCode = "RULE_FAILURE"
	TechnicalRuleCancelled     TechnicalErrorCode = "RULE_CANCELLED"
	TechnicalUnavailableInput  TechnicalErrorCode = "UNAVAILABLE_INPUT"
	TechnicalInvalidRuleOutput TechnicalErrorCode = "INVALID_RULE_OUTPUT"
)

type TechnicalRuleError struct {
	RuleID      RiskRuleID
	RuleVersion RiskRuleVersion
	Code        TechnicalErrorCode
	OccurredAt  time.Time
}

func (value TechnicalRuleError) Validate() error {
	if value.RuleID.Validate() != nil || value.RuleVersion.Validate() != nil ||
		value.OccurredAt.IsZero() {
		return ErrInvalidRiskEvaluation
	}
	switch value.Code {
	case TechnicalRuleFailure, TechnicalRuleCancelled,
		TechnicalUnavailableInput, TechnicalInvalidRuleOutput:
		return nil
	default:
		return ErrInvalidRiskEvaluation
	}
}

type RiskEvaluationSpec struct {
	SchemaVersion         string
	ID                    RiskEvaluationID
	ProposalID            strategymodel.ProposalID
	AllocationCandidateID portfoliomodel.AllocationCandidateID
	PortfolioSnapshotID   portfoliomodel.PortfolioSnapshotID
	PortfolioRevision     portfoliomodel.PortfolioRevision
	RiskPolicyID          RiskPolicyID
	RiskPolicyVersion     RiskPolicyVersion
	ConfigurationHash     RiskConfigurationHash
	RuleResults           []RuleResult
	Violations            []RiskViolation
	TechnicalErrors       []TechnicalRuleError
	StartedAt             time.Time
	CompletedAt           time.Time
}

type RiskEvaluation struct {
	spec     RiskEvaluationSpec
	evidence EvidenceChecksum
	raw      []byte
}

func DeriveRiskEvaluationID(spec RiskEvaluationSpec) (RiskEvaluationID, error) {
	if strings.TrimSpace(spec.SchemaVersion) == "" || spec.ProposalID.IsZero() || spec.AllocationCandidateID.IsZero() ||
		spec.PortfolioSnapshotID.IsZero() || spec.PortfolioRevision.Validate() != nil ||
		spec.RiskPolicyID.IsZero() || spec.RiskPolicyVersion.Validate() != nil ||
		spec.ConfigurationHash.IsZero() || spec.StartedAt.IsZero() ||
		spec.CompletedAt.Before(spec.StartedAt) || len(spec.RuleResults) == 0 ||
		len(spec.RuleResults) > MaximumRulesPerPolicy {
		return RiskEvaluationID{}, ErrInvalidRiskEvaluation
	}
	keys := []string{
		strings.TrimSpace(spec.SchemaVersion), spec.ProposalID.String(), spec.AllocationCandidateID.String(),
		spec.PortfolioSnapshotID.String(), fmt.Sprint(spec.PortfolioRevision),
		spec.RiskPolicyID.String(), fmt.Sprint(spec.RiskPolicyVersion),
		spec.ConfigurationHash.String(), spec.StartedAt.UTC().Format(time.RFC3339Nano),
		spec.CompletedAt.UTC().Format(time.RFC3339Nano),
	}
	for _, result := range spec.RuleResults {
		item := result.Spec()
		keys = append(keys, string(item.RuleID), fmt.Sprint(item.RuleVersion),
			string(item.Status), item.ReasonCode, string(item.Effect),
			item.ConfigurationHash.String())
		if item.Adjustment != nil {
			keys = append(keys, canonicalAdjustment(*item.Adjustment))
		}
		for _, evidence := range item.Evidence {
			checksum, _ := NewEvidenceChecksum(evidence.CanonicalJSON())
			keys = append(keys, evidence.Code(), checksum.String())
		}
	}
	for _, technical := range spec.TechnicalErrors {
		keys = append(keys, string(technical.RuleID), fmt.Sprint(technical.RuleVersion),
			string(technical.Code), technical.OccurredAt.UTC().Format(time.RFC3339Nano))
	}
	return NewRiskEvaluationID(keys...)
}

func NewRiskEvaluation(spec RiskEvaluationSpec) (RiskEvaluation, error) {
	spec.SchemaVersion = strings.TrimSpace(spec.SchemaVersion)
	expected, err := DeriveRiskEvaluationID(spec)
	if err != nil || spec.SchemaVersion == "" || spec.ID != expected ||
		len(spec.Violations) > MaximumViolationsPerEvaluation ||
		len(spec.TechnicalErrors) > MaximumTechnicalErrors {
		return RiskEvaluation{}, ErrInvalidRiskEvaluation
	}
	results := append([]RuleResult(nil), spec.RuleResults...)
	seenRules := make(map[RiskRuleID]struct{}, len(results))
	for _, result := range results {
		if _, exists := seenRules[result.RuleID()]; exists {
			return RiskEvaluation{}, ErrInvalidRiskEvaluation
		}
		seenRules[result.RuleID()] = struct{}{}
	}
	violations := append([]RiskViolation(nil), spec.Violations...)
	seenViolations := make(map[RiskViolationID]struct{}, len(violations))
	for _, violation := range violations {
		if violation.EvaluationID() != spec.ID {
			return RiskEvaluation{}, ErrInvalidRiskEvaluation
		}
		if _, exists := seenViolations[violation.ID()]; exists {
			return RiskEvaluation{}, ErrInvalidRiskEvaluation
		}
		seenViolations[violation.ID()] = struct{}{}
		var matching *RuleResultSpec
		for _, result := range results {
			if result.RuleID() == violation.RuleID() {
				item := result.Spec()
				matching = &item
				break
			}
		}
		violationSpec := violation.Spec()
		if matching == nil ||
			(matching.Status != RuleViolation && matching.Status != RuleModificationRequired) ||
			matching.RuleVersion != violationSpec.RuleVersion ||
			matching.ConfigurationHash != violationSpec.ConfigurationHash ||
			matching.ReasonCode != string(violationSpec.ReasonCode) ||
			matching.Severity != violationSpec.Severity ||
			matching.Effect != violationSpec.Effect ||
			!equalEvidence(matching.Evidence, violationSpec.Evidence) {
			return RiskEvaluation{}, ErrInvalidRiskEvaluation
		}
	}
	sort.Slice(violations, func(i, j int) bool {
		leftOrder := ruleOrder(results, violations[i].RuleID())
		rightOrder := ruleOrder(results, violations[j].RuleID())
		if leftOrder == rightOrder {
			return violations[i].ID().String() < violations[j].ID().String()
		}
		return leftOrder < rightOrder
	})
	technical := append([]TechnicalRuleError(nil), spec.TechnicalErrors...)
	for index := range technical {
		if technical[index].Validate() != nil {
			return RiskEvaluation{}, ErrInvalidRiskEvaluation
		}
		technical[index].OccurredAt = technical[index].OccurredAt.UTC()
	}
	sort.Slice(technical, func(i, j int) bool {
		return string(technical[i].RuleID) < string(technical[j].RuleID)
	})
	spec.RuleResults = results
	spec.Violations = violations
	spec.TechnicalErrors = technical
	spec.StartedAt = spec.StartedAt.UTC()
	spec.CompletedAt = spec.CompletedAt.UTC()
	raw, err := canonicalEvaluation(spec)
	if err != nil || len(raw) > MaximumEvaluationBytes {
		return RiskEvaluation{}, ErrInvalidRiskEvaluation
	}
	evidence, _ := NewEvidenceChecksum(raw)
	return RiskEvaluation{spec: spec, evidence: evidence, raw: raw}, nil
}

func equalEvidence(left, right []RiskEvidence) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		leftChecksum, _ := NewEvidenceChecksum(left[index].CanonicalJSON())
		rightChecksum, _ := NewEvidenceChecksum(right[index].CanonicalJSON())
		if leftChecksum != rightChecksum {
			return false
		}
	}
	return true
}

func ruleOrder(results []RuleResult, id RiskRuleID) int {
	for index, result := range results {
		if result.RuleID() == id {
			return index
		}
	}
	return len(results)
}

func (value RiskEvaluation) ID() RiskEvaluationID                 { return value.spec.ID }
func (value RiskEvaluation) ProposalID() strategymodel.ProposalID { return value.spec.ProposalID }
func (value RiskEvaluation) AllocationCandidateID() portfoliomodel.AllocationCandidateID {
	return value.spec.AllocationCandidateID
}
func (value RiskEvaluation) EvidenceChecksum() EvidenceChecksum { return value.evidence }
func (value RiskEvaluation) RuleResults() []RuleResult {
	return append([]RuleResult(nil), value.spec.RuleResults...)
}
func (value RiskEvaluation) Violations() []RiskViolation {
	return append([]RiskViolation(nil), value.spec.Violations...)
}
func (value RiskEvaluation) Spec() RiskEvaluationSpec {
	result := value.spec
	result.RuleResults = append([]RuleResult(nil), result.RuleResults...)
	result.Violations = append([]RiskViolation(nil), result.Violations...)
	result.TechnicalErrors = append([]TechnicalRuleError(nil), result.TechnicalErrors...)
	return result
}
func (value RiskEvaluation) CanonicalJSON() []byte { return append([]byte(nil), value.raw...) }

func canonicalViolation(spec RiskViolationSpec) ([]byte, error) {
	evidence := make([]json.RawMessage, len(spec.Evidence))
	for index, value := range spec.Evidence {
		evidence[index] = value.CanonicalJSON()
	}
	return json.Marshal(struct {
		SchemaVersion, EvaluationID, RuleID, ReasonCode, Severity, Effect string
		RuleVersion                                                       uint64
		Evidence                                                          []json.RawMessage
		GeneratedAt, ConfigurationHash                                    string
	}{
		SchemaVersion: spec.SchemaVersion, EvaluationID: spec.EvaluationID.String(),
		RuleID: string(spec.RuleID), RuleVersion: uint64(spec.RuleVersion),
		ReasonCode: string(spec.ReasonCode), Severity: string(spec.Severity),
		Effect: string(spec.Effect), Evidence: evidence,
		GeneratedAt:       spec.GeneratedAt.Format(time.RFC3339Nano),
		ConfigurationHash: spec.ConfigurationHash.String(),
	})
}

func canonicalEvaluation(spec RiskEvaluationSpec) ([]byte, error) {
	type resultWire struct {
		RuleID, Status, Reason, Severity, Effect, ConfigurationHash, Adjustment string
		RuleVersion                                                             uint64
		EvidenceChecksums                                                       []string
	}
	results := make([]resultWire, len(spec.RuleResults))
	for index, value := range spec.RuleResults {
		item := value.Spec()
		evidenceChecksums := make([]string, len(item.Evidence))
		for evidenceIndex, evidence := range item.Evidence {
			checksum, _ := NewEvidenceChecksum(evidence.CanonicalJSON())
			evidenceChecksums[evidenceIndex] = checksum.String()
		}
		results[index] = resultWire{RuleID: string(item.RuleID), RuleVersion: uint64(item.RuleVersion),
			Status: string(item.Status), Reason: item.ReasonCode, Severity: string(item.Severity),
			Effect: string(item.Effect), ConfigurationHash: item.ConfigurationHash.String(),
			EvidenceChecksums: evidenceChecksums}
		if item.Adjustment != nil {
			results[index].Adjustment = canonicalAdjustment(*item.Adjustment)
		}
	}
	violations := make([]string, len(spec.Violations))
	for index, value := range spec.Violations {
		violations[index] = value.ID().String()
	}
	type technicalWire struct {
		RuleID, Code, OccurredAt string
		RuleVersion              uint64
	}
	technical := make([]technicalWire, len(spec.TechnicalErrors))
	for index, value := range spec.TechnicalErrors {
		technical[index] = technicalWire{
			RuleID: string(value.RuleID), RuleVersion: uint64(value.RuleVersion),
			Code: string(value.Code), OccurredAt: value.OccurredAt.Format(time.RFC3339Nano),
		}
	}
	return json.Marshal(struct {
		SchemaVersion, ID, ProposalID, CandidateID, SnapshotID, PolicyID, ConfigurationHash string
		PortfolioRevision, PolicyVersion                                                    uint64
		Results                                                                             []resultWire
		Violations                                                                          []string
		TechnicalErrors                                                                     []technicalWire
		StartedAt, CompletedAt                                                              string
	}{
		SchemaVersion: spec.SchemaVersion, ID: spec.ID.String(),
		ProposalID: spec.ProposalID.String(), CandidateID: spec.AllocationCandidateID.String(),
		SnapshotID: spec.PortfolioSnapshotID.String(), PortfolioRevision: uint64(spec.PortfolioRevision),
		PolicyID: spec.RiskPolicyID.String(), PolicyVersion: uint64(spec.RiskPolicyVersion),
		ConfigurationHash: spec.ConfigurationHash.String(), Results: results,
		Violations: violations, TechnicalErrors: technical,
		StartedAt:   spec.StartedAt.Format(time.RFC3339Nano),
		CompletedAt: spec.CompletedAt.Format(time.RFC3339Nano),
	})
}

func canonicalAdjustment(value RuleAdjustment) string {
	parts := []string{
		fmt.Sprintf("%s:%d", value.MaximumCapital.Currency(), value.MaximumCapital.MinorUnits()),
		value.ValidUntil.UTC().Format(time.RFC3339Nano),
	}
	for _, leg := range value.LegBounds {
		parts = append(parts, fmt.Sprintf("%s:%s:%d:%s:%d:%d", leg.InstrumentID, leg.Side,
			leg.Ratio, leg.Resolution, leg.MaximumUnits.Int64(), leg.LotSize.Int64()))
	}
	for _, constraint := range value.Constraints {
		parts = append(parts, fmt.Sprintf("%s:%d:%d:%s", constraint.Code,
			constraint.Before.MinorUnits(), constraint.After.MinorUnits(), constraint.Explanation))
	}
	return strings.Join(parts, "|")
}
