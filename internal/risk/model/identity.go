package model

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

var ErrInvalidRiskIdentity = errors.New("invalid risk identity")

type riskDigest [sha256.Size]byte

func derive(namespace string, parts ...string) riskDigest {
	hash := sha256.New()
	writeFramed(hash, namespace)
	for _, part := range parts {
		writeFramed(hash, part)
	}
	var result riskDigest
	copy(result[:], hash.Sum(nil))
	return result
}

type writer interface{ Write([]byte) (int, error) }

func writeFramed(output writer, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = output.Write(size[:])
	_, _ = output.Write([]byte(value))
}

func validParts(parts []string) bool {
	// A production evaluation includes the base identity plus six framed parts
	// per configured rule and two per evidence item. The reviewed ten-rule
	// catalog therefore legitimately exceeds the old 64-part fixture limit.
	if len(parts) == 0 || len(parts) > 256 {
		return false
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" || len(part) > 4096 {
			return false
		}
	}
	return true
}

type RiskPolicyID riskDigest
type RiskEvaluationID riskDigest
type RiskViolationID riskDigest
type PortfolioRiskDecisionID riskDigest
type DecisionTriggerID riskDigest
type EvidenceChecksum riskDigest
type DecisionChecksum riskDigest
type RiskConfigurationHash riskDigest

func NewRiskPolicyID(parts ...string) (RiskPolicyID, error) {
	if !validParts(parts) {
		return RiskPolicyID{}, ErrInvalidRiskIdentity
	}
	return RiskPolicyID(derive("risk-policy-id/v1", parts...)), nil
}
func NewRiskEvaluationID(parts ...string) (RiskEvaluationID, error) {
	if !validParts(parts) {
		return RiskEvaluationID{}, ErrInvalidRiskIdentity
	}
	return RiskEvaluationID(derive("risk-evaluation-id/v1", parts...)), nil
}
func NewRiskViolationID(parts ...string) (RiskViolationID, error) {
	if !validParts(parts) {
		return RiskViolationID{}, ErrInvalidRiskIdentity
	}
	return RiskViolationID(derive("risk-violation-id/v1", parts...)), nil
}
func NewPortfolioRiskDecisionID(parts ...string) (PortfolioRiskDecisionID, error) {
	if !validParts(parts) {
		return PortfolioRiskDecisionID{}, ErrInvalidRiskIdentity
	}
	return PortfolioRiskDecisionID(derive("portfolio-risk-decision-id/v1", parts...)), nil
}
func NewDecisionTriggerID(parts ...string) (DecisionTriggerID, error) {
	if !validParts(parts) {
		return DecisionTriggerID{}, ErrInvalidRiskIdentity
	}
	return DecisionTriggerID(derive("portfolio-risk-trigger-id/v1", parts...)), nil
}
func NewEvidenceChecksum(canonical []byte) (EvidenceChecksum, error) {
	if len(canonical) == 0 {
		return EvidenceChecksum{}, ErrInvalidRiskIdentity
	}
	return EvidenceChecksum(derive("risk-evidence-checksum/v1", string(canonical))), nil
}
func NewDecisionChecksum(canonical []byte) (DecisionChecksum, error) {
	if len(canonical) == 0 {
		return DecisionChecksum{}, ErrInvalidRiskIdentity
	}
	return DecisionChecksum(derive("risk-decision-checksum/v1", string(canonical))), nil
}
func NewRiskConfigurationHash(canonical []byte) (RiskConfigurationHash, error) {
	if len(canonical) == 0 {
		return RiskConfigurationHash{}, ErrInvalidRiskIdentity
	}
	return RiskConfigurationHash(derive("risk-configuration-hash/v1", string(canonical))), nil
}

func digestString(value riskDigest) string           { return hex.EncodeToString(value[:]) }
func (value RiskPolicyID) String() string            { return digestString(riskDigest(value)) }
func (value RiskEvaluationID) String() string        { return digestString(riskDigest(value)) }
func (value RiskViolationID) String() string         { return digestString(riskDigest(value)) }
func (value PortfolioRiskDecisionID) String() string { return digestString(riskDigest(value)) }
func (value DecisionTriggerID) String() string       { return digestString(riskDigest(value)) }
func (value EvidenceChecksum) String() string        { return digestString(riskDigest(value)) }
func (value DecisionChecksum) String() string        { return digestString(riskDigest(value)) }
func (value RiskConfigurationHash) String() string   { return digestString(riskDigest(value)) }
func (value RiskPolicyID) IsZero() bool              { return value == RiskPolicyID{} }
func (value RiskEvaluationID) IsZero() bool          { return value == RiskEvaluationID{} }
func (value RiskViolationID) IsZero() bool           { return value == RiskViolationID{} }
func (value PortfolioRiskDecisionID) IsZero() bool   { return value == PortfolioRiskDecisionID{} }
func (value DecisionTriggerID) IsZero() bool         { return value == DecisionTriggerID{} }
func (value EvidenceChecksum) IsZero() bool          { return value == EvidenceChecksum{} }
func (value DecisionChecksum) IsZero() bool          { return value == DecisionChecksum{} }
func (value RiskConfigurationHash) IsZero() bool     { return value == RiskConfigurationHash{} }

type RiskPolicyVersion uint64
type RiskRuleVersion uint64

func (value RiskPolicyVersion) Validate() error {
	if value == 0 {
		return ErrInvalidRiskIdentity
	}
	return nil
}
func (value RiskRuleVersion) Validate() error {
	if value == 0 {
		return ErrInvalidRiskIdentity
	}
	return nil
}

type RiskRuleID string

var rulePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,63}$`)

func NewRiskRuleID(value string) (RiskRuleID, error) {
	value = strings.TrimSpace(value)
	if !rulePattern.MatchString(value) {
		return "", ErrInvalidRiskIdentity
	}
	return RiskRuleID(value), nil
}

func (value RiskRuleID) Validate() error {
	_, err := NewRiskRuleID(string(value))
	return err
}
