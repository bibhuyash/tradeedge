package coordinator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/adapters/broker/paper"
	executionmemory "github.com/bibhuyash/tradeedge/internal/adapters/execution/memory"
	"github.com/bibhuyash/tradeedge/internal/domain"
	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	"github.com/bibhuyash/tradeedge/internal/execution/coordinator"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	executionfixture "github.com/bibhuyash/tradeedge/internal/execution/testfixture"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (value *clock) Now() time.Time { value.mu.Lock(); defer value.mu.Unlock(); return value.now }
func (value *clock) Advance(duration time.Duration) {
	value.mu.Lock()
	value.now = value.now.Add(duration)
	value.mu.Unlock()
}

func setup(t *testing.T, multi bool, scenarios []paper.Scenario) (*coordinator.Coordinator, *executionmemory.Store, *paper.ScriptedBroker, *clock, executionfixture.Fixture) {
	t.Helper()
	fixture, err := executionfixture.New(multi)
	if err != nil {
		t.Fatal(err)
	}
	store := executionmemory.NewStore()
	if _, err = store.RegisterPlan(context.Background(), fixture.Intent, fixture.Plan, fixture.Orders); err != nil {
		t.Fatal(err)
	}
	manual := &clock{now: fixture.Plan.Spec().CreatedAt}
	broker, err := paper.NewScripted(manual, scenarios)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := coordinator.New(store, broker, coordinator.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	return runner, store, broker, manual, fixture
}

func TestCoordinatorImmediateFillAndCommittedDuplicate(t *testing.T) {
	runner, store, _, clock, fixture := setup(t, false, []paper.Scenario{{Behavior: paper.BehaviorImmediateFill}})
	receipt, err := runner.ExecutePlan(context.Background(), fixture.Plan.ID(), clock.Now())
	if err != nil || receipt.Outcome != coordinator.OutcomeCompleted {
		t.Fatalf("execute: %v %v", receipt, err)
	}
	order, _ := store.Order(context.Background(), fixture.Orders[0].ID())
	if order.Spec().State != executionmodel.OrderFilled || order.Spec().FilledQuantity != order.Spec().Leg.Quantity.Int64() {
		t.Fatal("order not filled")
	}
	again, err := runner.ExecutePlan(context.Background(), fixture.Plan.ID(), clock.Now())
	if err != nil || again.Outcome != coordinator.OutcomeDuplicateCommitted {
		t.Fatalf("duplicate: %v %v", again, err)
	}
}

func TestCoordinatorPartialDelayedAndReplayContinuation(t *testing.T) {
	runner, store, _, clock, fixture := setup(t, false, []paper.Scenario{{Behavior: paper.BehaviorPartialFill, PartialQuantity: 20, Delay: time.Second}})
	receipt, err := runner.ExecutePlan(context.Background(), fixture.Plan.ID(), clock.Now())
	if err != nil || receipt.Outcome != coordinator.OutcomePending {
		t.Fatalf("partial: %v %v", receipt, err)
	}
	order, _ := store.Order(context.Background(), fixture.Orders[0].ID())
	if order.Spec().State != executionmodel.OrderPartiallyFilled || order.Spec().FilledQuantity != 20 {
		t.Fatal("partial fill missing")
	}
	clock.Advance(time.Second)
	receipt, err = runner.ResumePlan(context.Background(), fixture.Plan.ID(), clock.Now())
	if err != nil || receipt.Outcome != coordinator.OutcomeCompleted {
		t.Fatalf("resume: %v %v", receipt, err)
	}
}

func TestCoordinatorProtectiveBuyBeforeSell(t *testing.T) {
	runner, store, broker, clock, fixture := setup(t, true, []paper.Scenario{{Behavior: paper.BehaviorPartialFill, PartialQuantity: 20, Delay: time.Second}, {Behavior: paper.BehaviorImmediateFill}})
	receipt, err := runner.ExecutePlan(context.Background(), fixture.Plan.ID(), clock.Now())
	if err != nil || receipt.Outcome != coordinator.OutcomePending {
		t.Fatalf("multi-leg: %v %v", receipt, err)
	}
	snapshot, _ := broker.Snapshot(context.Background(), 100)
	if len(snapshot.Orders) != 1 {
		t.Fatalf("submitted %d legs before protective BUY filled", len(snapshot.Orders))
	}
	for _, order := range fixture.Orders {
		stored, _ := store.Order(context.Background(), order.ID())
		if order.Spec().Leg.Side == domain.SideSell && stored.Spec().State != executionmodel.OrderPlanned {
			t.Fatalf("SELL advanced before protection: %s", stored.Spec().State)
		}
	}
	clock.Advance(time.Second)
	receipt, err = runner.ResumePlan(context.Background(), fixture.Plan.ID(), clock.Now())
	if err != nil || receipt.Outcome != coordinator.OutcomeCompleted {
		t.Fatalf("multi-leg resume: %v %v", receipt, err)
	}
}

func TestCoordinatorRejectsExpiredAuthority(t *testing.T) {
	runner, store, broker, _, fixture := setup(t, false, []paper.Scenario{{Behavior: paper.BehaviorImmediateFill}})
	_, err := runner.ExecutePlan(context.Background(), fixture.Plan.ID(), fixture.Plan.Spec().ExpiresAt)
	if !errors.Is(err, executionmodel.ErrDecisionExpired) {
		t.Fatalf("expired authority: %v", err)
	}
	order, _ := store.Order(context.Background(), fixture.Orders[0].ID())
	snapshot, _ := broker.Snapshot(context.Background(), 100)
	if order.Spec().State != executionmodel.OrderCreated || len(snapshot.Orders) != 0 {
		t.Fatal("expired authority produced an execution effect")
	}
}

func TestCoordinatorUnknownOutcomeRecoveryAndFailClosed(t *testing.T) {
	runner, store, _, clock, fixture := setup(t, false, []paper.Scenario{{Behavior: paper.BehaviorLostResponse}})
	receipt, err := runner.ExecutePlan(context.Background(), fixture.Plan.ID(), clock.Now())
	if err != nil || receipt.Outcome != coordinator.OutcomePending {
		t.Fatalf("lost response recovery: %v %v", receipt, err)
	}
	reports, _ := store.Reports(context.Background(), fixture.Orders[0].ID())
	foundUnknown := false
	for _, report := range reports {
		if report.Spec().Type == executionmodel.ReportUnknown {
			foundUnknown = true
		}
	}
	if !foundUnknown {
		t.Fatal("UNKNOWN was not published")
	}
	runner2, store2, _, clock2, fixture2 := setup(t, false, []paper.Scenario{{Behavior: paper.BehaviorImmediateFill, UnavailableAttempts: 100}})
	receipt, err = runner2.ExecutePlan(context.Background(), fixture2.Plan.ID(), clock2.Now())
	if !errors.Is(err, coordinator.ErrUnknownOutcome) || receipt.Outcome != coordinator.OutcomeUnknown {
		t.Fatalf("unresolved outcome: %v %v", receipt, err)
	}
	order, _ := store2.Order(context.Background(), fixture2.Orders[0].ID())
	if order.Spec().State != executionmodel.OrderUnknown {
		t.Fatal("uncertainty did not fail closed")
	}
}

func TestCoordinatorBrokerRejection(t *testing.T) {
	runner, store, _, clock, fixture := setup(t, false, []paper.Scenario{{Behavior: paper.BehaviorReject}})
	receipt, err := runner.ExecutePlan(context.Background(), fixture.Plan.ID(), clock.Now())
	if err != nil || receipt.Outcome != coordinator.OutcomeIncomplete {
		t.Fatalf("rejection: %v %v", receipt, err)
	}
	order, _ := store.Order(context.Background(), fixture.Orders[0].ID())
	if order.Spec().State != executionmodel.OrderRejected {
		t.Fatalf("rejected state = %s", order.Spec().State)
	}
}

func TestCoordinatorDuplicateOutOfOrderCancellationLateFillAndCheckpoint(t *testing.T) {
	runner, store, broker, clock, fixture := setup(t, false, []paper.Scenario{{Behavior: paper.BehaviorDuplicateEvents}})
	receipt, err := runner.ExecutePlan(context.Background(), fixture.Plan.ID(), clock.Now())
	if err != nil || receipt.Outcome != coordinator.OutcomeCompleted {
		t.Fatalf("duplicates: %v %v", receipt, err)
	}
	fills, _ := store.Fills(context.Background(), fixture.Orders[0].ID())
	if len(fills) != 1 {
		t.Fatalf("duplicate fills=%d", len(fills))
	}
	checkpoint := broker.Checkpoint()
	cursor := runner.Cursor()
	restoredBroker, err := paper.RestoreScripted(clock, []paper.Scenario{{Behavior: paper.BehaviorDuplicateEvents}}, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := coordinator.New(store, restoredBroker, coordinator.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	restored.RestoreCursor(cursor)
	if err = restored.DrainEvents(context.Background(), clock.Now()); err != nil {
		t.Fatal(err)
	}
	cancelRunner, cancelStore, _, cancelClock, cancelFixture := setup(t, false, []paper.Scenario{{Behavior: paper.BehaviorLateFill}})
	pending, err := cancelRunner.ExecutePlan(context.Background(), cancelFixture.Plan.ID(), cancelClock.Now())
	if err != nil || pending.Outcome != coordinator.OutcomePending {
		t.Fatalf("hold: %v %v", pending, err)
	}
	cancelled, err := cancelRunner.RequestCancel(context.Background(), cancelFixture.Orders[0].ID(), cancelClock.Now())
	if err != nil || cancelled.State != executionmodel.OrderCancelled {
		t.Fatalf("cancel: %v %v", cancelled, err)
	}
	cancelClock.Advance(time.Second)
	if err = cancelRunner.DrainEvents(context.Background(), cancelClock.Now()); err != nil {
		t.Fatal(err)
	}
	late, _ := cancelStore.Order(context.Background(), cancelFixture.Orders[0].ID())
	if late.Spec().State != executionmodel.OrderFilled {
		t.Fatal("late fill did not correct cancellation")
	}
	partialRunner, partialStore, _, partialClock, partialFixture := setup(t, false, []paper.Scenario{{Behavior: paper.BehaviorPartialFill, PartialQuantity: 20, Delay: time.Second}})
	_, _ = partialRunner.ExecutePlan(context.Background(), partialFixture.Plan.ID(), partialClock.Now())
	partialCancelled, err := partialRunner.RequestCancel(context.Background(), partialFixture.Orders[0].ID(), partialClock.Now())
	if err != nil || partialCancelled.State != executionmodel.OrderCancelled {
		t.Fatalf("partial cancel: %v %v", partialCancelled, err)
	}
	partialClock.Advance(time.Second)
	if err = partialRunner.DrainEvents(context.Background(), partialClock.Now()); err != nil {
		t.Fatal(err)
	}
	partialLate, _ := partialStore.Order(context.Background(), partialFixture.Orders[0].ID())
	if partialLate.Spec().State != executionmodel.OrderFilled || partialLate.Spec().FilledQuantity != partialLate.Spec().Leg.Quantity.Int64() {
		t.Fatal("partial cancellation lost broker late fill")
	}
	outOfOrderRunner, outOfOrderStore, _, outOfOrderClock, outOfOrderFixture := setup(t, false, []paper.Scenario{{Behavior: paper.BehaviorOutOfOrder}})
	outOfOrder, err := outOfOrderRunner.ExecutePlan(context.Background(), outOfOrderFixture.Plan.ID(), outOfOrderClock.Now())
	if err != nil || outOfOrder.Outcome != coordinator.OutcomeCompleted {
		t.Fatalf("out-of-order: %v %v", outOfOrder, err)
	}
	stored, _ := outOfOrderStore.Order(context.Background(), outOfOrderFixture.Orders[0].ID())
	if stored.Spec().State != executionmodel.OrderFilled {
		t.Fatal("out-of-order acknowledgement corrupted filled state")
	}
}

type blockingBroker struct {
	executionbroker.Port
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (broker *blockingBroker) Submit(ctx context.Context, request executionbroker.Submission) (executionbroker.SubmissionResult, error) {
	broker.once.Do(func() {
		close(broker.entered)
		select {
		case <-broker.release:
		case <-ctx.Done():
		}
	})
	return broker.Port.Submit(ctx, request)
}

func TestCoordinatorRejectsInProgressDuplicate(t *testing.T) {
	fixture, _ := executionfixture.New(false)
	store := executionmemory.NewStore()
	_, _ = store.RegisterPlan(context.Background(), fixture.Intent, fixture.Plan, fixture.Orders)
	manual := &clock{now: fixture.Plan.Spec().CreatedAt}
	base, _ := paper.NewScripted(manual, []paper.Scenario{{Behavior: paper.BehaviorImmediateFill}})
	broker := &blockingBroker{Port: base, entered: make(chan struct{}), release: make(chan struct{})}
	config := coordinator.DefaultConfig()
	config.BrokerTimeout = time.Second
	runner, _ := coordinator.New(store, broker, config)
	first := make(chan error, 1)
	go func() {
		_, err := runner.ExecutePlan(context.Background(), fixture.Plan.ID(), manual.Now())
		first <- err
	}()
	<-broker.entered
	receipt, err := runner.ExecutePlan(context.Background(), fixture.Plan.ID(), manual.Now())
	if !errors.Is(err, coordinator.ErrDuplicateInProgress) || receipt.Outcome != coordinator.OutcomeDuplicateInProgress {
		t.Fatalf("in-progress duplicate: %v %v", receipt, err)
	}
	close(broker.release)
	if err = <-first; err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorBoundsCrossPlanConcurrency(t *testing.T) {
	firstFixture, _ := executionfixture.New(false)
	secondFixture, _ := executionfixture.New(true)
	store := executionmemory.NewStore()
	_, _ = store.RegisterPlan(context.Background(), firstFixture.Intent, firstFixture.Plan, firstFixture.Orders)
	_, _ = store.RegisterPlan(context.Background(), secondFixture.Intent, secondFixture.Plan, secondFixture.Orders)
	manual := &clock{now: firstFixture.Plan.Spec().CreatedAt}
	base, _ := paper.NewScripted(manual, []paper.Scenario{{Behavior: paper.BehaviorImmediateFill}, {Behavior: paper.BehaviorImmediateFill}, {Behavior: paper.BehaviorImmediateFill}})
	broker := &blockingBroker{Port: base, entered: make(chan struct{}), release: make(chan struct{})}
	config := coordinator.DefaultConfig()
	config.MaxConcurrentPlans = 1
	runner, _ := coordinator.New(store, broker, config)
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, err := runner.ExecutePlan(context.Background(), firstFixture.Plan.ID(), manual.Now())
		firstDone <- err
	}()
	<-broker.entered
	go func() {
		_, err := runner.ExecutePlan(context.Background(), secondFixture.Plan.ID(), manual.Now())
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second plan bypassed concurrency bound: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(broker.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

type panicBroker struct{ executionbroker.Port }

func (panicBroker) Submit(context.Context, executionbroker.Submission) (executionbroker.SubmissionResult, error) {
	panic("broker panic")
}

func TestCoordinatorPanicContainmentAndShutdown(t *testing.T) {
	fixture, _ := executionfixture.New(false)
	store := executionmemory.NewStore()
	_, _ = store.RegisterPlan(context.Background(), fixture.Intent, fixture.Plan, fixture.Orders)
	base, _ := paper.NewScripted(&clock{now: fixture.Plan.Spec().CreatedAt}, []paper.Scenario{{Behavior: paper.BehaviorImmediateFill}})
	runner, _ := coordinator.New(store, panicBroker{base}, coordinator.DefaultConfig())
	receipt, err := runner.ExecutePlan(context.Background(), fixture.Plan.ID(), fixture.Plan.Spec().CreatedAt)
	if !errors.Is(err, coordinator.ErrUnknownOutcome) || receipt.Outcome != coordinator.OutcomeUnknown {
		t.Fatalf("panic: %v %v", receipt, err)
	}
	if err = runner.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = runner.ExecutePlan(context.Background(), fixture.Plan.ID(), fixture.Plan.Spec().CreatedAt)
	if !errors.Is(err, coordinator.ErrShutdown) {
		t.Fatalf("shutdown: %v", err)
	}
}
