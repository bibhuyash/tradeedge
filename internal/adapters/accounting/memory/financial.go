package memory

import (
	"context"
	"errors"
	"sync"

	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	"github.com/bibhuyash/tradeedge/internal/accounting/valuation"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

type FinancialStore struct {
	mu           sync.RWMutex
	current      map[portfoliomodel.PortfolioID]valuation.PortfolioFinancialSnapshot
	lastComplete map[portfoliomodel.PortfolioID]valuation.PortfolioFinancialSnapshot
	checkpoints  map[portfoliomodel.PortfolioID]accountingmodel.StateChecksum
	valuations   map[portfoliomodel.PortfolioID][]valuation.PositionValuation
	publications map[portfoliomodel.PortfolioID][]valuation.Publication
	capacity     int
}

func NewFinancialStore(capacity int) *FinancialStore {
	if capacity <= 0 || capacity > 10000 {
		capacity = 1000
	}
	return &FinancialStore{current: map[portfoliomodel.PortfolioID]valuation.PortfolioFinancialSnapshot{}, lastComplete: map[portfoliomodel.PortfolioID]valuation.PortfolioFinancialSnapshot{}, checkpoints: map[portfoliomodel.PortfolioID]accountingmodel.StateChecksum{}, valuations: map[portfoliomodel.PortfolioID][]valuation.PositionValuation{}, publications: map[portfoliomodel.PortfolioID][]valuation.Publication{}, capacity: capacity}
}
func (s *FinancialStore) LastComplete(ctx context.Context, id portfoliomodel.PortfolioID) (valuation.PortfolioFinancialSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return valuation.PortfolioFinancialSnapshot{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.lastComplete[id]
	if !ok {
		return value, valuation.ErrNotFound
	}
	return value, nil
}
func (s *FinancialStore) Current(ctx context.Context, id portfoliomodel.PortfolioID) (valuation.PortfolioFinancialSnapshot, accountingmodel.StateChecksum, error) {
	if err := ctx.Err(); err != nil {
		return valuation.PortfolioFinancialSnapshot{}, accountingmodel.StateChecksum{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.current[id]
	if !ok {
		return value, accountingmodel.StateChecksum{}, valuation.ErrNotFound
	}
	return value, s.checkpoints[id], nil
}
func (s *FinancialStore) Valuations(ctx context.Context, id portfoliomodel.PortfolioID, limit int) ([]valuation.PositionValuation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	values, ok := s.valuations[id]
	if !ok {
		return nil, valuation.ErrNotFound
	}
	if len(values) > limit {
		values = values[:limit]
	}
	return append([]valuation.PositionValuation(nil), values...), nil
}
func (s *FinancialStore) Publish(ctx context.Context, p valuation.Publication) (valuation.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return valuation.Receipt{}, err
	}
	if err := valuation.ValidatePublication(p); err != nil {
		return valuation.Receipt{}, err
	}
	id := p.Snapshot.PortfolioID
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.current[id]; ok {
		if current.Revision == p.Snapshot.Revision {
			if current.Checksum == p.Snapshot.Checksum {
				return valuation.Receipt{Revision: p.Snapshot.Revision, SnapshotID: p.Snapshot.ID, CheckpointChecksum: p.CheckpointChecksum, Idempotent: true}, nil
			}
			return valuation.Receipt{}, valuation.ErrIdentityCollision
		}
		if current.Revision != p.ExpectedRevision || s.checkpoints[id] != p.ExpectedCheckpoint {
			return valuation.Receipt{}, valuation.ErrStaleRevision
		}
	} else if p.ExpectedRevision != 0 || !p.ExpectedCheckpoint.IsZero() {
		return valuation.Receipt{}, valuation.ErrStaleRevision
	}
	if len(s.publications[id]) >= s.capacity {
		return valuation.Receipt{}, errors.New("financial store capacity exhausted")
	}
	s.current[id] = p.Snapshot
	if p.Snapshot.Status == valuation.StatusComplete {
		s.lastComplete[id] = p.Snapshot
	}
	s.checkpoints[id] = p.CheckpointChecksum
	s.valuations[id] = append([]valuation.PositionValuation(nil), p.Valuations...)
	s.publications[id] = append(s.publications[id], p)
	return valuation.Receipt{Revision: p.Snapshot.Revision, SnapshotID: p.Snapshot.ID, CheckpointChecksum: p.CheckpointChecksum}, nil
}
func (s *FinancialStore) Restore(ctx context.Context, publications []valuation.Publication) error {
	for _, p := range publications {
		if _, err := s.Publish(ctx, p); err != nil {
			return err
		}
	}
	return nil
}
