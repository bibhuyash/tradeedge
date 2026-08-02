package replay_test

import (
	"bytes"
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/adapters/broker/paper"
	executionmemory "github.com/bibhuyash/tradeedge/internal/adapters/execution/memory"
	"github.com/bibhuyash/tradeedge/internal/execution/coordinator"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	"github.com/bibhuyash/tradeedge/internal/execution/replay"
	executionstorage "github.com/bibhuyash/tradeedge/internal/execution/storage"
	executionfixture "github.com/bibhuyash/tradeedge/internal/execution/testfixture"
)

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *manualClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}
func (clock *manualClock) Set(value time.Time) {
	clock.mu.Lock()
	clock.now = value
	clock.mu.Unlock()
}

type runResult struct {
	receipts []coordinator.PlanReceipt
	order    []byte
	reports  [][]byte
	fills    [][]byte
}

func deterministicRun(t *testing.T) runResult {
	t.Helper()
	fixture, err := executionfixture.New(false)
	if err != nil {
		t.Fatal(err)
	}
	store := executionmemory.NewStore()
	if _, err = store.RegisterPlan(context.Background(), fixture.Intent, fixture.Plan, fixture.Orders); err != nil {
		t.Fatal(err)
	}
	clock := &manualClock{now: fixture.Plan.Spec().CreatedAt}
	broker, err := paper.NewScripted(clock, []paper.Scenario{{Behavior: paper.BehaviorImmediateFill}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := coordinator.New(store, broker, coordinator.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := replay.New(runner)
	if err != nil {
		t.Fatal(err)
	}
	steps := []replay.Step{{PlanID: fixture.Plan.ID(), LogicalTime: clock.Now()}}
	receipts, err := engine.Run(context.Background(), steps)
	if err != nil {
		t.Fatal(err)
	}
	order, _ := store.Order(context.Background(), fixture.Orders[0].ID())
	reports, _ := store.Reports(context.Background(), order.ID())
	fills, _ := store.Fills(context.Background(), order.ID())
	result := runResult{receipts: receipts, order: order.CanonicalJSON()}
	for _, report := range reports {
		result.reports = append(result.reports, report.CanonicalJSON())
	}
	for _, fill := range fills {
		result.fills = append(result.fills, fill.CanonicalJSON())
	}
	return result
}

func TestReplayIsDeterministic(t *testing.T) {
	first, second := deterministicRun(t), deterministicRun(t)
	if !reflect.DeepEqual(first.receipts, second.receipts) || !bytes.Equal(first.order, second.order) || !reflect.DeepEqual(first.reports, second.reports) || !reflect.DeepEqual(first.fills, second.fills) {
		t.Fatal("identical replay inputs produced different authoritative output")
	}
}

func TestCheckpointRestorationContinuesDeterministically(t *testing.T) {
	fixture, err := executionfixture.New(false)
	if err != nil {
		t.Fatal(err)
	}
	now := fixture.Plan.Spec().CreatedAt
	scenarios := []paper.Scenario{{Behavior: paper.BehaviorDelayedFill, Delay: time.Second}}
	clock := &manualClock{now: now}
	store := executionmemory.NewStore()
	_, _ = store.RegisterPlan(context.Background(), fixture.Intent, fixture.Plan, fixture.Orders)
	broker, _ := paper.NewScripted(clock, scenarios)
	runner, _ := coordinator.New(store, broker, coordinator.DefaultConfig())
	if _, err = runner.ExecutePlan(context.Background(), fixture.Plan.ID(), now); err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := store.CurrentOrderCheckpoint(context.Background(), fixture.Orders[0].ID())
	reports, _ := store.Reports(context.Background(), fixture.Orders[0].ID())
	fills, _ := store.Fills(context.Background(), fixture.Orders[0].ID())
	brokerCheckpoint := broker.Checkpoint()
	cursor := runner.Cursor()

	restoredStore := executionmemory.NewStore()
	if _, err = restoredStore.RestorePlan(context.Background(), fixture.Intent, fixture.Plan, []executionstorage.OrderCheckpoint{checkpoint}, reports, fills); err != nil {
		t.Fatal(err)
	}
	restoredBroker, err := paper.RestoreScripted(clock, scenarios, brokerCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	restoredRunner, _ := coordinator.New(restoredStore, restoredBroker, coordinator.DefaultConfig())
	restoredRunner.RestoreCursor(cursor)
	clock.Set(now.Add(time.Second))
	if _, err = restoredRunner.ResumePlan(context.Background(), fixture.Plan.ID(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	restored, _ := restoredStore.Order(context.Background(), fixture.Orders[0].ID())
	if restored.Spec().State != executionmodel.OrderFilled {
		t.Fatalf("restored state = %s", restored.Spec().State)
	}

	controlClock := &manualClock{now: now}
	controlStore := executionmemory.NewStore()
	_, _ = controlStore.RegisterPlan(context.Background(), fixture.Intent, fixture.Plan, fixture.Orders)
	controlBroker, _ := paper.NewScripted(controlClock, scenarios)
	controlRunner, _ := coordinator.New(controlStore, controlBroker, coordinator.DefaultConfig())
	_, _ = controlRunner.ExecutePlan(context.Background(), fixture.Plan.ID(), now)
	controlClock.Set(now.Add(time.Second))
	_, _ = controlRunner.ResumePlan(context.Background(), fixture.Plan.ID(), now.Add(time.Second))
	control, _ := controlStore.Order(context.Background(), fixture.Orders[0].ID())
	if !bytes.Equal(restored.CanonicalJSON(), control.CanonicalJSON()) {
		t.Fatal("checkpoint continuation diverged from uninterrupted execution")
	}
}
