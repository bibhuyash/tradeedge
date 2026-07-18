package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/bibhuyash/tradeedge/internal/domain"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

const checkpointSchemaVersion = 1

type RuntimeCheckpointSpec struct {
	InstanceID         domain.StrategyID
	DefinitionID       strategymodel.DefinitionID
	VersionID          strategymodel.VersionID
	InstanceRevisionID strategymodel.InstanceRevisionID
	ConfigurationHash  strategymodel.ConfigurationHash
	Revision           uint64
	ParentChecksum     CheckpointChecksum
	EvaluationID       strategymodel.EvaluationID
	State              strategymodel.StrategyRuntimeState
}

type RuntimeCheckpoint struct {
	instanceID         domain.StrategyID
	definitionID       strategymodel.DefinitionID
	versionID          strategymodel.VersionID
	instanceRevisionID strategymodel.InstanceRevisionID
	configurationHash  strategymodel.ConfigurationHash
	revision           uint64
	parentChecksum     CheckpointChecksum
	evaluationID       strategymodel.EvaluationID
	state              strategymodel.StrategyRuntimeState
	checksum           CheckpointChecksum
}

func NewRuntimeCheckpoint(spec RuntimeCheckpointSpec) (RuntimeCheckpoint, error) {
	if strings.TrimSpace(string(spec.InstanceID)) == "" ||
		spec.DefinitionID == "" || spec.VersionID.IsZero() ||
		spec.InstanceRevisionID.IsZero() || spec.ConfigurationHash.IsZero() ||
		spec.State.IsZero() {
		return RuntimeCheckpoint{}, ErrCorruptCheckpoint
	}
	if _, err := strategymodel.NewDefinitionID(spec.DefinitionID.String()); err != nil {
		return RuntimeCheckpoint{}, ErrCorruptCheckpoint
	}
	if spec.Revision == 0 {
		if !spec.ParentChecksum.IsZero() || !spec.EvaluationID.IsZero() {
			return RuntimeCheckpoint{}, ErrCorruptCheckpoint
		}
	} else if spec.ParentChecksum.IsZero() || spec.EvaluationID.IsZero() {
		return RuntimeCheckpoint{}, ErrCorruptCheckpoint
	}
	checkpoint := RuntimeCheckpoint{
		instanceID: spec.InstanceID, definitionID: spec.DefinitionID,
		versionID: spec.VersionID, instanceRevisionID: spec.InstanceRevisionID,
		configurationHash: spec.ConfigurationHash, revision: spec.Revision,
		parentChecksum: spec.ParentChecksum, evaluationID: spec.EvaluationID,
		state: spec.State,
	}
	payload, err := checkpointPayload(checkpoint)
	if err != nil {
		return RuntimeCheckpoint{}, ErrCorruptCheckpoint
	}
	checkpoint.checksum = CheckpointChecksum(sha256.Sum256(payload))
	return checkpoint, nil
}

func (checkpoint RuntimeCheckpoint) InstanceID() domain.StrategyID {
	return checkpoint.instanceID
}
func (checkpoint RuntimeCheckpoint) DefinitionID() strategymodel.DefinitionID {
	return checkpoint.definitionID
}
func (checkpoint RuntimeCheckpoint) VersionID() strategymodel.VersionID {
	return checkpoint.versionID
}
func (checkpoint RuntimeCheckpoint) InstanceRevisionID() strategymodel.InstanceRevisionID {
	return checkpoint.instanceRevisionID
}
func (checkpoint RuntimeCheckpoint) ConfigurationHash() strategymodel.ConfigurationHash {
	return checkpoint.configurationHash
}
func (checkpoint RuntimeCheckpoint) Revision() uint64 { return checkpoint.revision }
func (checkpoint RuntimeCheckpoint) ParentChecksum() CheckpointChecksum {
	return checkpoint.parentChecksum
}
func (checkpoint RuntimeCheckpoint) EvaluationID() strategymodel.EvaluationID {
	return checkpoint.evaluationID
}
func (checkpoint RuntimeCheckpoint) State() strategymodel.StrategyRuntimeState {
	return checkpoint.state
}
func (checkpoint RuntimeCheckpoint) Checksum() CheckpointChecksum { return checkpoint.checksum }
func (checkpoint RuntimeCheckpoint) IsZero() bool                 { return checkpoint.checksum.IsZero() }

type checkpointWire struct {
	SchemaVersion      int             `json:"schema_version"`
	InstanceID         string          `json:"instance_id"`
	DefinitionID       string          `json:"definition_id"`
	VersionID          string          `json:"version_id"`
	InstanceRevisionID string          `json:"instance_revision_id"`
	ConfigurationHash  string          `json:"configuration_hash"`
	Revision           uint64          `json:"revision"`
	ParentChecksum     string          `json:"parent_checksum,omitempty"`
	EvaluationID       string          `json:"evaluation_id,omitempty"`
	StateSchemaVersion string          `json:"state_schema_version"`
	State              json.RawMessage `json:"state"`
	Checksum           string          `json:"checksum,omitempty"`
}

func checkpointPayload(checkpoint RuntimeCheckpoint) ([]byte, error) {
	wire := checkpointToWire(checkpoint)
	wire.Checksum = ""
	return json.Marshal(wire)
}

func EncodeCheckpoint(checkpoint RuntimeCheckpoint) ([]byte, error) {
	if err := VerifyCheckpoint(checkpoint); err != nil {
		return nil, err
	}
	wire := checkpointToWire(checkpoint)
	wire.Checksum = checkpoint.checksum.String()
	return json.Marshal(wire)
}

func DecodeCheckpoint(data []byte) (RuntimeCheckpoint, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire checkpointWire
	if err := decoder.Decode(&wire); err != nil {
		return RuntimeCheckpoint{}, ErrCorruptCheckpoint
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return RuntimeCheckpoint{}, ErrCorruptCheckpoint
	}
	if wire.SchemaVersion != checkpointSchemaVersion || len(wire.Checksum) != sha256.Size*2 {
		return RuntimeCheckpoint{}, ErrCorruptCheckpoint
	}
	versionID, err := parseVersionID(wire.VersionID)
	if err != nil {
		return RuntimeCheckpoint{}, err
	}
	instanceRevisionID, err := parseInstanceRevisionID(wire.InstanceRevisionID)
	if err != nil {
		return RuntimeCheckpoint{}, err
	}
	configurationHash, err := parseConfigurationHash(wire.ConfigurationHash)
	if err != nil {
		return RuntimeCheckpoint{}, err
	}
	parentChecksum, err := parseCheckpointChecksum(wire.ParentChecksum, wire.Revision == 0)
	if err != nil {
		return RuntimeCheckpoint{}, err
	}
	evaluationID, err := parseEvaluationID(wire.EvaluationID, wire.Revision == 0)
	if err != nil {
		return RuntimeCheckpoint{}, err
	}
	state, err := strategymodel.NewStrategyRuntimeState(wire.StateSchemaVersion, wire.State)
	if err != nil {
		return RuntimeCheckpoint{}, ErrCorruptCheckpoint
	}
	definitionID, err := strategymodel.NewDefinitionID(wire.DefinitionID)
	if err != nil {
		return RuntimeCheckpoint{}, ErrCorruptCheckpoint
	}
	instanceID, err := domain.NewStrategyID(wire.InstanceID)
	if err != nil {
		return RuntimeCheckpoint{}, ErrCorruptCheckpoint
	}
	checkpoint, err := NewRuntimeCheckpoint(RuntimeCheckpointSpec{
		InstanceID: instanceID, DefinitionID: definitionID, VersionID: versionID,
		InstanceRevisionID: instanceRevisionID, ConfigurationHash: configurationHash,
		Revision: wire.Revision, ParentChecksum: parentChecksum,
		EvaluationID: evaluationID, State: state,
	})
	if err != nil || checkpoint.checksum.String() != strings.ToLower(wire.Checksum) {
		return RuntimeCheckpoint{}, ErrCorruptCheckpoint
	}
	return checkpoint, nil
}

func VerifyCheckpoint(checkpoint RuntimeCheckpoint) error {
	if checkpoint.IsZero() {
		return ErrCorruptCheckpoint
	}
	payload, err := checkpointPayload(checkpoint)
	if err != nil {
		return ErrCorruptCheckpoint
	}
	if CheckpointChecksum(sha256.Sum256(payload)) != checkpoint.checksum {
		return ErrCorruptCheckpoint
	}
	return nil
}

func VerifyRestoration(
	checkpoint RuntimeCheckpoint,
	expectation RestoreExpectation,
) error {
	if err := VerifyCheckpoint(checkpoint); err != nil ||
		checkpoint.InstanceID() != expectation.InstanceID ||
		checkpoint.DefinitionID() != expectation.DefinitionID ||
		checkpoint.VersionID() != expectation.VersionID ||
		checkpoint.ConfigurationHash() != expectation.ConfigurationHash ||
		checkpoint.InstanceRevisionID() != expectation.InstanceRevisionID ||
		checkpoint.State().SchemaVersion() != expectation.StateSchemaVersion ||
		checkpoint.Revision() != expectation.Revision {
		return ErrCorruptCheckpoint
	}
	return nil
}

func checkpointToWire(checkpoint RuntimeCheckpoint) checkpointWire {
	return checkpointWire{
		SchemaVersion: checkpointSchemaVersion, InstanceID: string(checkpoint.instanceID),
		DefinitionID: checkpoint.definitionID.String(), VersionID: checkpoint.versionID.String(),
		InstanceRevisionID: checkpoint.instanceRevisionID.String(),
		ConfigurationHash:  checkpoint.configurationHash.String(),
		Revision:           checkpoint.revision, ParentChecksum: zeroableChecksum(checkpoint.parentChecksum),
		EvaluationID:       zeroableEvaluationID(checkpoint.evaluationID),
		StateSchemaVersion: checkpoint.state.SchemaVersion(),
		State:              json.RawMessage(checkpoint.state.CanonicalJSON()),
	}
}

func parseDigest(value string, allowEmpty bool) ([sha256.Size]byte, error) {
	if allowEmpty && value == "" {
		return [sha256.Size]byte{}, nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return [sha256.Size]byte{}, ErrCorruptCheckpoint
	}
	var result [sha256.Size]byte
	copy(result[:], decoded)
	return result, nil
}

func parseVersionID(value string) (strategymodel.VersionID, error) {
	digest, err := parseDigest(value, false)
	return strategymodel.VersionID(digest), err
}
func parseInstanceRevisionID(value string) (strategymodel.InstanceRevisionID, error) {
	digest, err := parseDigest(value, false)
	return strategymodel.InstanceRevisionID(digest), err
}
func parseConfigurationHash(value string) (strategymodel.ConfigurationHash, error) {
	digest, err := parseDigest(value, false)
	return strategymodel.ConfigurationHash(digest), err
}
func parseCheckpointChecksum(value string, allowEmpty bool) (CheckpointChecksum, error) {
	digest, err := parseDigest(value, allowEmpty)
	return CheckpointChecksum(digest), err
}
func parseEvaluationID(value string, allowEmpty bool) (strategymodel.EvaluationID, error) {
	digest, err := parseDigest(value, allowEmpty)
	return strategymodel.EvaluationID(digest), err
}
func zeroableChecksum(value CheckpointChecksum) string {
	if value.IsZero() {
		return ""
	}
	return value.String()
}
func zeroableEvaluationID(value strategymodel.EvaluationID) string {
	if value.IsZero() {
		return ""
	}
	return value.String()
}
