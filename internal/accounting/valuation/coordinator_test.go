package valuation

import (
	"context"
	"errors"
	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	"sync"
	"testing"
	"time"
)

type sources struct {
	mu                             sync.Mutex
	positions                      []accountingmodel.PositionSnapshot
	marks                          map[accountingmodel.PositionID]MarkPrice
	positionManifest, markManifest accountingmodel.StateChecksum
	change                         bool
}

func (s *sources) Positions(context.Context, portfoliomodel.PortfolioID) ([]accountingmodel.PositionSnapshot, accountingmodel.StateChecksum, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sum := s.positionManifest
	if s.change {
		s.change = false
		sum, _ = accountingmodel.NewStateChecksum("changed", []byte("changed"))
	}
	return append([]accountingmodel.PositionSnapshot(nil), s.positions...), sum, nil
}
func (s *sources) Marks(context.Context, []accountingmodel.PositionSnapshot) (map[accountingmodel.PositionID]MarkPrice, accountingmodel.StateChecksum, error) {
	result := map[accountingmodel.PositionID]MarkPrice{}
	for key, value := range s.marks {
		result[key] = value
	}
	return result, s.markManifest, nil
}

type testRepository struct {
	snapshot   PortfolioFinancialSnapshot
	checkpoint accountingmodel.StateChecksum
}

func (r *testRepository) Current(context.Context, portfoliomodel.PortfolioID) (PortfolioFinancialSnapshot, accountingmodel.StateChecksum, error) {
	if r.snapshot.Checksum.IsZero() {
		return PortfolioFinancialSnapshot{}, accountingmodel.StateChecksum{}, ErrNotFound
	}
	return r.snapshot, r.checkpoint, nil
}
func (r *testRepository) Valuations(context.Context, portfoliomodel.PortfolioID, int) ([]PositionValuation, error) {
	return nil, nil
}
func (r *testRepository) LastComplete(context.Context, portfoliomodel.PortfolioID) (PortfolioFinancialSnapshot, error) {
	if r.snapshot.Status != StatusComplete {
		return PortfolioFinancialSnapshot{}, ErrNotFound
	}
	return r.snapshot, nil
}
func (r *testRepository) Publish(_ context.Context, p Publication) (Receipt, error) {
	if r.snapshot.Revision != p.ExpectedRevision {
		return Receipt{}, ErrStaleRevision
	}
	r.snapshot = p.Snapshot
	r.checkpoint = p.CheckpointChecksum
	return Receipt{Revision: p.Snapshot.Revision, SnapshotID: p.Snapshot.ID, CheckpointChecksum: p.CheckpointChecksum}, nil
}
func (r *testRepository) Restore(context.Context, []Publication) error { return nil }
func TestCoordinatorPublicationIdempotencyConflictAndRestore(t *testing.T) {
	p := testPosition(t, domain.SideBuy, 10, 100)
	at := accountingfixtureTime()
	m := testMark(t, p, 120, at, readiness.StateReady)
	positionManifest, _ := accountingmodel.NewStateChecksum("positions", []byte("v1"))
	markManifest, _ := accountingmodel.NewStateChecksum("marks", []byte("v1"))
	source := &sources{positions: []accountingmodel.PositionSnapshot{p}, marks: map[accountingmodel.PositionID]MarkPrice{p.ID(): m}, positionManifest: positionManifest, markManifest: markManifest}
	store := &testRepository{}
	runner, err := NewCoordinator(source, source, store, nil, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Value(context.Background(), p.Spec().PortfolioID, at.Add(time.Millisecond))
	if err != nil || first.Revision != 1 || first.Idempotent {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := runner.Value(context.Background(), p.Spec().PortfolioID, at.Add(time.Millisecond))
	if err != nil || !second.Idempotent || second.SnapshotID != first.SnapshotID {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	source.change = true
	if _, err = runner.Value(context.Background(), p.Spec().PortfolioID, at.Add(2*time.Millisecond)); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("source conflict=%v", err)
	}
	current, _, err := store.Current(context.Background(), p.Spec().PortfolioID)
	if err != nil || current.Revision != 1 {
		t.Fatalf("atomic rollback revision=%d err=%v", current.Revision, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = runner.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if !runner.Health().Closed {
		t.Fatal("shutdown did not close coordinator")
	}
}
func accountingfixtureTime() time.Time {
	return time.Date(2026, time.January, 5, 9, 15, 1, 0, time.UTC)
}
