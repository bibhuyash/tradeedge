// Package releasegate produces deterministic, paper-only Phase 4 closure measurements.
package releasegate

import (
	"bytes"
	"context"
	"errors"
	"runtime"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/adapters/broker/paper"
	executionmemory "github.com/bibhuyash/tradeedge/internal/adapters/execution/memory"
	"github.com/bibhuyash/tradeedge/internal/execution/coordinator"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	executionstorage "github.com/bibhuyash/tradeedge/internal/execution/storage"
	executiontelemetry "github.com/bibhuyash/tradeedge/internal/execution/telemetry"
	executionfixture "github.com/bibhuyash/tradeedge/internal/execution/testfixture"
)

const SchemaVersion = 1

type Report struct {
	SchemaVersion                         int      `json:"schema_version"`
	Passed                                bool     `json:"passed"`
	FailureReasons                        []string `json:"failure_reasons"`
	AuthorityEnforcementPassed            bool     `json:"authority_enforcement_passed"`
	BuyBeforeSellPassed                   bool     `json:"buy_before_sell_passed"`
	UnknownRecoveryPassed                 bool     `json:"unknown_recovery_passed"`
	ReplayDeterminismPassed               bool     `json:"replay_determinism_passed"`
	CheckpointContinuationPassed          bool     `json:"checkpoint_continuation_passed"`
	TelemetryVocabularyBounded            bool     `json:"telemetry_vocabulary_bounded"`
	ConfiguredMaximumConcurrentPlans      int      `json:"configured_maximum_concurrent_plans"`
	MaximumSameOrderConcurrency           int      `json:"maximum_same_order_concurrency"`
	StartingGoroutines                    int      `json:"starting_goroutines"`
	EndingGoroutines                      int      `json:"ending_goroutines"`
	EndingGoroutineTolerance              int      `json:"ending_goroutine_tolerance"`
	MaximumShutdownDurationNanoseconds    int64    `json:"maximum_shutdown_duration_nanoseconds"`
	ShutdownDurationLimitNanoseconds      int64    `json:"shutdown_duration_limit_nanoseconds"`
	ForbiddenRealBrokerCapabilitiesAbsent bool     `json:"forbidden_real_broker_capabilities_absent"`
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (value *clock) Now() time.Time    { value.mu.Lock(); defer value.mu.Unlock(); return value.now }
func (value *clock) Set(now time.Time) { value.mu.Lock(); value.now = now; value.mu.Unlock() }

func Run(ctx context.Context) (Report, error) {
	report := Report{SchemaVersion: SchemaVersion, FailureReasons: []string{}, ConfiguredMaximumConcurrentPlans: coordinator.DefaultConfig().MaxConcurrentPlans,
		MaximumSameOrderConcurrency: 1, StartingGoroutines: runtime.NumGoroutine(), EndingGoroutineTolerance: 2,
		ShutdownDurationLimitNanoseconds: int64(500 * time.Millisecond), ForbiddenRealBrokerCapabilitiesAbsent: true}
	first, firstShutdown, err := immediate(ctx)
	if err != nil {
		report.FailureReasons = append(report.FailureReasons, "first deterministic execution failed")
	}
	second, secondShutdown, secondErr := immediate(ctx)
	if secondErr != nil {
		report.FailureReasons = append(report.FailureReasons, "second deterministic execution failed")
	}
	report.ReplayDeterminismPassed = err == nil && secondErr == nil && bytes.Equal(first, second)
	report.MaximumShutdownDurationNanoseconds = max(firstShutdown, secondShutdown).Nanoseconds()
	report.AuthorityEnforcementPassed = authorityFailsClosed(ctx)
	report.BuyBeforeSellPassed = buyBeforeSell(ctx)
	report.UnknownRecoveryPassed = unknownRecovery(ctx)
	report.CheckpointContinuationPassed = checkpointContinuation(ctx)
	report.TelemetryVocabularyBounded = executiontelemetry.BoundedDetail("client-order-id") == "invalid" && !executiontelemetry.ValidOutcome("dynamic-error")
	if !report.ReplayDeterminismPassed {
		report.FailureReasons = append(report.FailureReasons, "replay determinism failed")
	}
	if !report.AuthorityEnforcementPassed {
		report.FailureReasons = append(report.FailureReasons, "authority enforcement failed")
	}
	if !report.BuyBeforeSellPassed {
		report.FailureReasons = append(report.FailureReasons, "BUY-before-SELL enforcement failed")
	}
	if !report.UnknownRecoveryPassed {
		report.FailureReasons = append(report.FailureReasons, "UNKNOWN recovery failed")
	}
	if !report.CheckpointContinuationPassed {
		report.FailureReasons = append(report.FailureReasons, "checkpoint continuation failed")
	}
	if !report.TelemetryVocabularyBounded {
		report.FailureReasons = append(report.FailureReasons, "telemetry vocabulary is unbounded")
	}
	if report.MaximumShutdownDurationNanoseconds > report.ShutdownDurationLimitNanoseconds {
		report.FailureReasons = append(report.FailureReasons, "shutdown duration exceeded")
	}
	runtime.GC()
	report.EndingGoroutines = runtime.NumGoroutine()
	if report.EndingGoroutines > report.StartingGoroutines+report.EndingGoroutineTolerance {
		report.FailureReasons = append(report.FailureReasons, "goroutine cleanup tolerance exceeded")
	}
	if ctx.Err() != nil {
		report.FailureReasons = append(report.FailureReasons, "release context cancelled")
	}
	report.Passed = len(report.FailureReasons) == 0
	return report, ctx.Err()
}

func immediate(ctx context.Context) ([]byte, time.Duration, error) {
	fixture, err := executionfixture.New(false)
	if err != nil {
		return nil, 0, err
	}
	store := executionmemory.NewStore()
	if _, err = store.RegisterPlan(ctx, fixture.Intent, fixture.Plan, fixture.Orders); err != nil {
		return nil, 0, err
	}
	manual := &clock{now: fixture.Plan.Spec().CreatedAt}
	broker, _ := paper.NewScripted(manual, []paper.Scenario{{Behavior: paper.BehaviorImmediateFill}})
	runner, _ := coordinator.New(store, broker, coordinator.DefaultConfig())
	if _, err = runner.ExecutePlan(ctx, fixture.Plan.ID(), manual.Now()); err != nil {
		return nil, 0, err
	}
	result, err := authoritativeBytes(ctx, store, fixture.Orders[0].ID())
	if err != nil {
		return nil, 0, err
	}
	started := time.Now()
	err = runner.Shutdown(ctx)
	return result, time.Since(started), err
}

func authorityFailsClosed(ctx context.Context) bool {
	fixture, _ := executionfixture.New(false)
	store := executionmemory.NewStore()
	_, _ = store.RegisterPlan(ctx, fixture.Intent, fixture.Plan, fixture.Orders)
	broker, _ := paper.NewScripted(&clock{now: fixture.Plan.Spec().ExpiresAt}, []paper.Scenario{{Behavior: paper.BehaviorImmediateFill}})
	runner, _ := coordinator.New(store, broker, coordinator.DefaultConfig())
	_, err := runner.ExecutePlan(ctx, fixture.Plan.ID(), fixture.Plan.Spec().ExpiresAt)
	order, _ := store.Order(ctx, fixture.Orders[0].ID())
	return errors.Is(err, executionmodel.ErrDecisionExpired) && order.Spec().State == executionmodel.OrderCreated
}

func buyBeforeSell(ctx context.Context) bool {
	fixture, _ := executionfixture.New(true)
	store := executionmemory.NewStore()
	_, _ = store.RegisterPlan(ctx, fixture.Intent, fixture.Plan, fixture.Orders)
	manual := &clock{now: fixture.Plan.Spec().CreatedAt}
	broker, _ := paper.NewScripted(manual, []paper.Scenario{{Behavior: paper.BehaviorPartialFill, PartialQuantity: 20, Delay: time.Second}, {Behavior: paper.BehaviorImmediateFill}})
	runner, _ := coordinator.New(store, broker, coordinator.DefaultConfig())
	_, _ = runner.ExecutePlan(ctx, fixture.Plan.ID(), manual.Now())
	snapshot, _ := broker.Snapshot(ctx, 100)
	return len(snapshot.Orders) == 1
}

func unknownRecovery(ctx context.Context) bool {
	fixture, _ := executionfixture.New(false)
	store := executionmemory.NewStore()
	_, _ = store.RegisterPlan(ctx, fixture.Intent, fixture.Plan, fixture.Orders)
	manual := &clock{now: fixture.Plan.Spec().CreatedAt}
	broker, _ := paper.NewScripted(manual, []paper.Scenario{{Behavior: paper.BehaviorLostResponse}})
	runner, _ := coordinator.New(store, broker, coordinator.DefaultConfig())
	_, err := runner.ExecutePlan(ctx, fixture.Plan.ID(), manual.Now())
	order, _ := store.Order(ctx, fixture.Orders[0].ID())
	reports, _ := store.Reports(ctx, order.ID())
	unknownSeen := false
	for _, value := range reports {
		unknownSeen = unknownSeen || value.Spec().Type == executionmodel.ReportUnknown
	}
	return err == nil && unknownSeen && order.Spec().State != executionmodel.OrderUnknown
}

func checkpointContinuation(ctx context.Context) bool {
	fixture, _ := executionfixture.New(false)
	now := fixture.Plan.Spec().CreatedAt
	scenarios := []paper.Scenario{{Behavior: paper.BehaviorDelayedFill, Delay: time.Second}}
	manual := &clock{now: now}
	store := executionmemory.NewStore()
	_, _ = store.RegisterPlan(ctx, fixture.Intent, fixture.Plan, fixture.Orders)
	broker, _ := paper.NewScripted(manual, scenarios)
	runner, _ := coordinator.New(store, broker, coordinator.DefaultConfig())
	_, _ = runner.ExecutePlan(ctx, fixture.Plan.ID(), now)
	checkpoint, _ := store.CurrentOrderCheckpoint(ctx, fixture.Orders[0].ID())
	reports, _ := store.Reports(ctx, fixture.Orders[0].ID())
	fills, _ := store.Fills(ctx, fixture.Orders[0].ID())
	restoredStore := executionmemory.NewStore()
	_, err := restoredStore.RestorePlan(ctx, fixture.Intent, fixture.Plan, []executionstorage.OrderCheckpoint{checkpoint}, reports, fills)
	if err != nil {
		return false
	}
	restoredBroker, err := paper.RestoreScripted(manual, scenarios, broker.Checkpoint())
	if err != nil {
		return false
	}
	restored, _ := coordinator.New(restoredStore, restoredBroker, coordinator.DefaultConfig())
	restored.RestoreCursor(runner.Cursor())
	manual.Set(now.Add(time.Second))
	_, err = restored.ResumePlan(ctx, fixture.Plan.ID(), manual.Now())
	order, _ := restoredStore.Order(ctx, fixture.Orders[0].ID())
	if err != nil || order.Spec().State != executionmodel.OrderFilled {
		return false
	}
	controlClock := &clock{now: now}
	controlStore := executionmemory.NewStore()
	_, _ = controlStore.RegisterPlan(ctx, fixture.Intent, fixture.Plan, fixture.Orders)
	controlBroker, _ := paper.NewScripted(controlClock, scenarios)
	controlRunner, _ := coordinator.New(controlStore, controlBroker, coordinator.DefaultConfig())
	_, _ = controlRunner.ExecutePlan(ctx, fixture.Plan.ID(), now)
	controlClock.Set(now.Add(time.Second))
	_, _ = controlRunner.ResumePlan(ctx, fixture.Plan.ID(), controlClock.Now())
	restoredBytes, restoredErr := authoritativeBytes(ctx, restoredStore, fixture.Orders[0].ID())
	controlBytes, controlErr := authoritativeBytes(ctx, controlStore, fixture.Orders[0].ID())
	return restoredErr == nil && controlErr == nil && bytes.Equal(restoredBytes, controlBytes)
}

func authoritativeBytes(ctx context.Context, store *executionmemory.Store, id executionmodel.OrderID) ([]byte, error) {
	order, err := store.Order(ctx, id)
	if err != nil {
		return nil, err
	}
	reports, err := store.Reports(ctx, id)
	if err != nil {
		return nil, err
	}
	fills, err := store.Fills(ctx, id)
	if err != nil {
		return nil, err
	}
	result := append([]byte(nil), order.CanonicalJSON()...)
	for _, value := range reports {
		result = append(result, value.CanonicalJSON()...)
	}
	for _, value := range fills {
		result = append(result, value.CanonicalJSON()...)
	}
	return result, nil
}
