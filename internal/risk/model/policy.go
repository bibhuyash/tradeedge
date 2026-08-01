package model

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/canonicaljson"
)

const (
	MaximumRulesPerPolicy         = 50
	MaximumRuleConfigurationBytes = 64 << 10
)

var ErrInvalidRiskPolicy = errors.New("invalid risk policy")

type RuleSeverity string

const (
	SeverityInfo     RuleSeverity = "INFO"
	SeverityWarning  RuleSeverity = "WARNING"
	SeverityBlocking RuleSeverity = "BLOCKING"
	SeverityCritical RuleSeverity = "CRITICAL"
)

func (value RuleSeverity) Validate() error {
	switch value {
	case SeverityInfo, SeverityWarning, SeverityBlocking, SeverityCritical:
		return nil
	default:
		return ErrInvalidRiskPolicy
	}
}

type RuleEffect string

const (
	EffectNone               RuleEffect = "NONE"
	EffectModify             RuleEffect = "MODIFY"
	EffectDefer              RuleEffect = "DEFER"
	EffectReject             RuleEffect = "REJECT"
	EffectTripCircuitBreaker RuleEffect = "TRIP_CIRCUIT_BREAKER"
	EffectActivateKillSwitch RuleEffect = "ACTIVATE_KILL_SWITCH"
)

func (value RuleEffect) Validate() error {
	switch value {
	case EffectNone, EffectModify, EffectDefer, EffectReject,
		EffectTripCircuitBreaker, EffectActivateKillSwitch:
		return nil
	default:
		return ErrInvalidRiskPolicy
	}
}

type FailPosture string

const (
	FailClosed FailPosture = "FAIL_CLOSED"
)

type PolicyLifecycle string

const (
	PolicyActive   PolicyLifecycle = "ACTIVE"
	PolicyDisabled PolicyLifecycle = "DISABLED"
	PolicyRetired  PolicyLifecycle = "RETIRED"
)

type RiskRuleDescriptor struct {
	ID            RiskRuleID
	Version       RiskRuleVersion
	Name          string
	Description   string
	SchemaVersion string
}

func (value RiskRuleDescriptor) Validate() error {
	if value.ID.Validate() != nil || value.Version.Validate() != nil ||
		strings.TrimSpace(value.Name) == "" || len(value.Name) > 128 ||
		strings.TrimSpace(value.Description) == "" || len(value.Description) > 2048 ||
		strings.TrimSpace(value.SchemaVersion) == "" {
		return ErrInvalidRiskPolicy
	}
	return nil
}

type RiskRuleConfiguration struct {
	Descriptor        RiskRuleDescriptor
	Order             uint16
	Severity          RuleSeverity
	Effect            RuleEffect
	ConfigurationHash RiskConfigurationHash
	CanonicalJSON     []byte
}

func NewRiskRuleConfiguration(input RiskRuleConfiguration) (RiskRuleConfiguration, error) {
	if input.Descriptor.Validate() != nil || input.Order == 0 ||
		input.Severity.Validate() != nil || input.Effect.Validate() != nil ||
		input.ConfigurationHash.IsZero() || len(input.CanonicalJSON) == 0 ||
		len(input.CanonicalJSON) > MaximumRuleConfigurationBytes {
		return RiskRuleConfiguration{}, ErrInvalidRiskPolicy
	}
	canonical, err := canonicaljson.ObjectBounded(input.CanonicalJSON, canonicaljson.Limits{
		MaximumBytes: MaximumRuleConfigurationBytes, MaximumDepth: 8, MaximumCollection: 64,
	})
	if err != nil {
		return RiskRuleConfiguration{}, ErrInvalidRiskPolicy
	}
	expected, _ := NewRiskConfigurationHash(canonical)
	if input.ConfigurationHash != expected {
		return RiskRuleConfiguration{}, ErrInvalidRiskPolicy
	}
	input.CanonicalJSON = canonical
	return input, nil
}

func (value RiskRuleConfiguration) Canonical() []byte {
	return append([]byte(nil), value.CanonicalJSON...)
}

type RiskPolicySpec struct {
	ID                RiskPolicyID
	Version           RiskPolicyVersion
	SchemaVersion     string
	Lifecycle         PolicyLifecycle
	FailPosture       FailPosture
	EffectiveFrom     time.Time
	EffectiveUntil    time.Time
	Rules             []RiskRuleConfiguration
	ConfigurationHash RiskConfigurationHash
}

type RiskPolicy struct{ spec RiskPolicySpec }

func NewRiskPolicy(spec RiskPolicySpec) (RiskPolicy, error) {
	if spec.ID.IsZero() || spec.Version.Validate() != nil ||
		strings.TrimSpace(spec.SchemaVersion) == "" ||
		spec.FailPosture != FailClosed || spec.EffectiveFrom.IsZero() ||
		!spec.EffectiveUntil.After(spec.EffectiveFrom) || spec.ConfigurationHash.IsZero() ||
		len(spec.Rules) == 0 || len(spec.Rules) > MaximumRulesPerPolicy {
		return RiskPolicy{}, ErrInvalidRiskPolicy
	}
	switch spec.Lifecycle {
	case PolicyActive, PolicyDisabled, PolicyRetired:
	default:
		return RiskPolicy{}, ErrInvalidRiskPolicy
	}
	rules := append([]RiskRuleConfiguration(nil), spec.Rules...)
	seenID := make(map[RiskRuleID]struct{}, len(rules))
	seenOrder := make(map[uint16]struct{}, len(rules))
	for index := range rules {
		validated, err := NewRiskRuleConfiguration(rules[index])
		if err != nil {
			return RiskPolicy{}, err
		}
		rules[index] = validated
		if _, exists := seenID[validated.Descriptor.ID]; exists {
			return RiskPolicy{}, ErrInvalidRiskPolicy
		}
		if _, exists := seenOrder[validated.Order]; exists {
			return RiskPolicy{}, ErrInvalidRiskPolicy
		}
		seenID[validated.Descriptor.ID] = struct{}{}
		seenOrder[validated.Order] = struct{}{}
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Order < rules[j].Order })
	for index, rule := range rules {
		if rule.Order != uint16(index+1) {
			return RiskPolicy{}, ErrInvalidRiskPolicy
		}
	}
	spec.Rules = rules
	spec.EffectiveFrom = spec.EffectiveFrom.UTC()
	spec.EffectiveUntil = spec.EffectiveUntil.UTC()
	return RiskPolicy{spec: spec}, nil
}

func (value RiskPolicy) ID() RiskPolicyID           { return value.spec.ID }
func (value RiskPolicy) Version() RiskPolicyVersion { return value.spec.Version }
func (value RiskPolicy) ConfigurationHash() RiskConfigurationHash {
	return value.spec.ConfigurationHash
}
func (value RiskPolicy) Rules() []RiskRuleConfiguration {
	result := append([]RiskRuleConfiguration(nil), value.spec.Rules...)
	for index := range result {
		result[index].CanonicalJSON = append([]byte(nil), result[index].CanonicalJSON...)
	}
	return result
}
func (value RiskPolicy) Spec() RiskPolicySpec {
	result := value.spec
	result.Rules = value.Rules()
	return result
}
