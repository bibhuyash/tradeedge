package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
)

var (
	ErrNotFound           = errors.New("accounting record not found")
	ErrIdentityCollision  = errors.New("accounting identity collision")
	ErrStaleRevision      = errors.New("stale position revision")
	ErrInvalidPublication = errors.New("invalid accounting publication")
	ErrCorruptCheckpoint  = errors.New("corrupt accounting checkpoint")
	ErrCapacityExhausted  = errors.New("accounting repository capacity exhausted")
	ErrInternal           = errors.New("accounting repository internal failure")
)

type IdentityCollisionError struct{ Kind, Identity string }

func (value *IdentityCollisionError) Error() string {
	return fmt.Sprintf("%v: %s %s", ErrIdentityCollision, value.Kind, value.Identity)
}
func (value *IdentityCollisionError) Unwrap() error { return ErrIdentityCollision }

type RevisionConflictError struct {
	PositionID       accountingmodel.PositionID
	Expected, Actual accountingmodel.PositionRevision
}

func (value *RevisionConflictError) Error() string {
	return fmt.Sprintf("%v for %s: expected %d, actual %d", ErrStaleRevision, value.PositionID, value.Expected, value.Actual)
}
func (value *RevisionConflictError) Unwrap() error { return ErrStaleRevision }

type PositionCheckpoint struct {
	Snapshot           accountingmodel.PositionSnapshot
	SnapshotChecksum   accountingmodel.StateChecksum
	ParentChecksum     accountingmodel.StateChecksum
	ApplicationID      accountingmodel.FillApplicationID
	FillID             executionmodel.FillID
	CheckpointChecksum accountingmodel.StateChecksum
	canonical          []byte
}

func NewPositionCheckpoint(value PositionCheckpoint) (PositionCheckpoint, error) {
	if value.Snapshot.IsZero() || value.ApplicationID.IsZero() || value.FillID.IsZero() {
		return PositionCheckpoint{}, ErrCorruptCheckpoint
	}
	if !value.SnapshotChecksum.IsZero() && value.SnapshotChecksum != value.Snapshot.Checksum() {
		return PositionCheckpoint{}, ErrCorruptCheckpoint
	}
	value.SnapshotChecksum = value.Snapshot.Checksum()
	if value.Snapshot.Revision() == 1 {
		if !value.ParentChecksum.IsZero() {
			return PositionCheckpoint{}, ErrCorruptCheckpoint
		}
	} else if value.ParentChecksum.IsZero() {
		return PositionCheckpoint{}, ErrCorruptCheckpoint
	}
	raw, err := json.Marshal(struct {
		Snapshot                                                json.RawMessage
		SnapshotChecksum, ParentChecksum, ApplicationID, FillID string
	}{
		value.Snapshot.CanonicalJSON(), value.SnapshotChecksum.String(), value.ParentChecksum.String(), value.ApplicationID.String(), value.FillID.String()})
	if err != nil {
		return PositionCheckpoint{}, ErrCorruptCheckpoint
	}
	checksum, _ := accountingmodel.NewStateChecksum("accounting-position-checkpoint/v1", raw)
	if !value.CheckpointChecksum.IsZero() && value.CheckpointChecksum != checksum {
		return PositionCheckpoint{}, ErrCorruptCheckpoint
	}
	value.CheckpointChecksum, value.canonical = checksum, raw
	return value, nil
}
func (value PositionCheckpoint) CanonicalJSON() []byte {
	return append([]byte(nil), value.canonical...)
}

type PositionPublication struct {
	PublicationID       accountingmodel.PublicationID
	ExpectedRevision    accountingmodel.PositionRevision
	ExpectedCheckpoint  accountingmodel.StateChecksum
	Fill                accountingmodel.AccountingFill
	Application         accountingmodel.FillApplication
	NextCheckpoint      PositionCheckpoint
	PublicationChecksum accountingmodel.StateChecksum
	canonical           []byte
}

func NewPositionPublication(value PositionPublication) (PositionPublication, error) {
	checkpoint, checkpointErr := NewPositionCheckpoint(value.NextCheckpoint)
	if checkpointErr != nil {
		return PositionPublication{}, ErrInvalidPublication
	}
	value.NextCheckpoint = checkpoint
	if value.PublicationID.IsZero() || value.Fill.IsZero() || value.Application.IsZero() || value.NextCheckpoint.Snapshot.IsZero() ||
		value.Application.Spec().PositionID != value.Fill.PositionID() || value.NextCheckpoint.Snapshot.ID() != value.Fill.PositionID() ||
		value.Application.Spec().FillID != value.Fill.Spec().Fill.ID() || value.Application.Spec().FillChecksum != value.Fill.Checksum() ||
		value.Application.Spec().PreviousRevision != value.ExpectedRevision || value.Application.Spec().NextRevision != value.ExpectedRevision+1 ||
		value.NextCheckpoint.Snapshot.Revision() != value.ExpectedRevision+1 || value.NextCheckpoint.ApplicationID != value.Application.ID() ||
		value.NextCheckpoint.FillID != value.Fill.Spec().Fill.ID() || value.Application.Spec().NextSnapshotChecksum != value.NextCheckpoint.Snapshot.Checksum() ||
		value.NextCheckpoint.ParentChecksum != value.ExpectedCheckpoint {
		return PositionPublication{}, ErrInvalidPublication
	}
	if value.ExpectedRevision == 0 {
		if !value.ExpectedCheckpoint.IsZero() || !value.Application.Spec().PreviousSnapshotChecksum.IsZero() {
			return PositionPublication{}, ErrInvalidPublication
		}
	} else if value.ExpectedCheckpoint.IsZero() || value.Application.Spec().PreviousSnapshotChecksum.IsZero() {
		return PositionPublication{}, ErrInvalidPublication
	}
	raw, err := json.Marshal(struct {
		PublicationID, ExpectedCheckpoint string
		ExpectedRevision                  uint64
		Fill, Application, NextCheckpoint json.RawMessage
	}{
		value.PublicationID.String(), value.ExpectedCheckpoint.String(), uint64(value.ExpectedRevision), value.Fill.CanonicalJSON(), value.Application.CanonicalJSON(), value.NextCheckpoint.CanonicalJSON()})
	if err != nil {
		return PositionPublication{}, ErrInvalidPublication
	}
	checksum, _ := accountingmodel.NewStateChecksum("accounting-position-publication/v1", raw)
	if !value.PublicationChecksum.IsZero() && value.PublicationChecksum != checksum {
		return PositionPublication{}, ErrInvalidPublication
	}
	value.PublicationChecksum, value.canonical = checksum, raw
	return value, nil
}
func (value PositionPublication) CanonicalJSON() []byte {
	return append([]byte(nil), value.canonical...)
}

type PublicationStatus string

const (
	PublicationCommitted  PublicationStatus = "COMMITTED"
	PublicationIdempotent PublicationStatus = "IDEMPOTENT_REPLAY"
)

type PublicationReceipt struct {
	Status              PublicationStatus
	PublicationID       accountingmodel.PublicationID
	PositionID          accountingmodel.PositionID
	Revision            accountingmodel.PositionRevision
	FillID              executionmodel.FillID
	ApplicationID       accountingmodel.FillApplicationID
	CheckpointChecksum  accountingmodel.StateChecksum
	PublicationChecksum accountingmodel.StateChecksum
}

type Repository interface {
	CurrentPositionCheckpoint(context.Context, accountingmodel.PositionID) (PositionCheckpoint, error)
	PositionCheckpoint(context.Context, accountingmodel.PositionID, accountingmodel.PositionRevision) (PositionCheckpoint, error)
	Position(context.Context, accountingmodel.PositionID) (accountingmodel.PositionSnapshot, error)
	ApplicationByFill(context.Context, executionmodel.FillID) (accountingmodel.FillApplication, accountingmodel.AccountingFill, error)
	Applications(context.Context, accountingmodel.PositionID) ([]accountingmodel.FillApplication, error)
	Publications(context.Context, accountingmodel.PositionID) ([]PositionPublication, error)
	CommittedPublication(context.Context, accountingmodel.PublicationID) (PublicationReceipt, error)
	PublishPosition(context.Context, PositionPublication) (PublicationReceipt, error)
	RestorePosition(context.Context, []PositionPublication) error
}
