package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

var (
	ErrInvalidStrategyIdentity = errors.New("invalid strategy identity")
	definitionPattern          = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62})$`)
)

type DefinitionID string
type VersionID [sha256.Size]byte
type ConfigurationHash [sha256.Size]byte
type InstanceRevisionID [sha256.Size]byte
type FrameID [sha256.Size]byte
type TriggerID [sha256.Size]byte
type EvaluationID [sha256.Size]byte
type ProposalID [sha256.Size]byte
type StateHash [sha256.Size]byte

func NewDefinitionID(value string) (DefinitionID, error) {
	value = strings.TrimSpace(value)
	if !definitionPattern.MatchString(value) {
		return "", ErrInvalidStrategyIdentity
	}
	return DefinitionID(value), nil
}

func (id DefinitionID) String() string { return string(id) }

type VersionManifest struct {
	DefinitionID               DefinitionID
	ImplementationVersion      string
	InputContractVersion       string
	ConfigurationSchemaVersion string
	StateSchemaVersion         string
	ResultSchemaVersion        string
	ProposalSchemaVersion      string
}

func NewVersionID(manifest VersionManifest) (VersionID, error) {
	if _, err := NewDefinitionID(manifest.DefinitionID.String()); err != nil {
		return VersionID{}, ErrInvalidStrategyIdentity
	}
	values := []string{
		manifest.DefinitionID.String(),
		manifest.ImplementationVersion,
		manifest.InputContractVersion,
		manifest.ConfigurationSchemaVersion,
		manifest.StateSchemaVersion,
		manifest.ResultSchemaVersion,
		manifest.ProposalSchemaVersion,
	}
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
		if values[index] == "" {
			return VersionID{}, ErrInvalidStrategyIdentity
		}
	}
	return VersionID(sha256.Sum256([]byte(stableKey("strategy-version/v1", values...)))), nil
}

func NewInstanceRevisionID(
	instanceID domain.StrategyID,
	versionID VersionID,
	configurationHash ConfigurationHash,
	generation uint64,
) (InstanceRevisionID, error) {
	if strings.TrimSpace(string(instanceID)) == "" || versionID.IsZero() ||
		configurationHash.IsZero() || generation == 0 {
		return InstanceRevisionID{}, ErrInvalidStrategyIdentity
	}
	payload := stableKey("strategy-instance-revision/v1", string(instanceID),
		versionID.String(), configurationHash.String(), strconv.FormatUint(generation, 10))
	return InstanceRevisionID(sha256.Sum256([]byte(payload))), nil
}

func NewFrameID(key string) (FrameID, error) {
	digest, err := newDigest(key)
	return FrameID(digest), err
}

func NewTriggerID(key string) (TriggerID, error) {
	digest, err := newDigest(key)
	return TriggerID(digest), err
}

func NewEvaluationID(key string) (EvaluationID, error) {
	digest, err := newDigest(key)
	return EvaluationID(digest), err
}

func NewProposalID(key string) (ProposalID, error) {
	digest, err := newDigest(key)
	return ProposalID(digest), err
}

func newDigest(key string) ([sha256.Size]byte, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return [sha256.Size]byte{}, ErrInvalidStrategyIdentity
	}
	return sha256.Sum256([]byte(key)), nil
}

// stableKey length-prefixes every component so arbitrary text cannot create
// delimiter collisions in content-derived identities.
func stableKey(schema string, values ...string) string {
	var builder strings.Builder
	builder.WriteString(schema)
	for _, value := range values {
		fmt.Fprintf(&builder, "|%d:", len(value))
		builder.WriteString(value)
	}
	return builder.String()
}

func digestString(value [sha256.Size]byte) string { return hex.EncodeToString(value[:]) }

func (id VersionID) String() string          { return digestString(id) }
func (id ConfigurationHash) String() string  { return digestString(id) }
func (id InstanceRevisionID) String() string { return digestString(id) }
func (id FrameID) String() string            { return digestString(id) }
func (id TriggerID) String() string          { return digestString(id) }
func (id EvaluationID) String() string       { return digestString(id) }
func (id ProposalID) String() string         { return digestString(id) }
func (id StateHash) String() string          { return digestString(id) }

func (id VersionID) IsZero() bool          { return id == VersionID{} }
func (id ConfigurationHash) IsZero() bool  { return id == ConfigurationHash{} }
func (id InstanceRevisionID) IsZero() bool { return id == InstanceRevisionID{} }
func (id FrameID) IsZero() bool            { return id == FrameID{} }
func (id TriggerID) IsZero() bool          { return id == TriggerID{} }
func (id EvaluationID) IsZero() bool       { return id == EvaluationID{} }
func (id ProposalID) IsZero() bool         { return id == ProposalID{} }
func (id StateHash) IsZero() bool          { return id == StateHash{} }
