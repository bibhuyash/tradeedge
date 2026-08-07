package model

import (
	"encoding/json"
	"errors"
	"time"

	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
)

var ErrInvalidFillApplication = errors.New("invalid fill application")

type FillApplicationSpec struct {
	SchemaVersion            string
	ID                       FillApplicationID
	PositionID               PositionID
	FillID                   executionmodel.FillID
	FillChecksum             StateChecksum
	OrderingKey              FillOrderingKey
	PreviousRevision         PositionRevision
	PreviousSnapshotChecksum StateChecksum
	NextRevision             PositionRevision
	NextSnapshotChecksum     StateChecksum
	ClosedQuantity           ClosedQuantity
	OpenedQuantity           OpenQuantity
	AllocatedClosingBasis    CostBasis
	GrossRealizedDelta       RealizedPnL
	AppliedAt                time.Time
}

type FillApplication struct {
	spec      FillApplicationSpec
	checksum  StateChecksum
	canonical []byte
}

func NewFillApplication(spec FillApplicationSpec) (FillApplication, error) {
	if spec.SchemaVersion == "" || spec.ID.IsZero() || spec.PositionID.IsZero() || spec.FillID.IsZero() || spec.FillChecksum.IsZero() ||
		spec.OrderingKey.IsZero() || spec.OrderingKey.FillID != spec.FillID || spec.NextRevision.Validate() != nil ||
		spec.NextRevision != spec.PreviousRevision+1 || spec.NextSnapshotChecksum.IsZero() || spec.ClosedQuantity < 0 || spec.OpenedQuantity < 0 ||
		spec.AllocatedClosingBasis.IsZeroValue() || spec.GrossRealizedDelta.IsZeroValue() ||
		spec.AllocatedClosingBasis.Currency() != spec.GrossRealizedDelta.Currency() || spec.AllocatedClosingBasis.MinorUnits() < 0 || spec.AppliedAt.IsZero() {
		return FillApplication{}, ErrInvalidFillApplication
	}
	if spec.PreviousRevision == 0 {
		if !spec.PreviousSnapshotChecksum.IsZero() {
			return FillApplication{}, ErrInvalidFillApplication
		}
	} else if spec.PreviousSnapshotChecksum.IsZero() {
		return FillApplication{}, ErrInvalidFillApplication
	}
	spec.AppliedAt = spec.AppliedAt.UTC()
	raw, err := json.Marshal(struct {
		SchemaVersion, ID, PositionID, FillID, FillChecksum string
		OccurredAt, ReceivedAt                              string
		PreviousRevision, NextRevision                      uint64
		PreviousSnapshotChecksum, NextSnapshotChecksum      string
		ClosedQuantity                                      ClosedQuantity
		OpenedQuantity                                      OpenQuantity
		AllocatedClosingBasisMinor, GrossRealizedDeltaMinor int64
		Currency, AppliedAt                                 string
	}{spec.SchemaVersion, spec.ID.String(), spec.PositionID.String(), spec.FillID.String(), spec.FillChecksum.String(), spec.OrderingKey.OccurredAt.Format(time.RFC3339Nano), spec.OrderingKey.ReceivedAt.Format(time.RFC3339Nano), uint64(spec.PreviousRevision), uint64(spec.NextRevision), spec.PreviousSnapshotChecksum.String(), spec.NextSnapshotChecksum.String(), spec.ClosedQuantity, spec.OpenedQuantity, spec.AllocatedClosingBasis.MinorUnits(), spec.GrossRealizedDelta.MinorUnits(), spec.GrossRealizedDelta.Currency().String(), spec.AppliedAt.Format(time.RFC3339Nano)})
	if err != nil {
		return FillApplication{}, ErrInvalidFillApplication
	}
	checksum, _ := NewStateChecksum("accounting-fill-application/v1", raw)
	return FillApplication{spec: spec, checksum: checksum, canonical: raw}, nil
}

func (value FillApplication) ID() FillApplicationID     { return value.spec.ID }
func (value FillApplication) Spec() FillApplicationSpec { return value.spec }
func (value FillApplication) Checksum() StateChecksum   { return value.checksum }
func (value FillApplication) CanonicalJSON() []byte     { return append([]byte(nil), value.canonical...) }
func (value FillApplication) IsZero() bool              { return value.spec.ID.IsZero() }
