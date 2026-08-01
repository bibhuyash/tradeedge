package model

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
)

var ErrInvalidIdentity = errors.New("invalid portfolio identity")

type digest [sha256.Size]byte

func derive(namespace string, parts ...string) digest {
	hash := sha256.New()
	writePart(hash, namespace)
	for _, part := range parts {
		writePart(hash, part)
	}
	var result digest
	copy(result[:], hash.Sum(nil))
	return result
}

type writer interface{ Write([]byte) (int, error) }

func writePart(output writer, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = output.Write(size[:])
	_, _ = output.Write([]byte(value))
}

func validParts(parts []string) bool {
	if len(parts) == 0 || len(parts) > 64 {
		return false
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" || len(part) > 4096 {
			return false
		}
	}
	return true
}

func digestString(value digest) string { return hex.EncodeToString(value[:]) }

type PortfolioID digest
type PortfolioSnapshotID digest
type PortfolioConfigurationID digest
type AllocationPolicyID digest
type StrategyAllocationID digest
type AllocationCandidateID digest
type KillSwitchID digest
type CircuitBreakerID digest
type ConfigurationHash digest
type StateChecksum digest

func NewPortfolioID(parts ...string) (PortfolioID, error) {
	if !validParts(parts) {
		return PortfolioID{}, ErrInvalidIdentity
	}
	return PortfolioID(derive("portfolio-id/v1", parts...)), nil
}
func NewPortfolioConfigurationID(parts ...string) (PortfolioConfigurationID, error) {
	if !validParts(parts) {
		return PortfolioConfigurationID{}, ErrInvalidIdentity
	}
	return PortfolioConfigurationID(derive("portfolio-configuration-id/v1", parts...)), nil
}
func NewAllocationPolicyID(parts ...string) (AllocationPolicyID, error) {
	if !validParts(parts) {
		return AllocationPolicyID{}, ErrInvalidIdentity
	}
	return AllocationPolicyID(derive("allocation-policy-id/v1", parts...)), nil
}
func NewStrategyAllocationID(parts ...string) (StrategyAllocationID, error) {
	if !validParts(parts) {
		return StrategyAllocationID{}, ErrInvalidIdentity
	}
	return StrategyAllocationID(derive("strategy-allocation-id/v1", parts...)), nil
}
func NewAllocationCandidateID(parts ...string) (AllocationCandidateID, error) {
	if !validParts(parts) {
		return AllocationCandidateID{}, ErrInvalidIdentity
	}
	return AllocationCandidateID(derive("allocation-candidate-id/v1", parts...)), nil
}
func NewKillSwitchID(parts ...string) (KillSwitchID, error) {
	if !validParts(parts) {
		return KillSwitchID{}, ErrInvalidIdentity
	}
	return KillSwitchID(derive("kill-switch-id/v1", parts...)), nil
}
func NewCircuitBreakerID(parts ...string) (CircuitBreakerID, error) {
	if !validParts(parts) {
		return CircuitBreakerID{}, ErrInvalidIdentity
	}
	return CircuitBreakerID(derive("circuit-breaker-id/v1", parts...)), nil
}
func NewConfigurationHash(canonical []byte) (ConfigurationHash, error) {
	if len(canonical) == 0 {
		return ConfigurationHash{}, ErrInvalidIdentity
	}
	return ConfigurationHash(derive("portfolio-configuration-hash/v1", string(canonical))), nil
}
func NewStateChecksum(canonical []byte) (StateChecksum, error) {
	if len(canonical) == 0 {
		return StateChecksum{}, ErrInvalidIdentity
	}
	return StateChecksum(derive("portfolio-state-checksum/v1", string(canonical))), nil
}

func (value PortfolioID) String() string              { return digestString(digest(value)) }
func (value PortfolioSnapshotID) String() string      { return digestString(digest(value)) }
func (value PortfolioConfigurationID) String() string { return digestString(digest(value)) }
func (value AllocationPolicyID) String() string       { return digestString(digest(value)) }
func (value StrategyAllocationID) String() string     { return digestString(digest(value)) }
func (value AllocationCandidateID) String() string    { return digestString(digest(value)) }
func (value KillSwitchID) String() string             { return digestString(digest(value)) }
func (value CircuitBreakerID) String() string         { return digestString(digest(value)) }
func (value ConfigurationHash) String() string        { return digestString(digest(value)) }
func (value StateChecksum) String() string            { return digestString(digest(value)) }

func (value PortfolioID) IsZero() bool              { return value == PortfolioID{} }
func (value PortfolioSnapshotID) IsZero() bool      { return value == PortfolioSnapshotID{} }
func (value PortfolioConfigurationID) IsZero() bool { return value == PortfolioConfigurationID{} }
func (value AllocationPolicyID) IsZero() bool       { return value == AllocationPolicyID{} }
func (value StrategyAllocationID) IsZero() bool     { return value == StrategyAllocationID{} }
func (value AllocationCandidateID) IsZero() bool    { return value == AllocationCandidateID{} }
func (value KillSwitchID) IsZero() bool             { return value == KillSwitchID{} }
func (value CircuitBreakerID) IsZero() bool         { return value == CircuitBreakerID{} }
func (value ConfigurationHash) IsZero() bool        { return value == ConfigurationHash{} }
func (value StateChecksum) IsZero() bool            { return value == StateChecksum{} }

type PortfolioRevision uint64
type PortfolioConfigurationVersion uint64
type AllocationPolicyVersion uint64

func (value PortfolioRevision) Validate() error {
	if value == 0 {
		return ErrInvalidIdentity
	}
	return nil
}
func (value PortfolioConfigurationVersion) Validate() error {
	if value == 0 {
		return ErrInvalidIdentity
	}
	return nil
}
func (value AllocationPolicyVersion) Validate() error {
	if value == 0 {
		return ErrInvalidIdentity
	}
	return nil
}

func deriveSnapshotID(parts ...string) PortfolioSnapshotID {
	return PortfolioSnapshotID(derive("portfolio-snapshot-id/v1", parts...))
}
