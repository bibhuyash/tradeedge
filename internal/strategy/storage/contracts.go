package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/bibhuyash/tradeedge/internal/domain"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

var (
	ErrNotFound           = errors.New("strategy repository record not found")
	ErrRevisionConflict   = errors.New("strategy state revision conflict")
	ErrIdentityCollision  = errors.New("strategy identity integrity violation")
	ErrInvalidPublication = errors.New("invalid strategy evaluation publication")
	ErrCorruptCheckpoint  = errors.New("corrupt strategy checkpoint")
	ErrStorageFailure     = errors.New("strategy repository internal failure")
)

type RevisionConflictError struct {
	InstanceID domain.StrategyID
	Expected   uint64
	Actual     uint64
}

func (err *RevisionConflictError) Error() string {
	return fmt.Sprintf("%v for %s: expected %d, actual %d",
		ErrRevisionConflict, err.InstanceID, err.Expected, err.Actual)
}
func (err *RevisionConflictError) Unwrap() error { return ErrRevisionConflict }

type InstanceRevisionConflictError struct {
	InstanceID domain.StrategyID
	Expected   strategymodel.InstanceRevisionID
	Actual     strategymodel.InstanceRevisionID
}

func (err *InstanceRevisionConflictError) Error() string {
	return fmt.Sprintf("%v for %s: expected %s, actual %s",
		ErrRevisionConflict, err.InstanceID, err.Expected, err.Actual)
}
func (err *InstanceRevisionConflictError) Unwrap() error { return ErrRevisionConflict }

type IdentityCollisionError struct {
	Kind     string
	Identity string
}

func (err *IdentityCollisionError) Error() string {
	return fmt.Sprintf("%v: %s %s", ErrIdentityCollision, err.Kind, err.Identity)
}
func (err *IdentityCollisionError) Unwrap() error { return ErrIdentityCollision }

type CheckpointChecksum [sha256.Size]byte
type PublicationChecksum [sha256.Size]byte

func (checksum CheckpointChecksum) String() string {
	return hex.EncodeToString(checksum[:])
}
func (checksum CheckpointChecksum) IsZero() bool {
	return checksum == CheckpointChecksum{}
}
func (checksum PublicationChecksum) String() string {
	return hex.EncodeToString(checksum[:])
}
func (checksum PublicationChecksum) IsZero() bool {
	return checksum == PublicationChecksum{}
}

type DefinitionRecord struct {
	ID            strategymodel.DefinitionID
	SchemaVersion string
	Name          string
	Description   string
}

func NewDefinitionRecord(
	id strategymodel.DefinitionID,
	schemaVersion string,
	name string,
	description string,
) (DefinitionRecord, error) {
	schemaVersion = strings.TrimSpace(schemaVersion)
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if _, err := strategymodel.NewDefinitionID(id.String()); err != nil ||
		schemaVersion == "" || name == "" || description == "" ||
		len(name) > 128 || len(description) > 4096 {
		return DefinitionRecord{}, ErrInvalidPublication
	}
	return DefinitionRecord{
		ID: id, SchemaVersion: schemaVersion, Name: name, Description: description,
	}, nil
}

func (record DefinitionRecord) Validate() error {
	if _, err := NewDefinitionRecord(
		record.ID, record.SchemaVersion, record.Name, record.Description,
	); err != nil {
		return err
	}
	return nil
}

type RegistrationStatus string

const (
	RegistrationCommitted  RegistrationStatus = "COMMITTED"
	RegistrationIdempotent RegistrationStatus = "IDEMPOTENT_REPLAY"
)

type RegistrationOutcome struct {
	Status RegistrationStatus
}

type PublicationStatus string

const (
	PublicationCommitted         PublicationStatus = "COMMITTED"
	PublicationIdempotent        PublicationStatus = "IDEMPOTENT_REPLAY"
	PublicationRevisionConflict  PublicationStatus = "REVISION_CONFLICT"
	PublicationIdentityCollision PublicationStatus = "IDENTITY_COLLISION"
	PublicationInvalid           PublicationStatus = "INVALID_PUBLICATION"
	PublicationCorruptPriorState PublicationStatus = "CORRUPTED_PRIOR_STATE"
	PublicationCancelled         PublicationStatus = "CANCELLED"
	PublicationInternalFailure   PublicationStatus = "INTERNAL_STORAGE_FAILURE"
)

type PublicationOutcome struct {
	Status              PublicationStatus
	CheckpointRevision  uint64
	CheckpointChecksum  CheckpointChecksum
	PublicationChecksum PublicationChecksum
}

type RestoreExpectation struct {
	InstanceID         domain.StrategyID
	DefinitionID       strategymodel.DefinitionID
	VersionID          strategymodel.VersionID
	ConfigurationHash  strategymodel.ConfigurationHash
	InstanceRevisionID strategymodel.InstanceRevisionID
	StateSchemaVersion string
	Revision           uint64
}

type EvaluationPublication struct {
	InstanceID            domain.StrategyID
	DefinitionID          strategymodel.DefinitionID
	VersionID             strategymodel.VersionID
	InstanceRevisionID    strategymodel.InstanceRevisionID
	ConfigurationHash     strategymodel.ConfigurationHash
	EvaluationID          strategymodel.EvaluationID
	FrameID               strategymodel.FrameID
	ExpectedStateRevision uint64
	Checkpoint            RuntimeCheckpoint
	Record                strategymodel.EvaluationRecord
	Observation           *strategymodel.StrategyObservation
	Proposal              *strategymodel.TradeProposal
	Checksum              PublicationChecksum
}

type DefinitionRegistry interface {
	RegisterDefinition(context.Context, DefinitionRecord) (RegistrationOutcome, error)
	RegisterVersion(context.Context, strategymodel.Descriptor) (RegistrationOutcome, error)
	Definition(context.Context, strategymodel.DefinitionID) (DefinitionRecord, error)
	Version(context.Context, strategymodel.VersionID) (strategymodel.Descriptor, error)
	Definitions(context.Context) ([]DefinitionRecord, error)
	Versions(context.Context, strategymodel.DefinitionID) ([]strategymodel.Descriptor, error)
}

type InstanceRepository interface {
	PutInstance(
		context.Context,
		strategymodel.InstanceRevisionID,
		strategymodel.StrategyInstance,
	) (RegistrationOutcome, error)
	Instance(context.Context, domain.StrategyID) (strategymodel.StrategyInstance, error)
	Instances(context.Context) ([]strategymodel.StrategyInstance, error)
}

type CheckpointRepository interface {
	InitializeCheckpoint(context.Context, RuntimeCheckpoint) (RegistrationOutcome, error)
	CurrentCheckpoint(context.Context, domain.StrategyID) (RuntimeCheckpoint, error)
	Checkpoint(context.Context, domain.StrategyID, uint64) (RuntimeCheckpoint, error)
	Checkpoints(context.Context, domain.StrategyID) ([]RuntimeCheckpoint, error)
	Restore(context.Context, []byte, RestoreExpectation) (RuntimeCheckpoint, error)
}

type EvaluationRecordRepository interface {
	Evaluation(context.Context, strategymodel.EvaluationID) (strategymodel.EvaluationRecord, error)
	Evaluations(context.Context, domain.StrategyID) ([]strategymodel.EvaluationRecord, error)
}

type ObservationRepository interface {
	Observation(context.Context, strategymodel.EvaluationID) (strategymodel.StrategyObservation, error)
	Observations(context.Context, domain.StrategyID) ([]strategymodel.StrategyObservation, error)
}

type TradeProposalRepository interface {
	Proposal(context.Context, strategymodel.ProposalID) (strategymodel.TradeProposal, error)
	Proposals(context.Context, domain.StrategyID) ([]strategymodel.TradeProposal, error)
}

type EvaluationPublisher interface {
	PublishEvaluation(context.Context, EvaluationPublication) (PublicationOutcome, error)
}
