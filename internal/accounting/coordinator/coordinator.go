package coordinator

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	accountingengine "github.com/bibhuyash/tradeedge/internal/accounting/engine"
	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	accountingstorage "github.com/bibhuyash/tradeedge/internal/accounting/storage"
)

var (
	ErrInvalidConfiguration = errors.New("invalid accounting coordinator configuration")
	ErrDuplicateInProgress  = errors.New("accounting fill is already in progress")
	ErrPositionBusy         = errors.New("position has another fill in progress")
	ErrShutdown             = errors.New("accounting coordinator is shut down")
)

type Config struct {
	MaxConcurrency int
	Timeout        time.Duration
}

func DefaultConfig() Config { return Config{MaxConcurrency: 4, Timeout: 100 * time.Millisecond} }

type Coordinator struct {
	repository accountingstorage.Repository
	config     Config
	semaphore  chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	running    map[accountingmodel.PositionID]accountingmodel.StateChecksum
	closed     bool
	inFlight   atomic.Int64
	wait       sync.WaitGroup
	stopOnce   sync.Once
	stopped    chan struct{}
}

func New(repository accountingstorage.Repository, config Config) (*Coordinator, error) {
	if repository == nil || config.MaxConcurrency <= 0 || config.MaxConcurrency > 64 || config.Timeout <= 0 || config.Timeout > time.Minute {
		return nil, ErrInvalidConfiguration
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Coordinator{repository: repository, config: config, semaphore: make(chan struct{}, config.MaxConcurrency), ctx: ctx, cancel: cancel, running: map[accountingmodel.PositionID]accountingmodel.StateChecksum{}, stopped: make(chan struct{})}, nil
}

func (runner *Coordinator) Health() (closed bool, inFlight, keyed, maximum int) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.closed, int(runner.inFlight.Load()), len(runner.running), runner.config.MaxConcurrency
}

func (runner *Coordinator) ApplyFill(ctx context.Context, fill accountingmodel.AccountingFill) (accountingstorage.PublicationReceipt, error) {
	return runner.applyFill(ctx, fill, nil)
}

func (runner *Coordinator) ApplyIngestedFill(ctx context.Context, fill accountingmodel.AccountingFill, metadata accountingmodel.IngestionMetadata) (accountingstorage.PublicationReceipt, error) {
	if metadata.ID.IsZero() || metadata.Binding.PortfolioID != fill.Spec().PortfolioID {
		return accountingstorage.PublicationReceipt{}, accountingmodel.ErrInvalidIngestion
	}
	return runner.applyFill(ctx, fill, &metadata)
}

func (runner *Coordinator) applyFill(ctx context.Context, fill accountingmodel.AccountingFill, metadata *accountingmodel.IngestionMetadata) (accountingstorage.PublicationReceipt, error) {
	if err := ctx.Err(); err != nil {
		return accountingstorage.PublicationReceipt{}, err
	}
	if fill.IsZero() {
		return accountingstorage.PublicationReceipt{}, accountingengine.ErrInvalidInput
	}
	publicationID, _ := accountingmodel.NewPublicationID(fill.PositionID(), fill.Spec().Fill.ID().String())
	if application, existing, err := runner.repository.ApplicationByFill(ctx, fill.Spec().Fill.ID()); err == nil {
		if !bytes.Equal(existing.CanonicalJSON(), fill.CanonicalJSON()) {
			return accountingstorage.PublicationReceipt{}, &accountingstorage.IdentityCollisionError{Kind: "fill", Identity: fill.Spec().Fill.ID().String()}
		}
		if metadata != nil {
			progress, progressErr := runner.repository.IngestionProgress(ctx, metadata.ID)
			if progressErr != nil || progress.FillID != fill.Spec().Fill.ID() || progress.Metadata.SourceChecksum != metadata.SourceChecksum {
				return accountingstorage.PublicationReceipt{}, &accountingstorage.IdentityCollisionError{Kind: "ingestion", Identity: metadata.ID.String()}
			}
		}
		receipt, receiptErr := runner.repository.CommittedPublication(ctx, publicationID)
		if receiptErr != nil || receipt.ApplicationID != application.ID() {
			return accountingstorage.PublicationReceipt{}, accountingstorage.ErrInternal
		}
		receipt.Status = accountingstorage.PublicationIdempotent
		return receipt, nil
	} else if !errors.Is(err, accountingstorage.ErrNotFound) {
		return accountingstorage.PublicationReceipt{}, err
	}
	if err := runner.reserve(fill.PositionID(), fill.Checksum()); err != nil {
		return accountingstorage.PublicationReceipt{}, err
	}
	defer runner.release(fill.PositionID())
	select {
	case runner.semaphore <- struct{}{}:
		defer func() { <-runner.semaphore }()
	case <-ctx.Done():
		return accountingstorage.PublicationReceipt{}, ctx.Err()
	case <-runner.ctx.Done():
		return accountingstorage.PublicationReceipt{}, ErrShutdown
	}
	operationCtx, cancel := context.WithTimeout(ctx, runner.config.Timeout)
	defer cancel()
	stopCancellation := context.AfterFunc(runner.ctx, cancel)
	defer stopCancellation()
	checkpoint, err := runner.repository.CurrentPositionCheckpoint(operationCtx, fill.PositionID())
	var current *accountingmodel.PositionSnapshot
	var expectedRevision accountingmodel.PositionRevision
	var expectedChecksum accountingmodel.StateChecksum
	if err == nil {
		snapshot := checkpoint.Snapshot
		current = &snapshot
		expectedRevision, expectedChecksum = snapshot.Revision(), checkpoint.CheckpointChecksum
	} else if !errors.Is(err, accountingstorage.ErrNotFound) {
		return accountingstorage.PublicationReceipt{}, err
	}
	result, err := accountingengine.Apply(current, fill)
	if err != nil {
		return accountingstorage.PublicationReceipt{}, err
	}
	nextCheckpoint, err := accountingstorage.NewPositionCheckpoint(accountingstorage.PositionCheckpoint{Snapshot: result.Snapshot, ParentChecksum: expectedChecksum, ApplicationID: result.Application.ID(), FillID: fill.Spec().Fill.ID()})
	if err != nil {
		return accountingstorage.PublicationReceipt{}, err
	}
	var progress *accountingmodel.IngestionProgress
	if metadata != nil {
		value, progressErr := accountingmodel.NewIngestionProgress(accountingmodel.IngestionProgress{Metadata: *metadata, FillID: fill.Spec().Fill.ID(), FillChecksum: fill.Checksum(), PositionID: fill.PositionID(), PositionRevision: result.Snapshot.Revision(), ApplicationID: result.Application.ID(), CheckpointChecksum: nextCheckpoint.CheckpointChecksum})
		if progressErr != nil {
			return accountingstorage.PublicationReceipt{}, progressErr
		}
		progress = &value
	}
	publication, err := accountingstorage.NewPositionPublication(accountingstorage.PositionPublication{PublicationID: publicationID, ExpectedRevision: expectedRevision, ExpectedCheckpoint: expectedChecksum, Fill: fill, Application: result.Application, NextCheckpoint: nextCheckpoint, IngestionProgress: progress})
	if err != nil {
		return accountingstorage.PublicationReceipt{}, err
	}
	if err := operationCtx.Err(); err != nil {
		return accountingstorage.PublicationReceipt{}, err
	}
	return runner.repository.PublishPosition(operationCtx, publication)
}

func (runner *Coordinator) reserve(id accountingmodel.PositionID, checksum accountingmodel.StateChecksum) error {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.closed {
		return ErrShutdown
	}
	if existing, ok := runner.running[id]; ok {
		if existing == checksum {
			return ErrDuplicateInProgress
		}
		return ErrPositionBusy
	}
	runner.running[id] = checksum
	runner.inFlight.Add(1)
	runner.wait.Add(1)
	return nil
}
func (runner *Coordinator) release(id accountingmodel.PositionID) {
	runner.mu.Lock()
	delete(runner.running, id)
	runner.inFlight.Add(-1)
	runner.wait.Done()
	runner.mu.Unlock()
}

func (runner *Coordinator) Shutdown(ctx context.Context) error {
	runner.stopOnce.Do(func() {
		runner.mu.Lock()
		runner.closed = true
		runner.cancel()
		runner.mu.Unlock()
		go func() { runner.wait.Wait(); close(runner.stopped) }()
	})
	select {
	case <-runner.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
