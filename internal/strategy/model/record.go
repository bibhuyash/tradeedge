package model

import (
	"errors"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

var ErrInvalidEvaluationRecord = errors.New("invalid strategy evaluation record")

type EvaluationRecordSpec struct {
	EvaluationID       EvaluationID
	DefinitionID       DefinitionID
	VersionID          VersionID
	InstanceID         domain.StrategyID
	InstanceRevisionID InstanceRevisionID
	ConfigurationHash  ConfigurationHash
	FrameID            FrameID
	LogicalTime        time.Time
	ResultKind         ResultKind
	NoActionReason     NoActionReason
	PriorStateHash     StateHash
	NextStateHash      StateHash
	CheckpointRevision uint64
	ObservationCode    string
	ProposalID         ProposalID
}

type EvaluationRecord struct {
	spec EvaluationRecordSpec
}

func NewEvaluationRecord(spec EvaluationRecordSpec) (EvaluationRecord, error) {
	if spec.EvaluationID.IsZero() || spec.DefinitionID == "" ||
		spec.VersionID.IsZero() || strings.TrimSpace(string(spec.InstanceID)) == "" ||
		spec.InstanceRevisionID.IsZero() || spec.ConfigurationHash.IsZero() ||
		spec.FrameID.IsZero() || spec.LogicalTime.IsZero() ||
		spec.PriorStateHash.IsZero() || spec.NextStateHash.IsZero() ||
		spec.CheckpointRevision == 0 {
		return EvaluationRecord{}, ErrInvalidEvaluationRecord
	}
	if _, err := NewDefinitionID(spec.DefinitionID.String()); err != nil {
		return EvaluationRecord{}, ErrInvalidEvaluationRecord
	}
	switch spec.ResultKind {
	case ResultNoAction:
		if !spec.NoActionReason.Valid() || spec.ObservationCode != "" ||
			!spec.ProposalID.IsZero() {
			return EvaluationRecord{}, ErrInvalidEvaluationRecord
		}
	case ResultObservation:
		if spec.NoActionReason != "" || !stableCodePattern.MatchString(spec.ObservationCode) ||
			!spec.ProposalID.IsZero() {
			return EvaluationRecord{}, ErrInvalidEvaluationRecord
		}
	case ResultTradeProposal:
		if spec.NoActionReason != "" || spec.ObservationCode != "" ||
			spec.ProposalID.IsZero() {
			return EvaluationRecord{}, ErrInvalidEvaluationRecord
		}
	default:
		return EvaluationRecord{}, ErrInvalidEvaluationRecord
	}
	spec.LogicalTime = spec.LogicalTime.UTC()
	return EvaluationRecord{spec: spec}, nil
}

func (record EvaluationRecord) EvaluationID() EvaluationID {
	return record.spec.EvaluationID
}
func (record EvaluationRecord) DefinitionID() DefinitionID {
	return record.spec.DefinitionID
}
func (record EvaluationRecord) VersionID() VersionID { return record.spec.VersionID }
func (record EvaluationRecord) InstanceID() domain.StrategyID {
	return record.spec.InstanceID
}
func (record EvaluationRecord) InstanceRevisionID() InstanceRevisionID {
	return record.spec.InstanceRevisionID
}
func (record EvaluationRecord) ConfigurationHash() ConfigurationHash {
	return record.spec.ConfigurationHash
}
func (record EvaluationRecord) FrameID() FrameID       { return record.spec.FrameID }
func (record EvaluationRecord) LogicalTime() time.Time { return record.spec.LogicalTime }
func (record EvaluationRecord) ResultKind() ResultKind { return record.spec.ResultKind }
func (record EvaluationRecord) NoActionReason() NoActionReason {
	return record.spec.NoActionReason
}
func (record EvaluationRecord) PriorStateHash() StateHash { return record.spec.PriorStateHash }
func (record EvaluationRecord) NextStateHash() StateHash  { return record.spec.NextStateHash }
func (record EvaluationRecord) CheckpointRevision() uint64 {
	return record.spec.CheckpointRevision
}
func (record EvaluationRecord) ObservationCode() string { return record.spec.ObservationCode }
func (record EvaluationRecord) ProposalID() ProposalID  { return record.spec.ProposalID }
func (record EvaluationRecord) IsZero() bool            { return record.spec.EvaluationID.IsZero() }
