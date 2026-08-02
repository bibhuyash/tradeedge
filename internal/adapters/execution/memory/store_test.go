package memory_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/adapters/execution/memory"
	"github.com/bibhuyash/tradeedge/internal/domain"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	executionstorage "github.com/bibhuyash/tradeedge/internal/execution/storage"
	"github.com/bibhuyash/tradeedge/internal/risk/testfixture"
)

type fixture struct {
	intent executionmodel.ExecutionIntent
	plan   executionmodel.OrderPlan
	orders []executionmodel.Order
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	decision, err := testfixture.ApprovedDecision()
	if err != nil {
		t.Fatal(err)
	}
	approved, _ := decision.ApprovedAllocation()
	legs := make([]executionmodel.IntentLeg, len(approved.LegBounds))
	for i, bound := range approved.LegBounds {
		legs[i] = executionmodel.IntentLeg{InstrumentID: bound.InstrumentID, Side: bound.Side, Ratio: bound.Ratio, Quantity: bound.MaximumUnits, LotSize: bound.LotSize}
	}
	intent, err := executionmodel.NewExecutionIntent(executionmodel.ExecutionIntentSpec{SchemaVersion: "execution-intent/v1", Decision: decision, MaximumCapital: approved.MaximumCapital, Legs: legs, PortfolioRevision: decision.Spec().ExpectedPortfolioRevision + 1, CreatedAt: decision.Spec().GeneratedAt, ExpiresAt: decision.Spec().ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	price, _ := domain.NewPrice(100, "INR")
	plan, err := executionmodel.NewOrderPlan(executionmodel.OrderPlanSpec{SchemaVersion: "order-plan/v1", Intent: intent, Legs: []executionmodel.OrderLegDraft{{InstrumentID: legs[0].InstrumentID, Side: legs[0].Side, Quantity: legs[0].Quantity, LimitPrice: price, Protective: true}}, CreatedAt: intent.Spec().CreatedAt, ExpiresAt: intent.Spec().ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	order, err := executionmodel.NewOrder(plan, plan.Legs()[0].ID, plan.Spec().CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{intent, plan, []executionmodel.Order{order}}
}

func TestOMSQueriesUseStableTradeEdgeIdentity(t *testing.T) {
	value := newFixture(t)
	store := memory.NewStore()
	if _, err := store.RegisterPlan(context.Background(), value.intent, value.plan, value.orders); err != nil {
		t.Fatal(err)
	}
	order, err := store.OrderByClientOrderID(context.Background(), value.orders[0].ClientOrderID())
	if err != nil || order.ID() != value.orders[0].ID() {
		t.Fatalf("client identity lookup: %v", err)
	}
	orders, err := store.OrdersForPlan(context.Background(), value.plan.ID())
	if err != nil || len(orders) != len(value.orders) {
		t.Fatalf("plan orders: %d %v", len(orders), err)
	}
	nonTerminal, err := store.NonTerminalOrders(context.Background(), 1)
	if err != nil || len(nonTerminal) != 1 || nonTerminal[0].ID() != value.orders[0].ID() {
		t.Fatalf("non-terminal orders: %v %v", nonTerminal, err)
	}
	plans, err := store.RecentPlans(context.Background(), 100)
	if err != nil || len(plans) != 1 || plans[0].ID() != value.plan.ID() {
		t.Fatalf("recent plans: %v %v", plans, err)
	}
	orders, err = store.RecentOrders(context.Background(), executionmodel.OrderCreated, 100)
	if err != nil || len(orders) != 1 || orders[0].ID() != value.orders[0].ID() {
		t.Fatalf("recent orders: %v %v", orders, err)
	}
	health := store.Health()
	if !health.Available || health.Plans != 1 || health.Orders != 1 || health.UnknownOrders != 0 || health.OrderLimit != memory.DefaultLimits().Orders {
		t.Fatalf("OMS health: %+v", health)
	}
	if _, err = store.RecentPlans(context.Background(), 101); !errors.Is(err, executionstorage.ErrInvalidPublication) {
		t.Fatalf("unbounded recent query: %v", err)
	}
}

func transitionReason(kind executionmodel.ReportType) executionmodel.TransitionReason {
	return map[executionmodel.ReportType]executionmodel.TransitionReason{executionmodel.ReportPlanned: executionmodel.ReasonPlanned, executionmodel.ReportSubmissionPending: executionmodel.ReasonSubmissionStarted, executionmodel.ReportSubmitted: executionmodel.ReasonBrokerAccepted, executionmodel.ReportAcknowledged: executionmodel.ReasonBrokerAcknowledged, executionmodel.ReportPartialFill: executionmodel.ReasonBrokerFill, executionmodel.ReportFill: executionmodel.ReasonBrokerFill, executionmodel.ReportCancelPending: executionmodel.ReasonCancellationRequested, executionmodel.ReportCancelled: executionmodel.ReasonBrokerCancelled, executionmodel.ReportRejected: executionmodel.ReasonBrokerRejected, executionmodel.ReportExpired: executionmodel.ReasonAuthorityExpired, executionmodel.ReportFailed: executionmodel.ReasonInternalFailure, executionmodel.ReportUnknown: executionmodel.ReasonSubmissionOutcomeUnknown}[kind]
}

func publication(t *testing.T, current executionstorage.OrderCheckpoint, kind executionmodel.ReportType, cumulative, fillQuantity int64, key string) executionstorage.OrderPublication {
	t.Helper()
	now := current.Order.Spec().UpdatedAt.Add(time.Second)
	report, err := executionmodel.NewExecutionReport(executionmodel.ExecutionReportSpec{SchemaVersion: "execution-report/v1", Source: "TEST", SourceEventID: key, OrderID: current.Order.ID(), ClientOrderID: current.Order.ClientOrderID(), BrokerOrderID: "opaque", Type: kind, Reason: transitionReason(kind), CumulativeFilled: cumulative, OccurredAt: now, ReceivedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	var fill *executionmodel.Fill
	if fillQuantity > 0 {
		quantity, _ := domain.NewQuantity(fillQuantity)
		price, _ := domain.NewPrice(100, "INR")
		value, fillErr := executionmodel.NewFill(executionmodel.FillSpec{SchemaVersion: "fill/v1", Source: "TEST", SourceExecutionID: "fill-" + key, OrderID: current.Order.ID(), ReportID: report.ID(), Quantity: quantity, Price: price, OccurredAt: now})
		if fillErr != nil {
			t.Fatal(fillErr)
		}
		fill = &value
	}
	next, err := executionmodel.ApplyExecutionReport(current.Order, report, fill)
	if err != nil {
		t.Fatal(err)
	}
	checkpointSpec := executionstorage.OrderCheckpoint{Order: next, ParentChecksum: current.CheckpointChecksum, ReportID: report.ID()}
	if fill != nil {
		checkpointSpec.FillID = fill.ID()
	}
	checkpoint, err := executionstorage.NewOrderCheckpoint(checkpointSpec)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := executionmodel.NewPublicationID(current.Order.ID().String(), key)
	value, err := executionstorage.NewOrderPublication(executionstorage.OrderPublication{PublicationID: id, ExpectedRevision: current.Order.Spec().Revision, ExpectedCheckpoint: current.CheckpointChecksum, Report: report, Fill: fill, NextCheckpoint: checkpoint})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func registered(t *testing.T) (*memory.Store, fixture, executionstorage.OrderCheckpoint) {
	t.Helper()
	value := newFixture(t)
	store := memory.NewStore()
	outcome, err := store.RegisterPlan(context.Background(), value.intent, value.plan, value.orders)
	if err != nil || outcome.Status != executionstorage.RegistrationCommitted {
		t.Fatalf("register: %v %v", outcome, err)
	}
	checkpoint, err := store.CurrentOrderCheckpoint(context.Background(), value.orders[0].ID())
	if err != nil {
		t.Fatal(err)
	}
	return store, value, checkpoint
}

func TestAtomicPublicationIdempotencyAndDefensiveReads(t *testing.T) {
	store, value, current := registered(t)
	registration, err := store.RegisterPlan(context.Background(), value.intent, value.plan, value.orders)
	if err != nil || registration.Status != executionstorage.RegistrationIdempotent {
		t.Fatalf("plan retry: %v %v", registration, err)
	}
	changedOrder, _ := executionmodel.NewOrder(value.plan, value.plan.Legs()[0].ID, value.plan.Spec().CreatedAt.Add(time.Second))
	_, err = store.RegisterPlan(context.Background(), value.intent, value.plan, []executionmodel.Order{changedOrder})
	if !errors.Is(err, executionstorage.ErrIdentityCollision) {
		t.Fatalf("changed order identity: %v", err)
	}
	pub := publication(t, current, executionmodel.ReportPlanned, 0, 0, "planned")
	receipt, err := store.PublishOrderEvent(context.Background(), pub)
	if err != nil || receipt.Status != executionstorage.RegistrationCommitted {
		t.Fatalf("publish: %v %v", receipt, err)
	}
	again, err := store.PublishOrderEvent(context.Background(), pub)
	if err != nil || again.Status != executionstorage.RegistrationIdempotent {
		t.Fatalf("retry: %v %v", again, err)
	}
	reports, _ := store.Reports(context.Background(), value.orders[0].ID())
	if len(reports) != 1 {
		t.Fatalf("duplicate reports: %d", len(reports))
	}
	fills, _ := store.Fills(context.Background(), value.orders[0].ID())
	if len(fills) != 0 {
		t.Fatal("unexpected fill")
	}
	plan, _ := store.Plan(context.Background(), value.plan.ID())
	legs := plan.Legs()
	legs[0].DependsOn = append(legs[0].DependsOn, executionmodel.OrderLegID{})
	planAgain, _ := store.Plan(context.Background(), value.plan.ID())
	if len(planAgain.Legs()[0].DependsOn) != 0 {
		t.Fatal("plan read was not defensive")
	}
	raw := pub.CanonicalJSON()
	raw[0] ^= 1
	if bytes.Equal(raw, pub.CanonicalJSON()) {
		t.Fatal("publication bytes were not defensive")
	}
}

func TestStaleRevisionConcurrentPublicationAndAtomicRollback(t *testing.T) {
	store, value, current := registered(t)
	store.SetFailBeforeCommitForTest(true)
	pub := publication(t, current, executionmodel.ReportPlanned, 0, 0, "rollback")
	_, err := store.PublishOrderEvent(context.Background(), pub)
	if !errors.Is(err, executionstorage.ErrInternal) {
		t.Fatalf("rollback error: %v", err)
	}
	after, _ := store.CurrentOrderCheckpoint(context.Background(), value.orders[0].ID())
	if after.Order.Spec().Revision != 1 {
		t.Fatal("failed commit advanced revision")
	}
	reports, _ := store.Reports(context.Background(), value.orders[0].ID())
	if len(reports) != 0 {
		t.Fatal("failed commit exposed report")
	}
	store.SetFailBeforeCommitForTest(false)
	const workers = 32
	var committed, idempotent int32
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			receipt, publishErr := store.PublishOrderEvent(context.Background(), pub)
			if publishErr != nil {
				t.Errorf("concurrent publish: %v", publishErr)
				return
			}
			if receipt.Status == executionstorage.RegistrationCommitted {
				atomic.AddInt32(&committed, 1)
			} else {
				atomic.AddInt32(&idempotent, 1)
			}
		}()
	}
	wait.Wait()
	if committed != 1 || idempotent != workers-1 {
		t.Fatalf("committed=%d idempotent=%d", committed, idempotent)
	}
	other := publication(t, current, executionmodel.ReportExpired, 0, 0, "stale")
	_, err = store.PublishOrderEvent(context.Background(), other)
	if !errors.Is(err, executionstorage.ErrStaleOrderRevision) {
		t.Fatalf("stale revision: %v", err)
	}
}

func TestDuplicateReportAndFillIntegrity(t *testing.T) {
	store, _, current := registered(t)
	planned := publication(t, current, executionmodel.ReportPlanned, 0, 0, "p1")
	_, _ = store.PublishOrderEvent(context.Background(), planned)
	current, _ = store.CurrentOrderCheckpoint(context.Background(), current.Order.ID())
	pending := publication(t, current, executionmodel.ReportSubmissionPending, 0, 0, "p2")
	_, _ = store.PublishOrderEvent(context.Background(), pending)
	current, _ = store.CurrentOrderCheckpoint(context.Background(), current.Order.ID())
	partial := publication(t, current, executionmodel.ReportPartialFill, 20, 20, "p3")
	_, err := store.PublishOrderEvent(context.Background(), partial)
	if err != nil {
		t.Fatal(err)
	}
	duplicateID, _ := executionmodel.NewPublicationID(current.Order.ID().String(), "duplicate-report-wrapper")
	duplicate := partial
	duplicate.PublicationID = duplicateID
	duplicate.PublicationChecksum = executionmodel.StateChecksum{}
	duplicate, err = executionstorage.NewOrderPublication(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := store.PublishOrderEvent(context.Background(), duplicate)
	if err != nil || receipt.Status != executionstorage.RegistrationIdempotent {
		t.Fatalf("duplicate report: %v %v", receipt, err)
	}
	changedQuantity, _ := domain.NewQuantity(10)
	changedPrice, _ := domain.NewPrice(100, "INR")
	changedFill, _ := executionmodel.NewFill(executionmodel.FillSpec{SchemaVersion: "fill/v1", Source: "TEST", SourceExecutionID: "fill-p3", OrderID: partial.Report.Spec().OrderID, ReportID: partial.Report.ID(), Quantity: changedQuantity, Price: changedPrice, OccurredAt: partial.Report.Spec().OccurredAt})
	changedFillPublication := duplicate
	changedFillPublication.Fill = &changedFill
	changedFillPublication.PublicationChecksum = executionmodel.StateChecksum{}
	changedFillPublication, err = executionstorage.NewOrderPublication(changedFillPublication)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.PublishOrderEvent(context.Background(), changedFillPublication)
	if !errors.Is(err, executionstorage.ErrIdentityCollision) {
		t.Fatalf("changed fill identity: %v", err)
	}
	changedReportSpec := partial.Report.Spec()
	changedReportSpec.ReceivedAt = changedReportSpec.ReceivedAt.Add(time.Second)
	changedReport, _ := executionmodel.NewExecutionReport(changedReportSpec)
	changed := partial
	changed.PublicationID = duplicateID
	changed.PublicationChecksum = executionmodel.StateChecksum{}
	changed.Report = changedReport
	changed.NextCheckpoint.ReportID = changedReport.ID()
	changed.NextCheckpoint, _ = executionstorage.NewOrderCheckpoint(changed.NextCheckpoint)
	changed, _ = executionstorage.NewOrderPublication(changed)
	_, err = store.PublishOrderEvent(context.Background(), changed)
	if !errors.Is(err, executionstorage.ErrIdentityCollision) {
		t.Fatalf("changed report identity: %v", err)
	}
	fills, _ := store.Fills(context.Background(), current.Order.ID())
	if len(fills) != 1 || fills[0].Spec().Quantity.Int64() != 20 {
		t.Fatal("fill was duplicated")
	}
}

func TestCheckpointRestorationAndCorruption(t *testing.T) {
	store, value, current := registered(t)
	for index, step := range []struct {
		kind             executionmodel.ReportType
		cumulative, fill int64
	}{{executionmodel.ReportPlanned, 0, 0}, {executionmodel.ReportSubmissionPending, 0, 0}, {executionmodel.ReportPartialFill, 20, 20}} {
		pub := publication(t, current, step.kind, step.cumulative, step.fill, fmt.Sprintf("restore-%d", index))
		if _, err := store.PublishOrderEvent(context.Background(), pub); err != nil {
			t.Fatal(err)
		}
		current, _ = store.CurrentOrderCheckpoint(context.Background(), current.Order.ID())
	}
	reports, _ := store.Reports(context.Background(), current.Order.ID())
	fills, _ := store.Fills(context.Background(), current.Order.ID())
	restored := memory.NewStore()
	outcome, err := restored.RestorePlan(context.Background(), value.intent, value.plan, []executionstorage.OrderCheckpoint{current}, reports, fills)
	if err != nil || outcome.Status != executionstorage.RegistrationCommitted {
		t.Fatalf("restore: %v %v", outcome, err)
	}
	restoredOrder, _ := restored.Order(context.Background(), current.Order.ID())
	if !bytes.Equal(restoredOrder.CanonicalJSON(), current.Order.CanonicalJSON()) {
		t.Fatal("restored order diverged")
	}
	corrupt := current
	corrupt.CheckpointChecksum = executionmodel.StateChecksum{}
	corrupt.OrderChecksum = executionmodel.StateChecksum{}
	badFills := append([]executionmodel.Fill(nil), fills...)
	badFills = nil
	other := memory.NewStore()
	_, err = other.RestorePlan(context.Background(), value.intent, value.plan, []executionstorage.OrderCheckpoint{corrupt}, reports, badFills)
	if !errors.Is(err, executionstorage.ErrCorruptCheckpoint) {
		t.Fatalf("corrupt restore: %v", err)
	}
}

func TestRaceHeavyReadsAndCompetingWriters(t *testing.T) {
	store, value, current := registered(t)
	const readers = 24
	var wait sync.WaitGroup
	wait.Add(readers + 1)
	for range readers {
		go func() {
			defer wait.Done()
			for range 100 {
				_, _ = store.Order(context.Background(), value.orders[0].ID())
				_, _ = store.Plan(context.Background(), value.plan.ID())
				_, _ = store.Reports(context.Background(), value.orders[0].ID())
				_, _ = store.Fills(context.Background(), value.orders[0].ID())
			}
		}()
	}
	go func() {
		defer wait.Done()
		pub := publication(t, current, executionmodel.ReportPlanned, 0, 0, "race-writer")
		_, err := store.PublishOrderEvent(context.Background(), pub)
		if err != nil {
			t.Errorf("writer: %v", err)
		}
	}()
	wait.Wait()
}
