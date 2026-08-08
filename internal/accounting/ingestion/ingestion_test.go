package ingestion_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	accountingcoordinator "github.com/bibhuyash/tradeedge/internal/accounting/coordinator"
	accountingengine "github.com/bibhuyash/tradeedge/internal/accounting/engine"
	"github.com/bibhuyash/tradeedge/internal/accounting/ingestion"
	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	accountingstorage "github.com/bibhuyash/tradeedge/internal/accounting/storage"
	memory "github.com/bibhuyash/tradeedge/internal/adapters/accounting/memory"
	"github.com/bibhuyash/tradeedge/internal/domain"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	executionfixture "github.com/bibhuyash/tradeedge/internal/execution/testfixture"
)

func request(t *testing.T, sequence int, occurred time.Time, priceMinor int64) (ingestion.Request, ingestion.StaticInstrumentRegistry) {
	t.Helper()
	fixture, err := executionfixture.New(false)
	if err != nil {
		t.Fatal(err)
	}
	order := fixture.Orders[0]
	quantity, _ := domain.NewQuantity(2)
	price, _ := domain.NewPrice(priceMinor, "INR")
	report, err := executionmodel.NewExecutionReport(executionmodel.ExecutionReportSpec{SchemaVersion: "execution-report/v1", Source: "oms", SourceEventID: time.Duration(sequence).String(), OrderID: order.ID(), ClientOrderID: order.ClientOrderID(), Type: executionmodel.ReportPartialFill, Reason: executionmodel.ReasonBrokerFill, CumulativeFilled: 2, OccurredAt: occurred, ReceivedAt: occurred.Add(time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	fill, err := executionmodel.NewFill(executionmodel.FillSpec{SchemaVersion: "fill/v1", Source: "oms", SourceExecutionID: time.Duration(sequence).String(), OrderID: order.ID(), ReportID: report.ID(), Quantity: quantity, Price: price, OccurredAt: occurred})
	if err != nil {
		t.Fatal(err)
	}
	portfolio := fixture.Intent.Spec().Decision.Spec().PortfolioID
	account, _ := domain.NewAccountID("paper-account")
	binding, _ := accountingmodel.NewAccountBinding(portfolio, account, "binding-v1", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	source, _ := accountingmodel.NewStateChecksum("oms-source/v1", fill.CanonicalJSON())
	return ingestion.Request{Fill: fill, Report: report, Order: order, Plan: fixture.Plan, Intent: fixture.Intent, Binding: binding, SourceSequence: uint64(sequence), SourceCheckpoint: time.Duration(sequence).String(), SourceChecksum: source, ReceivedAt: occurred.Add(time.Millisecond)}, ingestion.StaticInstrumentRegistry{order.Spec().Leg.InstrumentID: {}}
}

func runner(t *testing.T, applier ingestion.Applier, registry ingestion.StaticInstrumentRegistry, request ...ingestion.Request) *ingestion.Coordinator {
	t.Helper()
	bindings := ingestion.StaticAccountBindings{}
	if len(request) > 0 {
		bindings[request[0].Binding.PortfolioID] = request[0].Binding
	}
	lineage := ingestion.StaticLineageReader{}
	for _, item := range request {
		lineage.Values = append(lineage.Values, ingestion.Lineage{Fill: item.Fill, Report: item.Report, Order: item.Order, Plan: item.Plan, Intent: item.Intent})
	}
	value, err := ingestion.New(applier, registry, bindings, lineage, ingestion.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestIngestionCommitsProgressAndIsIdempotent(t *testing.T) {
	store := memory.NewDefault()
	accounting, _ := accountingcoordinator.New(store, accountingcoordinator.DefaultConfig())
	at := time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC)
	req, registry := request(t, 1, at, 100)
	ingestor := runner(t, accounting, registry, req)
	first, err := ingestor.Ingest(context.Background(), req)
	if err != nil || first.Receipt.Revision != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	progress, err := store.IngestionProgress(context.Background(), first.Ingestion)
	if err != nil || progress.PositionRevision != 1 {
		t.Fatalf("progress=%+v err=%v", progress, err)
	}
	again, err := ingestor.Ingest(context.Background(), req)
	if err != nil || again.Receipt.Status != accountingstorage.PublicationIdempotent {
		t.Fatalf("retry=%+v err=%v", again, err)
	}
	publications, _ := store.Publications(context.Background(), first.Receipt.PositionID)
	restored := memory.NewDefault()
	if err = restored.RestorePosition(context.Background(), publications); err != nil {
		t.Fatal(err)
	}
	restoredAccounting, _ := accountingcoordinator.New(restored, accountingcoordinator.DefaultConfig())
	restarted := runner(t, restoredAccounting, registry, req)
	if result, retryErr := restarted.Ingest(context.Background(), req); retryErr != nil || result.Receipt.Status != accountingstorage.PublicationIdempotent {
		t.Fatalf("restored retry=%+v err=%v", result, retryErr)
	}
}

func TestSequentialLateAndConflictingFills(t *testing.T) {
	store := memory.NewDefault()
	accounting, _ := accountingcoordinator.New(store, accountingcoordinator.DefaultConfig())
	at := time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC)
	first, registry := request(t, 10, at, 100)
	second, _ := request(t, 11, at.Add(time.Second), 110)
	late, _ := request(t, 12, at.Add(-time.Second), 90)
	sourceCandidate, _ := request(t, 13, at.Add(2*time.Second), 120)
	ingestor := runner(t, accounting, registry, first, second, late, sourceCandidate)
	if _, err := ingestor.Ingest(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if result, err := ingestor.Ingest(context.Background(), second); err != nil || result.Receipt.Revision != 2 {
		t.Fatalf("second=%+v err=%v", result, err)
	}
	result, err := ingestor.Ingest(context.Background(), late)
	if !errors.Is(err, ingestion.ErrRebuildRequired) || result.Rebuild == nil || len(ingestor.Quarantines()) != 1 {
		t.Fatalf("late=%+v err=%v", result, err)
	}
	changed := first
	changed.SourceChecksum, _ = accountingmodel.NewStateChecksum("oms-source/v1", []byte("changed"))
	if _, err = ingestor.Ingest(context.Background(), changed); !errors.Is(err, accountingstorage.ErrIdentityCollision) {
		t.Fatalf("conflict err=%v", err)
	}
	sourceCollision := sourceCandidate
	sourceCollision.SourceSequence = first.SourceSequence
	sourceCollision.SourceCheckpoint = first.SourceCheckpoint
	if _, err = ingestor.Ingest(context.Background(), sourceCollision); !errors.Is(err, accountingstorage.ErrIdentityCollision) {
		t.Fatalf("source collision err=%v", err)
	}
}

func TestEqualTimestampUsesFillIdentityTieBreak(t *testing.T) {
	store := memory.NewDefault()
	accounting, _ := accountingcoordinator.New(store, accountingcoordinator.DefaultConfig())
	at := time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC)
	left, registry := request(t, 40, at, 100)
	right, _ := request(t, 41, at, 101)
	leftKey, _ := accountingmodel.NewFillOrderingKey(left.Fill.Spec().OccurredAt, left.ReceivedAt, left.Fill.ID())
	rightKey, _ := accountingmodel.NewFillOrderingKey(right.Fill.Spec().OccurredAt, right.ReceivedAt, right.Fill.ID())
	if leftKey.Compare(rightKey) > 0 {
		left, right = right, left
	}
	ingestor := runner(t, accounting, registry, left, right)
	if _, err := ingestor.Ingest(context.Background(), left); err != nil {
		t.Fatal(err)
	}
	result, err := ingestor.Ingest(context.Background(), right)
	if err != nil || result.Receipt.Revision != 2 {
		t.Fatalf("tie result=%+v err=%v", result, err)
	}
}

type blockingApplier struct {
	delegate ingestion.Applier
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

type panicApplier struct{}

func (panicApplier) ApplyIngestedFill(context.Context, accountingmodel.AccountingFill, accountingmodel.IngestionMetadata) (accountingstorage.PublicationReceipt, error) {
	panic("injected")
}

func (value *blockingApplier) ApplyIngestedFill(ctx context.Context, fill accountingmodel.AccountingFill, metadata accountingmodel.IngestionMetadata) (accountingstorage.PublicationReceipt, error) {
	value.once.Do(func() { close(value.entered) })
	select {
	case <-value.release:
		return value.delegate.ApplyIngestedFill(ctx, fill, metadata)
	case <-ctx.Done():
		return accountingstorage.PublicationReceipt{}, ctx.Err()
	}
}
func TestDuplicateInProgressAndCancellation(t *testing.T) {
	store := memory.NewDefault()
	accounting, _ := accountingcoordinator.New(store, accountingcoordinator.DefaultConfig())
	at := time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC)
	req, registry := request(t, 20, at, 100)
	block := &blockingApplier{accounting, make(chan struct{}), make(chan struct{}), sync.Once{}}
	config := ingestion.DefaultConfig()
	config.Timeout = time.Second
	ingestor, _ := ingestion.New(block, registry, ingestion.StaticAccountBindings{req.Binding.PortfolioID: req.Binding}, ingestion.StaticLineageReader{Values: []ingestion.Lineage{{Fill: req.Fill, Report: req.Report, Order: req.Order, Plan: req.Plan, Intent: req.Intent}}}, config)
	done := make(chan error, 1)
	go func() { _, err := ingestor.Ingest(context.Background(), req); done <- err }()
	<-block.entered
	if _, err := ingestor.Ingest(context.Background(), req); !errors.Is(err, ingestion.ErrDuplicateInProgress) {
		t.Fatalf("duplicate err=%v", err)
	}
	close(block.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cancelReq, _ := request(t, 21, at.Add(time.Second), 100)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ingestor.Ingest(ctx, cancelReq); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
}

func TestValidationAndAtomicFailure(t *testing.T) {
	store := memory.NewDefault()
	accounting, _ := accountingcoordinator.New(store, accountingcoordinator.DefaultConfig())
	at := time.Date(2026, 1, 5, 9, 15, 0, 0, time.UTC)
	req, registry := request(t, 30, at, 100)
	unknown := ingestion.StaticInstrumentRegistry{}
	if _, err := runner(t, accounting, unknown, req).Ingest(context.Background(), req); !errors.Is(err, ingestion.ErrUnknownInstrument) {
		t.Fatalf("unknown err=%v", err)
	}
	badBinding := req
	badBinding.Binding.Version = "changed"
	if _, err := runner(t, accounting, registry, req).Ingest(context.Background(), badBinding); !errors.Is(err, ingestion.ErrInvalidAccountScope) {
		t.Fatalf("binding err=%v", err)
	}
	if _, err := runner(t, panicApplier{}, registry, req).Ingest(context.Background(), req); !errors.Is(err, ingestion.ErrInternal) {
		t.Fatalf("panic err=%v", err)
	}
	bad := req
	bad.Order = executionmodel.Order{}
	if _, err := runner(t, accounting, registry, req).Ingest(context.Background(), bad); !errors.Is(err, ingestion.ErrInvalidRequest) {
		t.Fatalf("lineage err=%v", err)
	}
	store.SetFailBeforeCommit(true)
	result, err := runner(t, accounting, registry, req).Ingest(context.Background(), req)
	if !errors.Is(err, accountingstorage.ErrInternal) {
		t.Fatalf("failure err=%v", err)
	}
	if _, err = store.IngestionProgress(context.Background(), result.Ingestion); !errors.Is(err, accountingstorage.ErrNotFound) {
		t.Fatalf("partial progress err=%v", err)
	}
	overflow, _ := request(t, 31, at.Add(time.Second), int64(^uint64(0)>>1))
	overflowStore := memory.NewDefault()
	overflowAccounting, _ := accountingcoordinator.New(overflowStore, accountingcoordinator.DefaultConfig())
	overflowResult, overflowErr := runner(t, overflowAccounting, registry, overflow).Ingest(context.Background(), overflow)
	if !errors.Is(overflowErr, accountingengine.ErrArithmeticOverflow) {
		t.Fatalf("overflow err=%v", overflowErr)
	}
	if _, progressErr := overflowStore.IngestionProgress(context.Background(), overflowResult.Ingestion); !errors.Is(progressErr, accountingstorage.ErrNotFound) {
		t.Fatalf("overflow progress err=%v", progressErr)
	}
}
