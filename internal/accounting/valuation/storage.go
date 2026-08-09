package valuation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

var (
	ErrNotFound           = errors.New("financial snapshot not found")
	ErrStaleRevision      = errors.New("stale financial revision")
	ErrIdentityCollision  = errors.New("financial publication identity collision")
	ErrInvalidPublication = errors.New("invalid financial publication")
)

type Checkpoint struct {
	Snapshot                 PortfolioFinancialSnapshot
	ParentChecksum, Checksum accountingmodel.StateChecksum
	canonical                []byte
}

// Publication is the single atomic boundary for valuations, snapshot, and checkpoint.
type Publication struct {
	ExpectedRevision   uint64
	ExpectedCheckpoint accountingmodel.StateChecksum
	PositionManifest   accountingmodel.StateChecksum
	MarkManifest       accountingmodel.StateChecksum
	Valuations         []PositionValuation
	Snapshot           PortfolioFinancialSnapshot
	CheckpointChecksum accountingmodel.StateChecksum
}
type Receipt struct {
	Revision           uint64
	SnapshotID         accountingmodel.StateChecksum
	CheckpointChecksum accountingmodel.StateChecksum
	Idempotent         bool
}

type Repository interface {
	Current(context.Context, portfoliomodel.PortfolioID) (PortfolioFinancialSnapshot, accountingmodel.StateChecksum, error)
	LastComplete(context.Context, portfoliomodel.PortfolioID) (PortfolioFinancialSnapshot, error)
	Valuations(context.Context, portfoliomodel.PortfolioID, int) ([]PositionValuation, error)
	Publish(context.Context, Publication) (Receipt, error)
	Restore(context.Context, []Publication) error
}

func ValidatePublication(p Publication) error {
	if p.Snapshot.Checksum.IsZero() || p.Snapshot.Revision != p.ExpectedRevision+1 || len(p.Valuations) != p.Snapshot.PositionCount || p.PositionManifest.IsZero() || p.MarkManifest.IsZero() || p.CheckpointChecksum.IsZero() {
		return ErrInvalidPublication
	}
	for _, v := range p.Valuations {
		if v.PortfolioID != p.Snapshot.PortfolioID || v.Checksum.IsZero() {
			return ErrInvalidPublication
		}
	}
	raw, _ := json.Marshal(struct{ Parent, Snapshot, Positions, Marks string }{
		p.ExpectedCheckpoint.String(), p.Snapshot.Checksum.String(), p.PositionManifest.String(), p.MarkManifest.String(),
	})
	expected, _ := accountingmodel.NewStateChecksum("portfolio-financial-checkpoint/v1", raw)
	if expected != p.CheckpointChecksum {
		return ErrInvalidPublication
	}
	return nil
}
func samePublication(a, b Publication) bool {
	return bytes.Equal(a.Snapshot.CanonicalJSON(), b.Snapshot.CanonicalJSON()) && a.CheckpointChecksum == b.CheckpointChecksum
}
