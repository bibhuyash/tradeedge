package ingestion

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	accountingengine "github.com/bibhuyash/tradeedge/internal/accounting/engine"
	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	accountingstorage "github.com/bibhuyash/tradeedge/internal/accounting/storage"
	"github.com/bibhuyash/tradeedge/internal/domain"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

var (
	ErrInvalidRequest      = errors.New("invalid fill ingestion request")
	ErrInvalidLineage      = errors.New("invalid OMS fill lineage")
	ErrUnknownInstrument   = errors.New("unknown canonical instrument")
	ErrInvalidAccountScope = errors.New("invalid portfolio/account binding")
	ErrDuplicateInProgress = errors.New("fill ingestion already in progress")
	ErrShutdown            = errors.New("fill ingestion coordinator is shut down")
	ErrRebuildRequired     = errors.New("canonical predecessor requires verified rebuild")
	ErrInternal            = errors.New("fill ingestion internal failure")
)

type InstrumentRegistry interface {
	Known(context.Context, domain.InstrumentID) (bool, error)
}
type AccountBindingResolver interface {
	Resolve(context.Context, portfoliomodel.PortfolioID, time.Time) (accountingmodel.AccountBinding, error)
}
type Lineage struct {
	Fill   executionmodel.Fill
	Report executionmodel.ExecutionReport
	Order  executionmodel.Order
	Plan   executionmodel.OrderPlan
	Intent executionmodel.ExecutionIntent
}
type LineageReader interface {
	Resolve(context.Context, executionmodel.Fill) (Lineage, error)
}
type OMSRepository interface {
	Order(context.Context, executionmodel.OrderID) (executionmodel.Order, error)
	Plan(context.Context, executionmodel.OrderPlanID) (executionmodel.OrderPlan, error)
	Intent(context.Context, executionmodel.ExecutionIntentID) (executionmodel.ExecutionIntent, error)
	Reports(context.Context, executionmodel.OrderID) ([]executionmodel.ExecutionReport, error)
	Fills(context.Context, executionmodel.OrderID) ([]executionmodel.Fill, error)
}
type RepositoryLineageReader struct{ repository OMSRepository }

func NewRepositoryLineageReader(repository OMSRepository) (RepositoryLineageReader, error) {
	if repository == nil {
		return RepositoryLineageReader{}, ErrInvalidRequest
	}
	return RepositoryLineageReader{repository}, nil
}
func (reader RepositoryLineageReader) Resolve(ctx context.Context, candidate executionmodel.Fill) (Lineage, error) {
	if candidate.ID().IsZero() {
		return Lineage{}, ErrInvalidLineage
	}
	order, err := reader.repository.Order(ctx, candidate.Spec().OrderID)
	if err != nil {
		return Lineage{}, err
	}
	fills, err := reader.repository.Fills(ctx, order.ID())
	if err != nil {
		return Lineage{}, err
	}
	var fill executionmodel.Fill
	for _, value := range fills {
		if value.ID() == candidate.ID() {
			if !bytes.Equal(value.CanonicalJSON(), candidate.CanonicalJSON()) {
				return Lineage{}, ErrInvalidLineage
			}
			fill = value
			break
		}
	}
	if fill.ID().IsZero() {
		return Lineage{}, ErrInvalidLineage
	}
	reports, err := reader.repository.Reports(ctx, order.ID())
	if err != nil {
		return Lineage{}, err
	}
	var report executionmodel.ExecutionReport
	for _, value := range reports {
		if value.ID() == fill.Spec().ReportID {
			report = value
			break
		}
	}
	if report.ID().IsZero() {
		return Lineage{}, ErrInvalidLineage
	}
	plan, err := reader.repository.Plan(ctx, order.Spec().PlanID)
	if err != nil {
		return Lineage{}, err
	}
	intent, err := reader.repository.Intent(ctx, plan.IntentID())
	if err != nil {
		return Lineage{}, err
	}
	return Lineage{fill, report, order, plan, intent}, nil
}

type Applier interface {
	ApplyIngestedFill(context.Context, accountingmodel.AccountingFill, accountingmodel.IngestionMetadata) (accountingstorage.PublicationReceipt, error)
}

type Request struct {
	Fill             executionmodel.Fill
	Report           executionmodel.ExecutionReport
	Order            executionmodel.Order
	Plan             executionmodel.OrderPlan
	Intent           executionmodel.ExecutionIntent
	Binding          accountingmodel.AccountBinding
	SourceSequence   uint64
	SourceCheckpoint string
	SourceChecksum   accountingmodel.StateChecksum
	ReceivedAt       time.Time
}

type Result struct {
	Receipt   accountingstorage.PublicationReceipt
	Ingestion accountingmodel.IngestionID
	Rebuild   *RebuildEvidence
}

type RebuildEvidence struct {
	IngestionID accountingmodel.IngestionID
	PositionID  accountingmodel.PositionID
	FillID      executionmodel.FillID
	OrderingKey accountingmodel.FillOrderingKey
	Reason      string
}

type Config struct {
	MaxConcurrency     int
	Timeout            time.Duration
	MaximumQuarantines int
}

func DefaultConfig() Config {
	return Config{MaxConcurrency: 4, Timeout: time.Second, MaximumQuarantines: 1024}
}

type Coordinator struct {
	applier     Applier
	instruments InstrumentRegistry
	bindings    AccountBindingResolver
	lineage     LineageReader
	config      Config
	semaphore   chan struct{}
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	running     map[accountingmodel.IngestionID]accountingmodel.StateChecksum
	quarantines []RebuildEvidence
	closed      bool
	wait        sync.WaitGroup
	stopOnce    sync.Once
	stopped     chan struct{}
}

func New(applier Applier, instruments InstrumentRegistry, bindings AccountBindingResolver, lineage LineageReader, config Config) (*Coordinator, error) {
	if applier == nil || instruments == nil || bindings == nil || lineage == nil || config.MaxConcurrency <= 0 || config.MaxConcurrency > 64 || config.Timeout <= 0 || config.Timeout > time.Minute || config.MaximumQuarantines <= 0 || config.MaximumQuarantines > 10000 {
		return nil, ErrInvalidRequest
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Coordinator{applier: applier, instruments: instruments, bindings: bindings, lineage: lineage, config: config, semaphore: make(chan struct{}, config.MaxConcurrency), ctx: ctx, cancel: cancel, running: map[accountingmodel.IngestionID]accountingmodel.StateChecksum{}, stopped: make(chan struct{})}, nil
}

func (runner *Coordinator) Ingest(ctx context.Context, request Request) (result Result, err error) {
	fill, metadata, err := runner.normalize(ctx, request)
	if err != nil {
		return Result{}, err
	}
	result.Ingestion = metadata.ID
	if err = runner.reserve(metadata.ID, metadata.SourceChecksum); err != nil {
		return result, err
	}
	defer runner.release(metadata.ID)
	select {
	case runner.semaphore <- struct{}{}:
		defer func() { <-runner.semaphore }()
	case <-ctx.Done():
		return result, ctx.Err()
	case <-runner.ctx.Done():
		return result, ErrShutdown
	}
	opctx, cancel := context.WithTimeout(ctx, runner.config.Timeout)
	defer cancel()
	stop := context.AfterFunc(runner.ctx, cancel)
	defer stop()
	defer func() {
		if recover() != nil {
			result = Result{Ingestion: metadata.ID}
			err = ErrInternal
		}
	}()
	result.Receipt, err = runner.applier.ApplyIngestedFill(opctx, fill, metadata)
	if errors.Is(err, accountingengine.ErrOutOfOrderFill) {
		evidence := RebuildEvidence{IngestionID: metadata.ID, PositionID: fill.PositionID(), FillID: fill.Spec().Fill.ID(), OrderingKey: fill.OrderingKey(), Reason: "CANONICAL_PREDECESSOR"}
		runner.mu.Lock()
		runner.quarantines = append(runner.quarantines, evidence)
		if len(runner.quarantines) > runner.config.MaximumQuarantines {
			runner.quarantines = append([]RebuildEvidence(nil), runner.quarantines[len(runner.quarantines)-runner.config.MaximumQuarantines:]...)
		}
		runner.mu.Unlock()
		result.Rebuild = &evidence
		return result, fmt.Errorf("%w: %v", ErrRebuildRequired, err)
	}
	return result, err
}

func (runner *Coordinator) normalize(ctx context.Context, request Request) (accountingmodel.AccountingFill, accountingmodel.IngestionMetadata, error) {
	if err := ctx.Err(); err != nil {
		return accountingmodel.AccountingFill{}, accountingmodel.IngestionMetadata{}, err
	}
	if request.Fill.ID().IsZero() || request.Report.ID().IsZero() || request.Order.IsZero() || request.Plan.IsZero() || request.Intent.IsZero() || request.ReceivedAt.IsZero() {
		return accountingmodel.AccountingFill{}, accountingmodel.IngestionMetadata{}, ErrInvalidRequest
	}
	authoritative, lineageErr := runner.lineage.Resolve(ctx, request.Fill)
	if lineageErr != nil || !bytes.Equal(authoritative.Fill.CanonicalJSON(), request.Fill.CanonicalJSON()) || !bytes.Equal(authoritative.Report.CanonicalJSON(), request.Report.CanonicalJSON()) || !bytes.Equal(authoritative.Order.CanonicalJSON(), request.Order.CanonicalJSON()) || !bytes.Equal(authoritative.Plan.CanonicalJSON(), request.Plan.CanonicalJSON()) || !bytes.Equal(authoritative.Intent.CanonicalJSON(), request.Intent.CanonicalJSON()) {
		return accountingmodel.AccountingFill{}, accountingmodel.IngestionMetadata{}, ErrInvalidLineage
	}
	fillSpec, reportSpec, orderSpec := request.Fill.Spec(), request.Report.Spec(), request.Order.Spec()
	portfolioID := request.Intent.Spec().Decision.Spec().PortfolioID
	if fillSpec.OrderID != request.Order.ID() || fillSpec.ReportID != request.Report.ID() || reportSpec.OrderID != request.Order.ID() || reportSpec.ClientOrderID != request.Order.ClientOrderID() ||
		orderSpec.PlanID != request.Plan.ID() || request.Plan.IntentID() != request.Intent.ID() || orderSpec.Leg.InstrumentID.IsZero() ||
		reportSpec.Type != executionmodel.ReportPartialFill && reportSpec.Type != executionmodel.ReportFill || reportSpec.Reason != executionmodel.ReasonBrokerFill {
		return accountingmodel.AccountingFill{}, accountingmodel.IngestionMetadata{}, ErrInvalidLineage
	}
	if request.ReceivedAt.UTC() != reportSpec.ReceivedAt.UTC() || fillSpec.OccurredAt.UTC() != reportSpec.OccurredAt.UTC() ||
		fillSpec.Quantity.Int64() > reportSpec.CumulativeFilled || reportSpec.CumulativeFilled > orderSpec.Leg.Quantity.Int64() ||
		fillSpec.Price.Currency() != orderSpec.Leg.LimitPrice.Currency() {
		return accountingmodel.AccountingFill{}, accountingmodel.IngestionMetadata{}, ErrInvalidLineage
	}
	resolvedBinding, bindingErr := runner.bindings.Resolve(ctx, portfolioID, fillSpec.OccurredAt)
	if bindingErr != nil || request.Binding.Validate() != nil || request.Binding.PortfolioID != portfolioID || request.Binding.ValidFrom.After(fillSpec.OccurredAt) || resolvedBinding.Checksum() != request.Binding.Checksum() {
		return accountingmodel.AccountingFill{}, accountingmodel.IngestionMetadata{}, ErrInvalidAccountScope
	}
	known, err := runner.instruments.Known(ctx, orderSpec.Leg.InstrumentID)
	if err != nil {
		return accountingmodel.AccountingFill{}, accountingmodel.IngestionMetadata{}, err
	}
	if !known {
		return accountingmodel.AccountingFill{}, accountingmodel.IngestionMetadata{}, ErrUnknownInstrument
	}
	accountingFill, err := accountingmodel.NewAccountingFill(accountingmodel.AccountingFillSpec{SchemaVersion: "accounting-fill/v1", Fill: request.Fill, PortfolioID: portfolioID, InstrumentID: orderSpec.Leg.InstrumentID, Side: orderSpec.Leg.Side, ReceivedAt: request.ReceivedAt})
	if err != nil {
		return accountingmodel.AccountingFill{}, accountingmodel.IngestionMetadata{}, err
	}
	metadata, err := accountingmodel.NewIngestionMetadata(request.Fill, request.SourceSequence, request.SourceCheckpoint, request.SourceChecksum, request.Binding)
	return accountingFill, metadata, err
}

func (runner *Coordinator) reserve(id accountingmodel.IngestionID, checksum accountingmodel.StateChecksum) error {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.closed {
		return ErrShutdown
	}
	if existing, ok := runner.running[id]; ok {
		if existing == checksum {
			return ErrDuplicateInProgress
		}
		return accountingstorage.ErrIdentityCollision
	}
	runner.running[id] = checksum
	runner.wait.Add(1)
	return nil
}
func (runner *Coordinator) release(id accountingmodel.IngestionID) {
	runner.mu.Lock()
	delete(runner.running, id)
	runner.wait.Done()
	runner.mu.Unlock()
}
func (runner *Coordinator) Quarantines() []RebuildEvidence {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]RebuildEvidence(nil), runner.quarantines...)
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

type StaticInstrumentRegistry map[domain.InstrumentID]struct{}

func (registry StaticInstrumentRegistry) Known(ctx context.Context, id domain.InstrumentID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, ok := registry[id]
	return ok, nil
}

type StaticAccountBindings map[portfoliomodel.PortfolioID]accountingmodel.AccountBinding

func (bindings StaticAccountBindings) Resolve(ctx context.Context, portfolioID portfoliomodel.PortfolioID, at time.Time) (accountingmodel.AccountBinding, error) {
	if err := ctx.Err(); err != nil {
		return accountingmodel.AccountBinding{}, err
	}
	value, ok := bindings[portfolioID]
	if !ok || value.Validate() != nil || value.ValidFrom.After(at) {
		return accountingmodel.AccountBinding{}, ErrInvalidAccountScope
	}
	return value, nil
}

type StaticLineageReader struct{ Values []Lineage }

func (reader StaticLineageReader) Resolve(ctx context.Context, fill executionmodel.Fill) (Lineage, error) {
	if err := ctx.Err(); err != nil {
		return Lineage{}, err
	}
	for _, value := range reader.Values {
		if value.Fill.ID() == fill.ID() {
			return value, nil
		}
	}
	return Lineage{}, ErrInvalidLineage
}
