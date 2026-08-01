package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/canonicaljson"
	"github.com/bibhuyash/tradeedge/internal/domain"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
)

const (
	MaximumConfigurationBytes = 256 << 10
	MaximumConfigurationDepth = 16
	MaximumCollectionEntries  = 100
)

var (
	ErrInvalidConfiguration = errors.New("invalid risk configuration")
	ErrUnknownRule          = errors.New("unknown risk rule")
)

type KillSwitchConfiguration struct {
	Enabled       bool
	AllowedScopes []string
}

type CircuitBreakerConfiguration struct {
	Enabled        bool
	Threshold      uint64
	ResetThreshold uint64
}

type RiskConfiguration struct {
	policy         riskmodel.RiskPolicy
	hash           riskmodel.RiskConfigurationHash
	killSwitch     KillSwitchConfiguration
	circuitBreaker CircuitBreakerConfiguration
	canonical      []byte
}

type ruleLimitDocument struct {
	LimitMinor     *int64  `json:"limit_minor,omitempty"`
	LossLimitMinor *int64  `json:"loss_limit_minor,omitempty"`
	LimitBPS       *int32  `json:"limit_bps,omitempty"`
	Threshold      *uint64 `json:"threshold,omitempty"`
	ResetThreshold *uint64 `json:"reset_threshold,omitempty"`
	Currency       string  `json:"currency,omitempty"`
	ExposureGroup  string  `json:"exposure_group,omitempty"`
}

type ruleDocument struct {
	ID       string            `json:"id"`
	Version  uint64            `json:"version"`
	Order    uint16            `json:"order"`
	Severity string            `json:"severity"`
	Effect   string            `json:"effect"`
	Config   ruleLimitDocument `json:"config"`
}

type document struct {
	SchemaVersion  string         `json:"schema_version"`
	Version        uint64         `json:"version"`
	Lifecycle      string         `json:"lifecycle"`
	EffectiveFrom  string         `json:"effective_from"`
	EffectiveUntil string         `json:"effective_until"`
	Rules          []ruleDocument `json:"rules"`
	KillSwitch     struct {
		Enabled       bool     `json:"enabled"`
		AllowedScopes []string `json:"allowed_scopes"`
	} `json:"kill_switch"`
	CircuitBreaker struct {
		Enabled        bool   `json:"enabled"`
		Threshold      uint64 `json:"threshold"`
		ResetThreshold uint64 `json:"reset_threshold"`
	} `json:"circuit_breaker"`
}

func Decode(raw []byte, registry map[riskmodel.RiskRuleID]riskmodel.RiskRuleDescriptor,
	knownExposureGroups []string) (RiskConfiguration, error) {
	canonical, err := canonicaljson.ObjectBounded(raw, canonicaljson.Limits{
		MaximumBytes: MaximumConfigurationBytes, MaximumDepth: MaximumConfigurationDepth,
		MaximumCollection: MaximumCollectionEntries,
	})
	if err != nil {
		return RiskConfiguration{}, fmt.Errorf("%w: %v", ErrInvalidConfiguration, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var input document
	if err := decoder.Decode(&input); err != nil {
		return RiskConfiguration{}, fmt.Errorf("%w: %v", ErrInvalidConfiguration, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return RiskConfiguration{}, ErrInvalidConfiguration
	}
	return newConfiguration(input, canonical, registry, knownExposureGroups)
}

func newConfiguration(input document, canonical []byte,
	registry map[riskmodel.RiskRuleID]riskmodel.RiskRuleDescriptor,
	knownExposureGroups []string) (RiskConfiguration, error) {
	if strings.TrimSpace(input.SchemaVersion) == "" || input.Version == 0 ||
		len(input.Rules) == 0 || len(input.Rules) > riskmodel.MaximumRulesPerPolicy {
		return RiskConfiguration{}, ErrInvalidConfiguration
	}
	effectiveFrom, err := time.Parse(time.RFC3339Nano, input.EffectiveFrom)
	if err != nil {
		return RiskConfiguration{}, ErrInvalidConfiguration
	}
	effectiveUntil, err := time.Parse(time.RFC3339Nano, input.EffectiveUntil)
	if err != nil || !effectiveUntil.After(effectiveFrom) {
		return RiskConfiguration{}, ErrInvalidConfiguration
	}
	lifecycle := riskmodel.PolicyLifecycle(input.Lifecycle)
	groups := make(map[string]struct{}, len(knownExposureGroups))
	for _, group := range knownExposureGroups {
		groups[group] = struct{}{}
	}
	rules := make([]riskmodel.RiskRuleConfiguration, len(input.Rules))
	seen := make(map[riskmodel.RiskRuleID]struct{}, len(input.Rules))
	for index, rule := range input.Rules {
		id, idErr := riskmodel.NewRiskRuleID(rule.ID)
		if idErr != nil {
			return RiskConfiguration{}, ErrInvalidConfiguration
		}
		descriptor, found := registry[id]
		if !found {
			return RiskConfiguration{}, fmt.Errorf("%w: %s", ErrUnknownRule, id)
		}
		if descriptor.Version != riskmodel.RiskRuleVersion(rule.Version) {
			return RiskConfiguration{}, ErrInvalidConfiguration
		}
		if _, exists := seen[id]; exists {
			return RiskConfiguration{}, ErrInvalidConfiguration
		}
		seen[id] = struct{}{}
		if err := validateRuleLimit(rule.Config, groups); err != nil {
			return RiskConfiguration{}, err
		}
		configRaw, _ := json.Marshal(rule.Config)
		configCanonical, canonicalErr := canonicaljson.ObjectBounded(configRaw, canonicaljson.Limits{
			MaximumBytes: riskmodel.MaximumRuleConfigurationBytes,
			MaximumDepth: 4, MaximumCollection: 16,
		})
		if canonicalErr != nil {
			return RiskConfiguration{}, ErrInvalidConfiguration
		}
		hash, _ := riskmodel.NewRiskConfigurationHash(configCanonical)
		validated, validateErr := riskmodel.NewRiskRuleConfiguration(riskmodel.RiskRuleConfiguration{
			Descriptor: descriptor, Order: rule.Order,
			Severity:          riskmodel.RuleSeverity(rule.Severity),
			Effect:            riskmodel.RuleEffect(rule.Effect),
			ConfigurationHash: hash, CanonicalJSON: configCanonical,
		})
		if validateErr != nil {
			return RiskConfiguration{}, ErrInvalidConfiguration
		}
		rules[index] = validated
	}
	if input.CircuitBreaker.Enabled &&
		(input.CircuitBreaker.Threshold == 0 ||
			input.CircuitBreaker.ResetThreshold >= input.CircuitBreaker.Threshold) {
		return RiskConfiguration{}, ErrInvalidConfiguration
	}
	scopes := append([]string(nil), input.KillSwitch.AllowedScopes...)
	if input.KillSwitch.Enabled && len(scopes) == 0 {
		return RiskConfiguration{}, ErrInvalidConfiguration
	}
	sort.Strings(scopes)
	for index, scope := range scopes {
		if index > 0 && scopes[index-1] == scope {
			return RiskConfiguration{}, ErrInvalidConfiguration
		}
	}
	sort.Slice(input.Rules, func(i, j int) bool { return input.Rules[i].Order < input.Rules[j].Order })
	input.SchemaVersion = strings.TrimSpace(input.SchemaVersion)
	input.EffectiveFrom = effectiveFrom.UTC().Format(time.RFC3339Nano)
	input.EffectiveUntil = effectiveUntil.UTC().Format(time.RFC3339Nano)
	input.KillSwitch.AllowedScopes = scopes
	normalizedRaw, marshalErr := json.Marshal(input)
	if marshalErr != nil {
		return RiskConfiguration{}, ErrInvalidConfiguration
	}
	canonical, err = canonicaljson.ObjectBounded(normalizedRaw, canonicaljson.Limits{
		MaximumBytes: MaximumConfigurationBytes, MaximumDepth: MaximumConfigurationDepth,
		MaximumCollection: MaximumCollectionEntries,
	})
	if err != nil {
		return RiskConfiguration{}, ErrInvalidConfiguration
	}
	hash, _ := riskmodel.NewRiskConfigurationHash(canonical)
	policyID, _ := riskmodel.NewRiskPolicyID(input.SchemaVersion, fmt.Sprint(input.Version), hash.String())
	policy, err := riskmodel.NewRiskPolicy(riskmodel.RiskPolicySpec{
		ID: policyID, Version: riskmodel.RiskPolicyVersion(input.Version),
		SchemaVersion: input.SchemaVersion, Lifecycle: lifecycle,
		FailPosture: riskmodel.FailClosed, EffectiveFrom: effectiveFrom.UTC(),
		EffectiveUntil: effectiveUntil.UTC(), Rules: rules, ConfigurationHash: hash,
	})
	if err != nil {
		return RiskConfiguration{}, ErrInvalidConfiguration
	}
	return RiskConfiguration{
		policy: policy, hash: hash,
		killSwitch: KillSwitchConfiguration{Enabled: input.KillSwitch.Enabled, AllowedScopes: scopes},
		circuitBreaker: CircuitBreakerConfiguration{
			Enabled: input.CircuitBreaker.Enabled, Threshold: input.CircuitBreaker.Threshold,
			ResetThreshold: input.CircuitBreaker.ResetThreshold,
		},
		canonical: append([]byte(nil), canonical...),
	}, nil
}

func validateRuleLimit(value ruleLimitDocument, groups map[string]struct{}) error {
	present := 0
	if value.LimitMinor != nil {
		present++
		if *value.LimitMinor < 0 {
			return ErrInvalidConfiguration
		}
	}
	if value.LossLimitMinor != nil {
		present++
		if *value.LossLimitMinor <= 0 {
			return ErrInvalidConfiguration
		}
	}
	if value.LimitBPS != nil {
		present++
		if *value.LimitBPS < 0 || *value.LimitBPS > 10000 {
			return ErrInvalidConfiguration
		}
	}
	if value.Threshold != nil {
		present++
		if *value.Threshold == 0 {
			return ErrInvalidConfiguration
		}
	}
	if value.ResetThreshold != nil {
		if value.Threshold == nil || *value.ResetThreshold >= *value.Threshold {
			return ErrInvalidConfiguration
		}
	}
	if value.Currency != "" {
		if _, err := domain.NewCurrency(value.Currency); err != nil {
			return ErrInvalidConfiguration
		}
	}
	if value.ExposureGroup != "" {
		if _, found := groups[value.ExposureGroup]; !found {
			return ErrInvalidConfiguration
		}
	}
	if present == 0 {
		return ErrInvalidConfiguration
	}
	return nil
}

func (value RiskConfiguration) Policy() riskmodel.RiskPolicy          { return value.policy }
func (value RiskConfiguration) Hash() riskmodel.RiskConfigurationHash { return value.hash }
func (value RiskConfiguration) KillSwitch() KillSwitchConfiguration {
	result := value.killSwitch
	result.AllowedScopes = append([]string(nil), result.AllowedScopes...)
	return result
}
func (value RiskConfiguration) CircuitBreaker() CircuitBreakerConfiguration {
	return value.circuitBreaker
}
func (value RiskConfiguration) CanonicalJSON() []byte {
	return append([]byte(nil), value.canonical...)
}
