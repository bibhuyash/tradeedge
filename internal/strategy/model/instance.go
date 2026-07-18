package model

import (
	"errors"
	"strings"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

var ErrInvalidStrategyInstance = errors.New("invalid strategy instance")

// StrategyInstance binds one immutable definition version and configuration to
// a lifecycle-controlled deployment identity. Updating either version or
// configuration creates a new revision and never mutates the old identity.
type StrategyInstance struct {
	id            domain.StrategyID
	definitionID  DefinitionID
	versionID     VersionID
	revisionID    InstanceRevisionID
	generation    uint64
	configuration StrategyConfiguration
	lifecycle     LifecycleState
}

func NewStrategyInstance(
	id domain.StrategyID,
	descriptor Descriptor,
	configuration StrategyConfiguration,
	generation uint64,
	lifecycle LifecycleState,
) (StrategyInstance, error) {
	if strings.TrimSpace(string(id)) == "" || descriptor.Validate() != nil ||
		configuration.IsZero() ||
		configuration.SchemaVersion() != descriptor.Manifest.ConfigurationSchemaVersion ||
		generation == 0 || lifecycle.Validate() != nil {
		return StrategyInstance{}, ErrInvalidStrategyInstance
	}
	revisionID, err := NewInstanceRevisionID(
		id, descriptor.VersionID, configuration.Hash(), generation,
	)
	if err != nil {
		return StrategyInstance{}, ErrInvalidStrategyInstance
	}
	return StrategyInstance{
		id: id, definitionID: descriptor.Manifest.DefinitionID,
		versionID: descriptor.VersionID, revisionID: revisionID,
		generation: generation, configuration: configuration, lifecycle: lifecycle,
	}, nil
}

func (instance StrategyInstance) ID() domain.StrategyID      { return instance.id }
func (instance StrategyInstance) DefinitionID() DefinitionID { return instance.definitionID }
func (instance StrategyInstance) VersionID() VersionID       { return instance.versionID }
func (instance StrategyInstance) RevisionID() InstanceRevisionID {
	return instance.revisionID
}
func (instance StrategyInstance) Generation() uint64 { return instance.generation }
func (instance StrategyInstance) Configuration() StrategyConfiguration {
	return instance.configuration
}
func (instance StrategyInstance) Lifecycle() LifecycleState { return instance.lifecycle }
func (instance StrategyInstance) Evaluates() bool           { return instance.lifecycle.Evaluates() }
