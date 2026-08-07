package coordinator_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accountingcoordinator "github.com/bibhuyash/tradeedge/internal/accounting/coordinator"
	accountingengine "github.com/bibhuyash/tradeedge/internal/accounting/engine"
	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	accountingreplay "github.com/bibhuyash/tradeedge/internal/accounting/replay"
	accountingstorage "github.com/bibhuyash/tradeedge/internal/accounting/storage"
	accountingfixture "github.com/bibhuyash/tradeedge/internal/accounting/testfixture"
	memory "github.com/bibhuyash/tradeedge/internal/adapters/accounting/memory"
	"github.com/bibhuyash/tradeedge/internal/domain"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

func newRunner(t *testing.T, repository accountingstorage.Repository) *accountingcoordinator.Coordinator {
	t.Helper()
	runner, err := accountingcoordinator.New(repository, accountingcoordinator.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestDuplicateConflictAtomicRollbackAndDefensiveReads(t *testing.T) {
	store := memory.NewDefault()
	runner := newRunner(t, store)
	at := accountingfixture.BaseTime
	fill, _ := accountingfixture.Fill(1, domain.SideBuy, 10, 100, at, at)
	receipt, err := runner.ApplyFill(context.Background(), fill)
	if err != nil {
		t.Fatal(err)
	}
	again, err := runner.ApplyFill(context.Background(), fill)
	if err != nil || again.Status != accountingstorage.PublicationIdempotent || again.Revision != receipt.Revision {
		t.Fatalf("duplicate: %#v %v", again, err)
	}
	changed, _ := accountingfixture.FillWithIdentity("execution-001", domain.SideBuy, 10, 101, at, at)
	if _, err = runner.ApplyFill(context.Background(), changed); !errors.Is(err, accountingstorage.ErrIdentityCollision) {
		t.Fatalf("conflict: %v", err)
	}
	snapshot, _ := store.Position(context.Background(), fill.PositionID())
	spec := snapshot.Spec()
	spec.OpenLot.Quantity = 999
	stored, _ := store.Position(context.Background(), fill.PositionID())
	if stored.Spec().OpenLot.Quantity == 999 {
		t.Fatal("repository leaked mutable lot")
	}

	failedStore := memory.NewDefault()
	failedStore.SetFailBeforeCommit(true)
	failedRunner := newRunner(t, failedStore)
	if _, err = failedRunner.ApplyFill(context.Background(), fill); !errors.Is(err, accountingstorage.ErrInternal) {
		t.Fatalf("failure injection: %v", err)
	}
	if _, err = failedStore.Position(context.Background(), fill.PositionID()); !errors.Is(err, accountingstorage.ErrNotFound) {
		t.Fatal("failed publication exposed position")
	}
	if _, _, err = failedStore.ApplicationByFill(context.Background(), fill.Spec().Fill.ID()); !errors.Is(err, accountingstorage.ErrNotFound) {
		t.Fatal("failed publication exposed application")
	}
	overflow, _ := accountingfixture.Fill(2, domain.SideBuy, 2, math.MaxInt64, at.Add(time.Second), at.Add(time.Second))
	overflowStore := memory.NewDefault()
	if _, err = newRunner(t, overflowStore).ApplyFill(context.Background(), overflow); !errors.Is(err, accountingengine.ErrArithmeticOverflow) {
		t.Fatalf("overflow publication: %v", err)
	}
	if _, err = overflowStore.Position(context.Background(), overflow.PositionID()); !errors.Is(err, accountingstorage.ErrNotFound) {
		t.Fatal("overflow published state")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = newRunner(t, memory.NewDefault()).ApplyFill(cancelled, fill); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled application: %v", err)
	}
}

func TestBoundedStoreFailsClosedAtCapacity(t *testing.T) {
	store, err := memory.New(memory.Limits{Positions: 1, Revisions: 1, Applications: 1, Publications: 1})
	if err != nil {
		t.Fatal(err)
	}
	runner := newRunner(t, store)
	at := accountingfixture.BaseTime
	first, _ := accountingfixture.Fill(5, domain.SideBuy, 1, 100, at, at)
	if _, err = runner.ApplyFill(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second, _ := accountingfixture.Fill(6, domain.SideBuy, 1, 100, at.Add(time.Second), at.Add(time.Second))
	if _, err = runner.ApplyFill(context.Background(), second); !errors.Is(err, accountingstorage.ErrCapacityExhausted) {
		t.Fatalf("capacity: %v", err)
	}
	position, _ := store.Position(context.Background(), first.PositionID())
	if position.Revision() != 1 || position.Spec().NetQuantity.Int64() != 1 {
		t.Fatal("capacity failure changed position")
	}
}

func TestStaleRevisionPublishesNothing(t *testing.T) {
	store := memory.NewDefault()
	at := accountingfixture.BaseTime
	fill1, _ := accountingfixture.Fill(10, domain.SideBuy, 10, 100, at, at)
	pub1, snapshot1 := buildPublication(t, nil, nil, fill1)
	if _, err := store.PublishPosition(context.Background(), pub1); err != nil {
		t.Fatal(err)
	}
	fill2, _ := accountingfixture.Fill(11, domain.SideSell, 1, 110, at.Add(time.Second), at.Add(time.Second))
	stale, _ := buildPublication(t, &snapshot1, &pub1.NextCheckpoint, fill2)
	fill3, _ := accountingfixture.Fill(12, domain.SideBuy, 1, 100, at.Add(2*time.Second), at.Add(2*time.Second))
	advance, _ := buildPublication(t, &snapshot1, &pub1.NextCheckpoint, fill3)
	if _, err := store.PublishPosition(context.Background(), advance); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PublishPosition(context.Background(), stale); !errors.Is(err, accountingstorage.ErrStaleRevision) {
		t.Fatalf("stale revision: %v", err)
	}
	current, _ := store.Position(context.Background(), fill1.PositionID())
	if current.Spec().AppliedFillCount != 2 {
		t.Fatal("stale publication changed state")
	}
}

func TestConcurrentSerializationBoundedConcurrencyAndShutdown(t *testing.T) {
	base := memory.NewDefault()
	blocking := newBlockingRepository(base)
	runner, _ := accountingcoordinator.New(blocking, accountingcoordinator.Config{MaxConcurrency: 2, Timeout: time.Second})
	at := accountingfixture.BaseTime
	first, _ := accountingfixture.Fill(20, domain.SideBuy, 1, 100, at, at)
	firstDone := make(chan error, 1)
	go func() { _, err := runner.ApplyFill(context.Background(), first); firstDone <- err }()
	<-blocking.entered
	competing, _ := accountingfixture.Fill(21, domain.SideBuy, 1, 100, at.Add(time.Second), at.Add(time.Second))
	if _, err := runner.ApplyFill(context.Background(), competing); !errors.Is(err, accountingcoordinator.ErrPositionBusy) {
		t.Fatalf("same position was not serialized: %v", err)
	}

	second := fillFor(t, 22, "portfolio-2", "instrument-2")
	third := fillFor(t, 23, "portfolio-3", "instrument-3")
	var group sync.WaitGroup
	group.Add(2)
	errorsCh := make(chan error, 2)
	go func() { defer group.Done(); _, err := runner.ApplyFill(context.Background(), second); errorsCh <- err }()
	<-blocking.entered
	go func() { defer group.Done(); _, err := runner.ApplyFill(context.Background(), third); errorsCh <- err }()
	time.Sleep(20 * time.Millisecond)
	if blocking.maximum.Load() != 2 {
		t.Fatalf("maximum concurrent publications=%d", blocking.maximum.Load())
	}
	close(blocking.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	closed, inFlight, keyed, maximum := runner.Health()
	if !closed || inFlight != 0 || keyed != 0 || maximum != 2 {
		t.Fatalf("health: %v %d %d %d", closed, inFlight, keyed, maximum)
	}
	if _, err := runner.ApplyFill(context.Background(), fillFor(t, 24, "portfolio-4", "instrument-4")); !errors.Is(err, accountingcoordinator.ErrShutdown) {
		t.Fatalf("post-shutdown admission: %v", err)
	}
}

func TestShutdownCancelsAcceptedWorkAndDrains(t *testing.T) {
	blocking := newBlockingRepository(memory.NewDefault())
	runner, _ := accountingcoordinator.New(blocking, accountingcoordinator.Config{MaxConcurrency: 1, Timeout: time.Second})
	fill := fillFor(t, 25, "shutdown-portfolio", "shutdown-instrument")
	done := make(chan error, 1)
	go func() { _, err := runner.ApplyFill(context.Background(), fill); done <- err }()
	<-blocking.entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runner.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("accepted work was not cancelled: %v", err)
	}
	closed, inFlight, keyed, _ := runner.Health()
	if !closed || inFlight != 0 || keyed != 0 {
		t.Fatalf("runner did not drain: %v %d %d", closed, inFlight, keyed)
	}
}

func TestReplayRestorationAndCheckpointContinuationAreByteIdentical(t *testing.T) {
	fills := make([]accountingmodel.AccountingFill, 0, 4)
	at := accountingfixture.BaseTime
	for index, item := range []struct {
		side            domain.Side
		quantity, price int64
	}{{domain.SideBuy, 3, 100}, {domain.SideBuy, 2, 200}, {domain.SideSell, 4, 175}, {domain.SideSell, 2, 150}} {
		fill, _ := accountingfixture.Fill(30+index, item.side, item.quantity, item.price, at.Add(time.Duration(index)*time.Second), at.Add(time.Duration(index)*time.Second))
		fills = append(fills, fill)
	}
	controlStore := memory.NewDefault()
	controlRunner := newRunner(t, controlStore)
	replayEngine, _ := accountingreplay.New(controlRunner)
	shuffled := []accountingmodel.AccountingFill{fills[2], fills[0], fills[3], fills[1]}
	if _, err := replayEngine.Replay(context.Background(), shuffled); err != nil {
		t.Fatal(err)
	}
	control, _ := controlStore.Position(context.Background(), fills[0].PositionID())
	publications, _ := controlStore.Publications(context.Background(), fills[0].PositionID())

	restoredStore := memory.NewDefault()
	if err := restoredStore.RestorePosition(context.Background(), publications); err != nil {
		t.Fatal(err)
	}
	restored, _ := restoredStore.Position(context.Background(), fills[0].PositionID())
	if !bytes.Equal(restored.CanonicalJSON(), control.CanonicalJSON()) {
		t.Fatal("full restoration differs")
	}
	corrupt := append([]accountingstorage.PositionPublication(nil), publications...)
	corrupt[1].ExpectedCheckpoint = accountingmodel.StateChecksum{}
	if err := memory.NewDefault().RestorePosition(context.Background(), corrupt); !errors.Is(err, accountingstorage.ErrCorruptCheckpoint) {
		t.Fatalf("corrupt restoration: %v", err)
	}

	continuedStore := memory.NewDefault()
	if err := continuedStore.RestorePosition(context.Background(), publications[:2]); err != nil {
		t.Fatal(err)
	}
	continuedRunner := newRunner(t, continuedStore)
	continuedReplay, _ := accountingreplay.New(continuedRunner)
	if _, err := continuedReplay.Replay(context.Background(), fills[2:]); err != nil {
		t.Fatal(err)
	}
	continued, _ := continuedStore.Position(context.Background(), fills[0].PositionID())
	if !bytes.Equal(continued.CanonicalJSON(), control.CanonicalJSON()) {
		t.Fatal("checkpoint continuation differs")
	}
	controlCheckpoint, _ := controlStore.CurrentPositionCheckpoint(context.Background(), fills[0].PositionID())
	continuedCheckpoint, _ := continuedStore.CurrentPositionCheckpoint(context.Background(), fills[0].PositionID())
	if !bytes.Equal(controlCheckpoint.CanonicalJSON(), continuedCheckpoint.CanonicalJSON()) {
		t.Fatal("continued checkpoint bytes differ")
	}

	otherStore := memory.NewDefault()
	otherRunner := newRunner(t, otherStore)
	otherReplay, _ := accountingreplay.New(otherRunner)
	if _, err := otherReplay.Replay(context.Background(), []accountingmodel.AccountingFill{fills[3], fills[1], fills[0], fills[2]}); err != nil {
		t.Fatal(err)
	}
	other, _ := otherStore.Position(context.Background(), fills[0].PositionID())
	if !bytes.Equal(other.CanonicalJSON(), control.CanonicalJSON()) {
		t.Fatal("replay was not deterministic")
	}
}

func TestEqualTimestampOrderingUsesFillIdentity(t *testing.T) {
	at := accountingfixture.BaseTime
	left, _ := accountingfixture.Fill(40, domain.SideBuy, 1, 100, at, at)
	right, _ := accountingfixture.Fill(41, domain.SideBuy, 1, 200, at, at)
	store := memory.NewDefault()
	runner := newRunner(t, store)
	engine, _ := accountingreplay.New(runner)
	if _, err := engine.Replay(context.Background(), []accountingmodel.AccountingFill{right, left}); err != nil {
		t.Fatal(err)
	}
	applications, _ := store.Applications(context.Background(), left.PositionID())
	if len(applications) != 2 || applications[0].Spec().OrderingKey.Compare(applications[1].Spec().OrderingKey) >= 0 {
		t.Fatal("equal timestamp fills were not ordered by fill ID")
	}
}

func buildPublication(t *testing.T, current *accountingmodel.PositionSnapshot, checkpoint *accountingstorage.PositionCheckpoint, fill accountingmodel.AccountingFill) (accountingstorage.PositionPublication, accountingmodel.PositionSnapshot) {
	t.Helper()
	result, err := accountingengine.Apply(current, fill)
	if err != nil {
		t.Fatal(err)
	}
	var revision accountingmodel.PositionRevision
	var parent accountingmodel.StateChecksum
	if current != nil {
		revision = current.Revision()
		parent = checkpoint.CheckpointChecksum
	}
	next, err := accountingstorage.NewPositionCheckpoint(accountingstorage.PositionCheckpoint{Snapshot: result.Snapshot, ParentChecksum: parent, ApplicationID: result.Application.ID(), FillID: fill.Spec().Fill.ID()})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := accountingmodel.NewPublicationID(fill.PositionID(), fill.Spec().Fill.ID().String())
	publication, err := accountingstorage.NewPositionPublication(accountingstorage.PositionPublication{PublicationID: id, ExpectedRevision: revision, ExpectedCheckpoint: parent, Fill: fill, Application: result.Application, NextCheckpoint: next})
	if err != nil {
		t.Fatal(err)
	}
	return publication, result.Snapshot
}

func fillFor(t *testing.T, sequence int, portfolioKey, instrumentKey string) accountingmodel.AccountingFill {
	t.Helper()
	at := accountingfixture.BaseTime.Add(time.Duration(sequence) * time.Second)
	base, _ := accountingfixture.Fill(sequence, domain.SideBuy, 1, 100, at, at)
	spec := base.Spec()
	spec.PortfolioID, _ = portfoliomodel.NewPortfolioID(portfolioKey)
	spec.InstrumentID, _ = domain.InstrumentIDFromCanonicalKey(instrumentKey)
	value, err := accountingmodel.NewAccountingFill(spec)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

type blockingRepository struct {
	accountingstorage.Repository
	entered          chan struct{}
	release          chan struct{}
	current, maximum atomic.Int64
}

func newBlockingRepository(repository accountingstorage.Repository) *blockingRepository {
	return &blockingRepository{Repository: repository, entered: make(chan struct{}, 8), release: make(chan struct{})}
}
func (repository *blockingRepository) PublishPosition(ctx context.Context, publication accountingstorage.PositionPublication) (accountingstorage.PublicationReceipt, error) {
	current := repository.current.Add(1)
	for {
		maximum := repository.maximum.Load()
		if current <= maximum || repository.maximum.CompareAndSwap(maximum, current) {
			break
		}
	}
	repository.entered <- struct{}{}
	select {
	case <-repository.release:
	case <-ctx.Done():
		repository.current.Add(-1)
		return accountingstorage.PublicationReceipt{}, ctx.Err()
	}
	receipt, err := repository.Repository.PublishPosition(ctx, publication)
	repository.current.Add(-1)
	return receipt, err
}
