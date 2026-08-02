package reconciliation_test

import (
	"context"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/adapters/broker/paper"
	executionmemory "github.com/bibhuyash/tradeedge/internal/adapters/execution/memory"
	"github.com/bibhuyash/tradeedge/internal/domain"
	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	"github.com/bibhuyash/tradeedge/internal/execution/coordinator"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	"github.com/bibhuyash/tradeedge/internal/execution/reconciliation"
	executionfixture "github.com/bibhuyash/tradeedge/internal/execution/testfixture"
)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type snapshotBroker struct {
	executionbroker.Port
	snapshot executionbroker.Snapshot
}

func (broker snapshotBroker) Snapshot(context.Context, int) (executionbroker.Snapshot, error) {
	return broker.snapshot, nil
}

func runningOrder(t *testing.T) (*executionmemory.Store, *coordinator.Coordinator, *paper.ScriptedBroker, executionfixture.Fixture, time.Time) {
	t.Helper()
	fixture, err := executionfixture.New(false)
	if err != nil {
		t.Fatal(err)
	}
	store := executionmemory.NewStore()
	_, _ = store.RegisterPlan(context.Background(), fixture.Intent, fixture.Plan, fixture.Orders)
	now := fixture.Plan.Spec().CreatedAt
	broker, _ := paper.NewScripted(fixedClock{now}, []paper.Scenario{{Behavior: paper.BehaviorHold}})
	runner, _ := coordinator.New(store, broker, coordinator.DefaultConfig())
	receipt, err := runner.ExecutePlan(context.Background(), fixture.Plan.ID(), now)
	if err != nil || receipt.Outcome != coordinator.OutcomePending {
		t.Fatalf("setup: %v %v", receipt, err)
	}
	return store, runner, broker, fixture, now
}

func TestReconciliationRepairsAuthoritativeFill(t *testing.T) {
	store, runner, broker, fixture, now := runningOrder(t)
	snapshot, _ := broker.Snapshot(context.Background(), 100)
	snapshot.Orders[0].State = executionmodel.OrderFilled
	snapshot.Orders[0].CumulativeFilled = snapshot.Orders[0].Quantity.Int64()
	snapshot.Orders[0].UpdatedAt = now.Add(time.Second)
	snapshot.Orders[0].Fills = []executionbroker.FillSnapshot{{ExecutionID: "reconciled-fill", Quantity: snapshot.Orders[0].Quantity.Int64(), CumulativeFilled: snapshot.Orders[0].Quantity.Int64(), Price: snapshot.Orders[0].LimitPrice, OccurredAt: now.Add(time.Second)}}
	adapter := snapshotBroker{broker, snapshot}
	reconciler, err := reconciliation.New(store, adapter, func(ctx context.Context, event executionbroker.Event, at time.Time) error {
		_, publishErr := runner.PublishBrokerEvent(ctx, event, at)
		return publishErr
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := reconciler.Run(context.Background(), now.Add(time.Second))
	if err != nil || receipt.Blocked || receipt.Repaired != 1 {
		t.Fatalf("reconcile: %+v %v", receipt, err)
	}
	order, _ := store.Order(context.Background(), fixture.Orders[0].ID())
	if order.Spec().State != executionmodel.OrderFilled {
		t.Fatal("broker fill did not repair OMS")
	}
	health := reconciler.Health()
	if !health.Available || health.Blocked || health.Repairs != 1 || health.LastSuccess.IsZero() {
		t.Fatalf("reconciliation health: %+v", health)
	}
}

func TestReconciliationSurfacesTermsMissingUnknownAndIncomplete(t *testing.T) {
	store, runner, broker, _, now := runningOrder(t)
	base, _ := broker.Snapshot(context.Background(), 100)
	mismatch := base
	mismatch.Orders = append([]executionbroker.OrderSnapshot(nil), base.Orders...)
	quantity, _ := domain.NewQuantity(mismatch.Orders[0].Quantity.Int64() + 1)
	mismatch.Orders[0].Quantity = quantity
	adapter := snapshotBroker{broker, mismatch}
	reconciler, _ := reconciliation.New(store, adapter, func(ctx context.Context, event executionbroker.Event, at time.Time) error {
		_, err := runner.PublishBrokerEvent(ctx, event, at)
		return err
	})
	receipt, err := reconciler.Run(context.Background(), now)
	if err != nil || !receipt.Blocked || len(receipt.Issues) != 1 || receipt.Issues[0].Kind != reconciliation.IssueTermsMismatch {
		t.Fatalf("terms: %+v %v", receipt, err)
	}
	empty := executionbroker.Snapshot{Complete: true, ObservedAt: now}
	missingRunner, _ := reconciliation.New(store, snapshotBroker{broker, empty}, func(context.Context, executionbroker.Event, time.Time) error { return nil })
	missing, _ := missingRunner.Run(context.Background(), now)
	if !missing.Blocked || missing.Issues[0].Kind != reconciliation.IssueMissingBrokerOrder {
		t.Fatalf("missing: %+v", missing)
	}
	other, _ := executionfixture.New(true)
	unknownSnapshot := base
	unknownSnapshot.Orders = append(unknownSnapshot.Orders, executionbroker.OrderSnapshot{ClientOrderID: other.Orders[1].ClientOrderID(), BrokerOrderID: "unknown", InstrumentID: other.Orders[1].Spec().Leg.InstrumentID, Side: other.Orders[1].Spec().Leg.Side, Quantity: other.Orders[1].Spec().Leg.Quantity, LimitPrice: other.Orders[1].Spec().Leg.LimitPrice, State: executionmodel.OrderAcknowledged, UpdatedAt: now})
	unknownSnapshot.Complete = false
	unknownRunner, _ := reconciliation.New(store, snapshotBroker{broker, unknownSnapshot}, func(context.Context, executionbroker.Event, time.Time) error { return nil })
	unknown, _ := unknownRunner.Run(context.Background(), now)
	if !unknown.Blocked {
		t.Fatal("unknown/incomplete snapshot did not block")
	}
}

func TestReconciliationRefusesToInventFillEvidence(t *testing.T) {
	store, runner, broker, _, now := runningOrder(t)
	snapshot, _ := broker.Snapshot(context.Background(), 100)
	snapshot.Orders[0].State = executionmodel.OrderFilled
	snapshot.Orders[0].CumulativeFilled = snapshot.Orders[0].Quantity.Int64()
	reconciler, _ := reconciliation.New(store, snapshotBroker{broker, snapshot}, func(ctx context.Context, event executionbroker.Event, at time.Time) error {
		_, err := runner.PublishBrokerEvent(ctx, event, at)
		return err
	})
	receipt, err := reconciler.Run(context.Background(), now)
	if err != nil || !receipt.Blocked || len(receipt.Issues) != 1 || receipt.Issues[0].Kind != reconciliation.IssueFillMismatch || receipt.Repaired != 0 {
		t.Fatalf("invented fill evidence: %+v %v", receipt, err)
	}
}
