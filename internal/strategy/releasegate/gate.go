// Package releasegate provides the deterministic, paper-safe Phase 2 closure
// harness. It exercises strategy runtime guarantees without broker, risk,
// allocation, order, position, credential, or live-connectivity capability.
package releasegate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	strategymemory "github.com/bibhuyash/tradeedge/internal/adapters/strategy/memory"
	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
	strategyreplay "github.com/bibhuyash/tradeedge/internal/strategy/replay"
	"github.com/bibhuyash/tradeedge/internal/strategy/runner"
	strategystorage "github.com/bibhuyash/tradeedge/internal/strategy/storage"
	strategytelemetry "github.com/bibhuyash/tradeedge/internal/strategy/telemetry"
)

const (
	SchemaVersion             = 1
	ConfiguredConcurrency     = 2
	EndingGoroutineTolerance  = 2
	PeakGoroutineGrowthLimit  = 16
	EndingHeapGrowthLimit     = 16 * 1024 * 1024
	PeakHeapGrowthLimit       = 32 * 1024 * 1024
	CancellationDurationLimit = 500 * time.Millisecond
)

type Report struct {
	SchemaVersion                           int      `json:"schema_version"`
	Passed                                  bool     `json:"passed"`
	FailureReasons                          []string `json:"failure_reasons"`
	TotalEvaluations                        uint64   `json:"total_evaluations"`
	CommittedNoActionCount                  uint64   `json:"committed_no_action_count"`
	CommittedObservationCount               uint64   `json:"committed_observation_count"`
	CommittedProposalCount                  uint64   `json:"committed_proposal_count"`
	ReadinessBlockedCount                   uint64   `json:"readiness_blocked_count"`
	DuplicateCommittedCount                 uint64   `json:"duplicate_committed_count"`
	DuplicateInProgressCount                uint64   `json:"duplicate_in_progress_count"`
	InstanceBusyCount                       uint64   `json:"instance_busy_count"`
	TimeoutCount                            uint64   `json:"timeout_count"`
	PanicCount                              uint64   `json:"panic_count"`
	CancellationCount                       uint64   `json:"cancellation_count"`
	ShutdownCount                           uint64   `json:"shutdown_count"`
	InvalidOutputCount                      uint64   `json:"invalid_output_count"`
	RevisionConflictCount                   uint64   `json:"revision_conflict_count"`
	PublicationFailureCount                 uint64   `json:"publication_failure_count"`
	UnexpectedResultLoss                    uint64   `json:"unexpected_result_loss"`
	UnexpectedDuplicatePublication          uint64   `json:"unexpected_duplicate_publication"`
	MaximumConcurrentEvaluations            int      `json:"maximum_concurrent_evaluations"`
	ConfiguredMaximumConcurrentEvaluations  int      `json:"configured_maximum_concurrent_evaluations"`
	MaximumSameInstanceConcurrency          int      `json:"maximum_same_instance_concurrency"`
	ReadinessBlockedStrategyInvocations     uint64   `json:"readiness_blocked_strategy_invocations"`
	ReplayDeterminismPassed                 bool     `json:"replay_determinism_passed"`
	CheckpointContinuationEquivalencePassed bool     `json:"checkpoint_continuation_equivalence_passed"`
	StartingGoroutineCount                  int      `json:"starting_goroutine_count"`
	PeakGoroutineCount                      int      `json:"peak_goroutine_count"`
	EndingGoroutineCount                    int      `json:"ending_goroutine_count"`
	EndingGoroutineTolerance                int      `json:"ending_goroutine_tolerance"`
	PeakGoroutineGrowthLimit                int      `json:"peak_goroutine_growth_limit"`
	StartingHeapAllocationBytes             uint64   `json:"starting_heap_allocation_bytes"`
	PeakHeapAllocationBytes                 uint64   `json:"peak_heap_allocation_bytes"`
	EndingHeapAllocationBytes               uint64   `json:"ending_heap_allocation_bytes"`
	EndingHeapGrowthLimitBytes              uint64   `json:"ending_heap_growth_limit_bytes"`
	PeakHeapGrowthLimitBytes                uint64   `json:"peak_heap_growth_limit_bytes"`
	GarbageCollectionCycles                 uint32   `json:"garbage_collection_cycles"`
	MaximumCancellationShutdownNanoseconds  int64    `json:"maximum_cancellation_shutdown_duration_nanoseconds"`
	CancellationShutdownLimitNanoseconds    int64    `json:"cancellation_shutdown_limit_nanoseconds"`
}

type behavior int32

const (
	behaviorAutomatic behavior = iota
	behaviorBlock
	behaviorPanic
	behaviorTimeout
	behaviorInvalid
	behaviorNoAction
)

type definition struct {
	descriptor strategymodel.Descriptor
	mode       atomic.Int32
	calls      atomic.Uint64

	mu             sync.Mutex
	active         int
	maximumActive  int
	instanceActive map[domain.StrategyID]int
	maximumSame    int
	entered        chan struct{}
	release        chan struct{}
}

func newDefinition(descriptor strategymodel.Descriptor) *definition {
	return &definition{
		descriptor: descriptor, instanceActive: make(map[domain.StrategyID]int),
	}
}

func (value *definition) Descriptor() strategymodel.Descriptor {
	return value.descriptor
}

func (*definition) ValidateConfiguration(strategymodel.StrategyConfiguration) error {
	return nil
}

func (*definition) InitialState(
	strategymodel.StrategyConfiguration,
) (strategymodel.StrategyRuntimeState, error) {
	return newState(0)
}

func (value *definition) Evaluate(
	ctx context.Context,
	input strategymodel.EvaluationContext,
) (result strategymodel.EvaluationResult, err error) {
	value.calls.Add(1)
	value.enter(input.InstanceID)
	defer value.leave(input.InstanceID)
	switch behavior(value.mode.Load()) {
	case behaviorBlock:
		value.signalEntered()
		select {
		case <-ctx.Done():
			return strategymodel.EvaluationResult{}, ctx.Err()
		case <-value.release:
			return noAction(input)
		}
	case behaviorPanic:
		panic("phase-2-release-gate-panic")
	case behaviorTimeout:
		<-ctx.Done()
		return strategymodel.EvaluationResult{}, ctx.Err()
	case behaviorInvalid:
		return strategymodel.EvaluationResult{}, nil
	case behaviorNoAction:
		return noAction(input)
	default:
		return automaticResult(input)
	}
}

func (value *definition) setBehavior(mode behavior) {
	value.mode.Store(int32(mode))
}

func (value *definition) startBlocking(capacity int) {
	value.mu.Lock()
	defer value.mu.Unlock()
	value.entered = make(chan struct{}, capacity)
	value.release = make(chan struct{})
	value.mode.Store(int32(behaviorBlock))
}

func (value *definition) stopBlocking() {
	value.mu.Lock()
	release := value.release
	value.mu.Unlock()
	select {
	case <-release:
	default:
		close(release)
	}
}

func (value *definition) waitEntered(ctx context.Context, count int) error {
	for index := 0; index < count; index++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-value.entered:
		}
	}
	return nil
}

func (value *definition) signalEntered() {
	value.mu.Lock()
	entered := value.entered
	value.mu.Unlock()
	entered <- struct{}{}
}

func (value *definition) enter(instanceID domain.StrategyID) {
	value.mu.Lock()
	defer value.mu.Unlock()
	value.active++
	value.instanceActive[instanceID]++
	if value.active > value.maximumActive {
		value.maximumActive = value.active
	}
	if value.instanceActive[instanceID] > value.maximumSame {
		value.maximumSame = value.instanceActive[instanceID]
	}
}

func (value *definition) leave(instanceID domain.StrategyID) {
	value.mu.Lock()
	defer value.mu.Unlock()
	value.active--
	value.instanceActive[instanceID]--
}

func (value *definition) concurrency() (int, int) {
	value.mu.Lock()
	defer value.mu.Unlock()
	return value.maximumActive, value.maximumSame
}

type readinessGate struct{ ready atomic.Bool }

func newReadinessGate(ready bool) *readinessGate {
	value := &readinessGate{}
	value.ready.Store(ready)
	return value
}

func (value *readinessGate) Evidence(
	context.Context,
	strategymodel.StrategyInstance,
	strategymodel.CandleFrame,
) strategymodel.ReadinessEvidence {
	state := readiness.StateStale
	reasons := []readiness.ReasonCode{readiness.ReasonExchangeTimeStale}
	if value.ready.Load() {
		state = readiness.StateReady
		reasons = []readiness.ReasonCode{readiness.ReasonNone}
	}
	return strategymodel.ReadinessEvidence{
		State: state, Reasons: reasons, PolicyVersion: "release-gate-policy/v1",
		CalendarVersion: "release-gate-calendar/v1", EvaluatedAt: fixedTime(),
	}
}

type environment struct {
	store      *strategymemory.Store
	registry   *runner.Registry
	definition *definition
	gate       *readinessGate
	runner     *runner.Runner
	descriptor strategymodel.Descriptor
	instrument domain.InstrumentID
	telemetry  *strategytelemetry.MemoryRecorder
	instances  map[string]strategymodel.StrategyInstance
}

func newEnvironment(
	name string,
	recorder *strategytelemetry.MemoryRecorder,
	maximum int,
	timeout time.Duration,
) (*environment, error) {
	instrument, err := domain.InstrumentIDFromCanonicalKey("NSE|INDEX|release-gate|" + name)
	if err != nil {
		return nil, err
	}
	descriptor, err := newDescriptor(instrument)
	if err != nil {
		return nil, err
	}
	store := strategymemory.NewStore()
	record, err := strategystorage.NewDefinitionRecord(
		descriptor.Manifest.DefinitionID, "definition/v1",
		"Phase 2 release gate", "non-production deterministic release fixture",
	)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if _, err := store.RegisterDefinition(ctx, record); err != nil {
		return nil, err
	}
	if _, err := store.RegisterVersion(ctx, descriptor); err != nil {
		return nil, err
	}
	definition := newDefinition(descriptor)
	registry := runner.NewRegistry()
	if err := registry.Register(definition); err != nil {
		return nil, err
	}
	gate := newReadinessGate(true)
	value, err := runner.New(runner.Config{
		MaxConcurrency: maximum, Timeout: timeout,
	}, registry, store, gate, runner.RealClock{}, recorder)
	if err != nil {
		return nil, err
	}
	return &environment{
		store: store, registry: registry, definition: definition, gate: gate,
		runner: value, descriptor: descriptor, instrument: instrument,
		telemetry: recorder, instances: make(map[string]strategymodel.StrategyInstance),
	}, nil
}

func (value *environment) addInstance(name string) (strategymodel.StrategyInstance, error) {
	configuration, err := strategymodel.NewStrategyConfiguration(
		"release-gate-config/v1", []byte(`{"enabled":true}`),
	)
	if err != nil {
		return strategymodel.StrategyInstance{}, err
	}
	instanceID, err := domain.NewStrategyID("release-gate-" + name)
	if err != nil {
		return strategymodel.StrategyInstance{}, err
	}
	instance, err := strategymodel.NewStrategyInstance(
		instanceID, value.descriptor, configuration, 1, strategymodel.LifecycleCandidate,
	)
	if err != nil {
		return strategymodel.StrategyInstance{}, err
	}
	ctx := context.Background()
	if _, err := value.store.PutInstance(
		ctx, strategymodel.InstanceRevisionID{}, instance,
	); err != nil {
		return strategymodel.StrategyInstance{}, err
	}
	state, err := newState(0)
	if err != nil {
		return strategymodel.StrategyInstance{}, err
	}
	root, err := strategystorage.NewRuntimeCheckpoint(strategystorage.RuntimeCheckpointSpec{
		InstanceID: instance.ID(), DefinitionID: instance.DefinitionID(),
		VersionID: instance.VersionID(), InstanceRevisionID: instance.RevisionID(),
		ConfigurationHash: instance.Configuration().Hash(), State: state,
	})
	if err != nil {
		return strategymodel.StrategyInstance{}, err
	}
	if _, err := value.store.InitializeCheckpoint(ctx, root); err != nil {
		return strategymodel.StrategyInstance{}, err
	}
	value.instances[name] = instance
	return instance, nil
}

type conflictRepository struct{ *strategymemory.Store }

func (repository conflictRepository) PublishEvaluation(
	context.Context,
	strategystorage.EvaluationPublication,
) (strategystorage.PublicationOutcome, error) {
	return strategystorage.PublicationOutcome{
			Status: strategystorage.PublicationRevisionConflict,
		}, &strategystorage.RevisionConflictError{
			InstanceID: "release-gate-conflict", Expected: 0, Actual: 1,
		}
}

type gateRun struct {
	report            Report
	recorder          *strategytelemetry.MemoryRecorder
	definitions       []*definition
	environments      []*environment
	expectedCommitted uint64
	actualCommitted   uint64
	peakGoroutines    int
	peakHeap          uint64
	startingGCCount   uint32
}

func Run(ctx context.Context) (Report, error) {
	runtime.GC()
	run := &gateRun{
		report: Report{
			SchemaVersion:                          SchemaVersion,
			FailureReasons:                         []string{},
			ConfiguredMaximumConcurrentEvaluations: ConfiguredConcurrency,
			EndingGoroutineTolerance:               EndingGoroutineTolerance,
			PeakGoroutineGrowthLimit:               PeakGoroutineGrowthLimit,
			EndingHeapGrowthLimitBytes:             EndingHeapGrowthLimit,
			PeakHeapGrowthLimitBytes:               PeakHeapGrowthLimit,
			CancellationShutdownLimitNanoseconds:   CancellationDurationLimit.Nanoseconds(),
		},
		recorder: strategytelemetry.NewMemoryRecorder(),
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	run.report.StartingGoroutineCount = runtime.NumGoroutine()
	run.report.StartingHeapAllocationBytes = memory.HeapAlloc
	run.startingGCCount = memory.NumGC
	run.peakGoroutines = run.report.StartingGoroutineCount
	run.peakHeap = memory.HeapAlloc
	run.sampleResources()

	if err := run.successAndDuplicates(ctx); err != nil {
		run.fail(err)
	}
	if err := run.readinessBlock(ctx); err != nil {
		run.fail(err)
	}
	if err := run.sameInstanceSerialization(ctx); err != nil {
		run.fail(err)
	}
	if err := run.crossInstanceConcurrency(ctx); err != nil {
		run.fail(err)
	}
	for _, scenario := range []struct {
		name string
		mode behavior
	}{
		{"panic", behaviorPanic},
		{"timeout", behaviorTimeout},
		{"invalid", behaviorInvalid},
	} {
		if err := run.failedEvaluation(ctx, scenario.name, scenario.mode); err != nil {
			run.fail(err)
		}
	}
	if err := run.cancelledEvaluation(); err != nil {
		run.fail(err)
	}
	if err := run.revisionConflict(ctx); err != nil {
		run.fail(err)
	}
	if err := run.shutdownContainment(ctx); err != nil {
		run.fail(err)
	}
	if err := run.replayEquivalence(ctx); err != nil {
		run.fail(err)
	}

	runtime.GC()
	time.Sleep(25 * time.Millisecond)
	runtime.ReadMemStats(&memory)
	run.report.EndingGoroutineCount = runtime.NumGoroutine()
	run.report.EndingHeapAllocationBytes = memory.HeapAlloc
	run.report.GarbageCollectionCycles = memory.NumGC - run.startingGCCount
	run.sampleResources()
	run.report.PeakGoroutineCount = run.peakGoroutines
	run.report.PeakHeapAllocationBytes = run.peakHeap
	snapshot := run.recorder.Snapshot()
	run.report.TotalEvaluations = snapshot.Counts[string(runner.OutcomeStarted)]
	run.report.CommittedNoActionCount = snapshot.Counts[string(runner.OutcomeNoAction)]
	run.report.CommittedObservationCount = snapshot.Counts[string(runner.OutcomeObservation)]
	run.report.CommittedProposalCount = snapshot.Counts[string(runner.OutcomeTradeProposal)]
	run.report.ReadinessBlockedCount = snapshot.Counts[string(runner.OutcomeReadinessBlocked)]
	run.report.DuplicateCommittedCount = snapshot.Counts[string(runner.OutcomeDuplicateCommitted)]
	run.report.DuplicateInProgressCount = snapshot.Counts[string(runner.OutcomeDuplicateInProgress)]
	run.report.InstanceBusyCount = snapshot.Counts[string(runner.OutcomeInstanceBusy)]
	run.report.TimeoutCount = snapshot.Counts[string(runner.OutcomeTimedOut)]
	run.report.PanicCount = snapshot.Counts[string(runner.OutcomePanic)]
	run.report.CancellationCount = snapshot.Counts[string(runner.OutcomeCancelled)]
	run.report.ShutdownCount = snapshot.Counts[string(runner.OutcomeShutdown)]
	run.report.InvalidOutputCount = snapshot.Counts[string(runner.OutcomeInvalid)]
	run.report.RevisionConflictCount = snapshot.Counts[string(runner.OutcomeRevisionConflict)]
	run.report.PublicationFailureCount = snapshot.Counts[string(runner.OutcomePublicationFailure)]
	run.report.MaximumConcurrentEvaluations = snapshot.MaxInFlight
	published, duplicatePublications, publicationErr := run.countPublications(ctx)
	if publicationErr != nil {
		run.fail(publicationErr)
	}
	run.actualCommitted = published
	run.report.UnexpectedDuplicatePublication = duplicatePublications

	for _, definition := range run.definitions {
		maximum, same := definition.concurrency()
		if maximum > run.report.MaximumConcurrentEvaluations {
			run.report.MaximumConcurrentEvaluations = maximum
		}
		if same > run.report.MaximumSameInstanceConcurrency {
			run.report.MaximumSameInstanceConcurrency = same
		}
	}
	if run.actualCommitted < run.expectedCommitted {
		run.report.UnexpectedResultLoss = run.expectedCommitted - run.actualCommitted
	} else if run.actualCommitted > run.expectedCommitted {
		run.report.UnexpectedDuplicatePublication += run.actualCommitted - run.expectedCommitted
	}
	run.enforce()
	run.report.Passed = len(run.report.FailureReasons) == 0
	if !run.report.Passed {
		return run.report, errors.New("phase 2 release gate failed")
	}
	return run.report, nil
}

func (run *gateRun) successAndDuplicates(ctx context.Context) error {
	env, err := newEnvironment("success", run.recorder, ConfiguredConcurrency, 100*time.Millisecond)
	if err != nil {
		return err
	}
	run.track(env)
	tests := []struct {
		name    string
		price   int64
		outcome runner.Outcome
	}{
		{"no-action", 100, runner.OutcomeNoAction},
		{"observation", 200, runner.OutcomeObservation},
		{"proposal", 300, runner.OutcomeTradeProposal},
	}
	for index, test := range tests {
		instance, createErr := env.addInstance("success-" + test.name)
		if createErr != nil {
			return createErr
		}
		frame, createErr := env.frame(index, test.price)
		if createErr != nil {
			return createErr
		}
		receipt, evaluateErr := env.runner.EvaluateFrame(ctx, instance.ID(), frame)
		if evaluateErr != nil || receipt.Outcome != test.outcome {
			return fmt.Errorf("%s outcome %s: %w", test.name, receipt.Outcome, evaluateErr)
		}
		run.expectedCommitted++
		if index == 0 {
			before, _ := env.store.Evaluations(ctx, instance.ID())
			duplicate, duplicateErr := env.runner.EvaluateFrame(ctx, instance.ID(), frame)
			after, _ := env.store.Evaluations(ctx, instance.ID())
			if !errors.Is(duplicateErr, runner.ErrDuplicateTrigger) ||
				duplicate.Outcome != runner.OutcomeDuplicateCommitted ||
				len(before) != 1 || len(after) != 1 {
				return errors.New("committed duplicate was not suppressed")
			}
		}
	}
	return nil
}

func (run *gateRun) readinessBlock(ctx context.Context) error {
	env, err := newEnvironment("readiness", run.recorder, 1, 100*time.Millisecond)
	if err != nil {
		return err
	}
	run.track(env)
	instance, err := env.addInstance("readiness")
	if err != nil {
		return err
	}
	frame, err := env.frame(0, 100)
	if err != nil {
		return err
	}
	env.gate.ready.Store(false)
	before := env.definition.calls.Load()
	receipt, evaluateErr := env.runner.EvaluateFrame(ctx, instance.ID(), frame)
	after := env.definition.calls.Load()
	if !errors.Is(evaluateErr, runner.ErrReadinessBlocked) ||
		receipt.Outcome != runner.OutcomeReadinessBlocked || before != after {
		run.report.ReadinessBlockedStrategyInvocations += after - before
		return errors.New("readiness block invoked strategy or returned wrong outcome")
	}
	return nil
}

func (run *gateRun) sameInstanceSerialization(ctx context.Context) error {
	env, err := newEnvironment("same-instance", run.recorder, 2, time.Second)
	if err != nil {
		return err
	}
	run.track(env)
	instance, err := env.addInstance("same-instance")
	if err != nil {
		return err
	}
	frame, err := env.frame(0, 100)
	if err != nil {
		return err
	}
	env.definition.startBlocking(1)
	first := make(chan runner.Receipt, 1)
	firstErr := make(chan error, 1)
	go func() {
		receipt, evaluateErr := env.runner.EvaluateFrame(ctx, instance.ID(), frame)
		first <- receipt
		firstErr <- evaluateErr
	}()
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := env.definition.waitEntered(waitCtx, 1); err != nil {
		return err
	}
	run.sampleResources()
	duplicate, duplicateErr := env.runner.EvaluateFrame(ctx, instance.ID(), frame)
	if !errors.Is(duplicateErr, runner.ErrDuplicateTrigger) ||
		duplicate.Outcome != runner.OutcomeDuplicateInProgress {
		return errors.New("in-progress duplicate was not suppressed")
	}
	other, err := env.frame(1, 101)
	if err != nil {
		return err
	}
	busy, busyErr := env.runner.EvaluateFrame(ctx, instance.ID(), other)
	if !errors.Is(busyErr, runner.ErrInstanceBusy) ||
		busy.Outcome != runner.OutcomeInstanceBusy {
		return errors.New("same-instance different trigger was not serialized")
	}
	env.definition.stopBlocking()
	receipt := <-first
	if err := <-firstErr; err != nil || receipt.Outcome != runner.OutcomeNoAction {
		return fmt.Errorf("serialized evaluation failed: %w", err)
	}
	run.expectedCommitted++
	return nil
}

func (run *gateRun) crossInstanceConcurrency(ctx context.Context) error {
	env, err := newEnvironment("cross-instance", run.recorder, ConfiguredConcurrency, time.Second)
	if err != nil {
		return err
	}
	run.track(env)
	env.definition.startBlocking(3)
	instances := make([]strategymodel.StrategyInstance, 3)
	frames := make([]strategymodel.CandleFrame, 3)
	for index := range instances {
		instances[index], err = env.addInstance(fmt.Sprintf("cross-%d", index))
		if err != nil {
			return err
		}
		frames[index], err = env.frame(index, int64(100+index))
		if err != nil {
			return err
		}
	}
	results := make(chan error, len(instances))
	for index := range instances {
		go func(index int) {
			_, evaluateErr := env.runner.EvaluateFrame(ctx, instances[index].ID(), frames[index])
			results <- evaluateErr
		}(index)
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := env.definition.waitEntered(waitCtx, ConfiguredConcurrency); err != nil {
		return err
	}
	run.sampleResources()
	time.Sleep(10 * time.Millisecond)
	maximum, _ := env.definition.concurrency()
	if maximum > ConfiguredConcurrency {
		return fmt.Errorf("cross-instance concurrency %d exceeds %d", maximum, ConfiguredConcurrency)
	}
	env.definition.stopBlocking()
	for range instances {
		if err := <-results; err != nil {
			return err
		}
		run.expectedCommitted++
	}
	return nil
}

func (run *gateRun) failedEvaluation(
	ctx context.Context,
	name string,
	mode behavior,
) error {
	timeout := 100 * time.Millisecond
	if mode == behaviorTimeout {
		timeout = 10 * time.Millisecond
	}
	env, err := newEnvironment(name, run.recorder, 1, timeout)
	if err != nil {
		return err
	}
	run.track(env)
	instance, err := env.addInstance(name)
	if err != nil {
		return err
	}
	frame, err := env.frame(0, 100)
	if err != nil {
		return err
	}
	env.definition.setBehavior(mode)
	receipt, evaluateErr := env.runner.EvaluateFrame(ctx, instance.ID(), frame)
	want := runner.OutcomePanic
	switch mode {
	case behaviorTimeout:
		want = runner.OutcomeTimedOut
	case behaviorInvalid:
		want = runner.OutcomeInvalid
	}
	if evaluateErr == nil || receipt.Outcome != want {
		return fmt.Errorf("%s containment returned %s, %v", name, receipt.Outcome, evaluateErr)
	}
	return assertNoPublication(ctx, env.store, instance.ID(), name)
}

func (run *gateRun) cancelledEvaluation() error {
	env, err := newEnvironment("cancelled", run.recorder, 1, 100*time.Millisecond)
	if err != nil {
		return err
	}
	run.track(env)
	instance, err := env.addInstance("cancelled")
	if err != nil {
		return err
	}
	frame, err := env.frame(0, 100)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	receipt, evaluateErr := env.runner.EvaluateFrame(ctx, instance.ID(), frame)
	run.recordCancellationShutdownDuration(time.Since(started))
	if !errors.Is(evaluateErr, context.Canceled) || receipt.Outcome != runner.OutcomeCancelled {
		return fmt.Errorf("cancellation containment returned %s, %v", receipt.Outcome, evaluateErr)
	}
	return assertNoPublication(context.Background(), env.store, instance.ID(), "cancelled")
}

func (run *gateRun) revisionConflict(ctx context.Context) error {
	env, err := newEnvironment("conflict", run.recorder, 1, 100*time.Millisecond)
	if err != nil {
		return err
	}
	run.track(env)
	instance, err := env.addInstance("conflict")
	if err != nil {
		return err
	}
	frame, err := env.frame(0, 100)
	if err != nil {
		return err
	}
	value, err := runner.New(runner.DefaultConfig(), env.registry,
		conflictRepository{Store: env.store}, env.gate, runner.RealClock{}, run.recorder)
	if err != nil {
		return err
	}
	receipt, evaluateErr := value.EvaluateFrame(ctx, instance.ID(), frame)
	if !errors.Is(evaluateErr, strategystorage.ErrRevisionConflict) ||
		receipt.Outcome != runner.OutcomeRevisionConflict {
		return fmt.Errorf("revision conflict returned %s, %v", receipt.Outcome, evaluateErr)
	}
	return assertNoPublication(ctx, env.store, instance.ID(), "conflict")
}

func (run *gateRun) shutdownContainment(ctx context.Context) error {
	env, err := newEnvironment("shutdown", run.recorder, 1, time.Second)
	if err != nil {
		return err
	}
	run.track(env)
	instance, err := env.addInstance("shutdown")
	if err != nil {
		return err
	}
	frame, err := env.frame(0, 100)
	if err != nil {
		return err
	}
	env.definition.startBlocking(1)
	result := make(chan runner.Receipt, 1)
	go func() {
		receipt, _ := env.runner.EvaluateFrame(ctx, instance.ID(), frame)
		result <- receipt
	}()
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := env.definition.waitEntered(waitCtx, 1); err != nil {
		return err
	}
	run.sampleResources()
	started := time.Now()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	err = env.runner.Shutdown(shutdownCtx)
	shutdownCancel()
	elapsed := time.Since(started)
	run.recordCancellationShutdownDuration(elapsed)
	receipt := <-result
	if err != nil || receipt.Outcome != runner.OutcomeShutdown {
		return fmt.Errorf("shutdown containment returned %s, %v", receipt.Outcome, err)
	}
	return assertNoPublication(context.Background(), env.store, instance.ID(), "shutdown")
}

func (run *gateRun) replayEquivalence(ctx context.Context) error {
	left, leftReceipts, err := run.replayAll(ctx, "replay-equivalence")
	if err != nil {
		return err
	}
	right, rightReceipts, err := run.replayAll(ctx, "replay-equivalence")
	if err != nil {
		return err
	}
	leftRecords, _ := left.store.Evaluations(ctx, left.instances["replay"].ID())
	rightRecords, _ := right.store.Evaluations(ctx, right.instances["replay"].ID())
	leftCheckpoint, _ := left.store.CurrentCheckpoint(ctx, left.instances["replay"].ID())
	rightCheckpoint, _ := right.store.CurrentCheckpoint(ctx, right.instances["replay"].ID())
	leftBytes, _ := strategystorage.EncodeCheckpoint(leftCheckpoint)
	rightBytes, _ := strategystorage.EncodeCheckpoint(rightCheckpoint)
	run.report.ReplayDeterminismPassed =
		reflect.DeepEqual(leftReceipts, rightReceipts) &&
			reflect.DeepEqual(leftRecords, rightRecords) &&
			string(leftBytes) == string(rightBytes) &&
			leftCheckpoint.Checksum() == rightCheckpoint.Checksum()
	if !run.report.ReplayDeterminismPassed {
		return errors.New("repeated replay was not deterministic")
	}

	continued, err := newEnvironment(
		"replay-equivalence", run.recorder, ConfiguredConcurrency, 100*time.Millisecond,
	)
	if err != nil {
		return err
	}
	run.track(left)
	run.track(right)
	run.track(continued)
	instance, err := continued.addInstance("replay")
	if err != nil {
		return err
	}
	sink, err := strategyreplay.NewSink(
		continued.runner, instance.ID(), continued.descriptor.Subscriptions,
		"release-gate-calendar/v1",
	)
	if err != nil {
		return err
	}
	for index, price := range []int64{100, 200} {
		event, createErr := continued.candle(index, price)
		if createErr != nil {
			return createErr
		}
		if err := sink.Consume(ctx, event); err != nil {
			return err
		}
		run.expectedCommitted++
	}
	intermediate, _ := continued.store.CurrentCheckpoint(ctx, instance.ID())
	encoded, _ := strategystorage.EncodeCheckpoint(intermediate)
	if _, err := continued.store.Restore(ctx, encoded, strategystorage.RestoreExpectation{
		InstanceID: instance.ID(), DefinitionID: instance.DefinitionID(),
		VersionID: instance.VersionID(), ConfigurationHash: instance.Configuration().Hash(),
		InstanceRevisionID: instance.RevisionID(), StateSchemaVersion: "release-gate-state/v1",
		Revision: intermediate.Revision(),
	}); err != nil {
		return err
	}
	restarted, err := runner.New(runner.DefaultConfig(), continued.registry, continued.store,
		continued.gate, runner.RealClock{}, run.recorder)
	if err != nil {
		return err
	}
	restartedSink, err := strategyreplay.NewSink(
		restarted, instance.ID(), continued.descriptor.Subscriptions,
		"release-gate-calendar/v1",
	)
	if err != nil {
		return err
	}
	last, err := continued.candle(2, 300)
	if err != nil {
		return err
	}
	if err := restartedSink.Consume(ctx, last); err != nil {
		return err
	}
	run.expectedCommitted++
	continuedRecords, _ := continued.store.Evaluations(ctx, instance.ID())
	continuedCheckpoint, _ := continued.store.CurrentCheckpoint(ctx, instance.ID())
	continuedBytes, _ := strategystorage.EncodeCheckpoint(continuedCheckpoint)
	run.report.CheckpointContinuationEquivalencePassed =
		reflect.DeepEqual(leftRecords, continuedRecords) &&
			string(leftBytes) == string(continuedBytes) &&
			leftCheckpoint.Checksum() == continuedCheckpoint.Checksum()
	if !run.report.CheckpointContinuationEquivalencePassed {
		return errors.New("checkpoint continuation differed from uninterrupted replay")
	}
	return nil
}

func (run *gateRun) replayAll(
	ctx context.Context,
	name string,
) (*environment, []runner.Receipt, error) {
	env, err := newEnvironment(name, run.recorder, ConfiguredConcurrency, 100*time.Millisecond)
	if err != nil {
		return nil, nil, err
	}
	instance, err := env.addInstance("replay")
	if err != nil {
		return nil, nil, err
	}
	sink, err := strategyreplay.NewSink(
		env.runner, instance.ID(), env.descriptor.Subscriptions,
		"release-gate-calendar/v1",
	)
	if err != nil {
		return nil, nil, err
	}
	for index, price := range []int64{100, 200, 300} {
		event, createErr := env.candle(index, price)
		if createErr != nil {
			return nil, nil, createErr
		}
		if err := sink.Consume(ctx, event); err != nil {
			return nil, nil, err
		}
		run.expectedCommitted++
	}
	return env, sink.Receipts(), nil
}

func (run *gateRun) sampleResources() {
	current := runtime.NumGoroutine()
	if current > run.peakGoroutines {
		run.peakGoroutines = current
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	if memory.HeapAlloc > run.peakHeap {
		run.peakHeap = memory.HeapAlloc
	}
}

func (run *gateRun) track(environment *environment) {
	run.environments = append(run.environments, environment)
	run.definitions = append(run.definitions, environment.definition)
}

func (run *gateRun) countPublications(ctx context.Context) (uint64, uint64, error) {
	var total uint64
	var duplicates uint64
	for _, environment := range run.environments {
		seen := make(map[strategymodel.EvaluationID]struct{})
		for _, instance := range environment.instances {
			records, err := environment.store.Evaluations(ctx, instance.ID())
			if err != nil {
				return 0, 0, fmt.Errorf("count committed evaluations: %w", err)
			}
			total += uint64(len(records))
			for _, record := range records {
				id := record.EvaluationID()
				if _, exists := seen[id]; exists {
					duplicates++
					continue
				}
				seen[id] = struct{}{}
			}
		}
	}
	return total, duplicates, nil
}

func (run *gateRun) recordCancellationShutdownDuration(duration time.Duration) {
	if duration.Nanoseconds() > run.report.MaximumCancellationShutdownNanoseconds {
		run.report.MaximumCancellationShutdownNanoseconds = duration.Nanoseconds()
	}
}

func (run *gateRun) fail(err error) {
	if err != nil {
		run.report.FailureReasons = append(run.report.FailureReasons, err.Error())
	}
}

func (run *gateRun) enforce() {
	checks := []struct {
		ok     bool
		reason string
	}{
		{run.report.CommittedNoActionCount >= 1, "no committed NO_ACTION result"},
		{run.report.CommittedObservationCount >= 1, "no committed observation result"},
		{run.report.CommittedProposalCount >= 1, "no committed proposal result"},
		{run.report.ReadinessBlockedCount == 1, "readiness-blocked count changed"},
		{run.report.ReadinessBlockedStrategyInvocations == 0, "readiness block invoked strategy"},
		{run.report.DuplicateCommittedCount == 1, "committed duplicate suppression changed"},
		{run.report.DuplicateInProgressCount == 1, "in-progress duplicate suppression changed"},
		{run.report.TimeoutCount == 1, "timeout containment count changed"},
		{run.report.PanicCount == 1, "panic containment count changed"},
		{run.report.CancellationCount == 1, "cancellation containment count changed"},
		{run.report.ShutdownCount == 1, "shutdown containment count changed"},
		{run.report.InvalidOutputCount == 1, "invalid-output containment count changed"},
		{run.report.RevisionConflictCount == 1, "revision-conflict count changed"},
		{run.report.UnexpectedResultLoss == 0, "unexpected result loss detected"},
		{run.report.UnexpectedDuplicatePublication == 0, "duplicate publication detected"},
		{run.report.MaximumConcurrentEvaluations <= ConfiguredConcurrency,
			"cross-instance concurrency exceeded configured limit"},
		{run.report.MaximumSameInstanceConcurrency == 1,
			"same-instance strategy invocation was concurrent"},
		{run.report.ReplayDeterminismPassed, "replay determinism failed"},
		{run.report.CheckpointContinuationEquivalencePassed,
			"checkpoint continuation equivalence failed"},
		{run.report.EndingGoroutineCount <=
			run.report.StartingGoroutineCount+EndingGoroutineTolerance,
			"ending goroutine tolerance exceeded"},
		{run.report.PeakGoroutineCount <=
			run.report.StartingGoroutineCount+PeakGoroutineGrowthLimit,
			"peak goroutine growth limit exceeded"},
		{run.report.EndingHeapAllocationBytes <=
			run.report.StartingHeapAllocationBytes+EndingHeapGrowthLimit,
			"ending heap growth limit exceeded"},
		{run.report.PeakHeapAllocationBytes <=
			run.report.StartingHeapAllocationBytes+PeakHeapGrowthLimit,
			"peak heap growth limit exceeded"},
		{run.report.MaximumCancellationShutdownNanoseconds <= CancellationDurationLimit.Nanoseconds(),
			"cancellation or shutdown duration exceeded limit"},
	}
	for _, check := range checks {
		if !check.ok {
			run.report.FailureReasons = append(run.report.FailureReasons, check.reason)
		}
	}
}

func (value *environment) frame(index int, price int64) (strategymodel.CandleFrame, error) {
	candle, err := value.candle(index, price)
	if err != nil {
		return strategymodel.CandleFrame{}, err
	}
	subscription := value.descriptor.Subscriptions.Subscriptions()[0]
	series, err := strategymodel.NewCandleSeries(
		subscription, []marketmodel.CompletedCandleEvent{candle},
	)
	if err != nil {
		return strategymodel.CandleFrame{}, err
	}
	trigger, err := strategymodel.NewTriggerID("release-gate-frame|" + candle.ID().String())
	if err != nil {
		return strategymodel.CandleFrame{}, err
	}
	return strategymodel.NewCandleFrame(strategymodel.CandleFrameSpec{
		TriggerID: trigger, LogicalTime: candle.CloseTime(),
		Subscription: value.descriptor.Subscriptions,
		Series:       []strategymodel.CandleSeries{series}, MasterVersion: "release-gate-master/v1",
		CalendarVersion: "release-gate-calendar/v1", DatasetRevision: "release-gate-dataset/v1",
	})
}

func (value *environment) candle(
	index int,
	priceValue int64,
) (marketmodel.CompletedCandleEvent, error) {
	closeTime := fixedTime().Add(time.Duration(index) * time.Minute)
	price, err := domain.NewPrice(priceValue, "INR")
	if err != nil {
		return marketmodel.CompletedCandleEvent{}, err
	}
	return marketmodel.NewCompletedCandleEvent(marketmodel.CandleSpec{
		InstrumentID: value.instrument, Interval: marketmodel.Interval1Minute,
		OpenTime: closeTime.Add(-time.Minute), CloseTime: closeTime,
		Open: price, High: price, Low: price, Close: price,
		Volume: 1, EventCount: 1, IngestedAt: closeTime,
		Provenance: marketmodel.Provenance{
			Provider: "release-gate", ProviderToken: "non-production-fixture",
			MasterVersion:   "release-gate-master/v1",
			DatasetRevision: "release-gate-dataset/v1",
		},
	})
}

func newDescriptor(instrument domain.InstrumentID) (strategymodel.Descriptor, error) {
	definitionID, err := strategymodel.NewDefinitionID("phase-2-release-gate")
	if err != nil {
		return strategymodel.Descriptor{}, err
	}
	subscriptions, err := strategymodel.NewSubscriptionSpec(
		strategymodel.SubscriptionSingleStream,
		[]strategymodel.InputSubscription{{
			Role: "primary", InstrumentID: instrument, Interval: marketmodel.Interval1Minute,
			Required: true, Trigger: true, Lookback: 1, MaximumAge: time.Minute,
		}},
	)
	if err != nil {
		return strategymodel.Descriptor{}, err
	}
	return strategymodel.NewDescriptor(strategymodel.VersionManifest{
		DefinitionID: definitionID, ImplementationVersion: "release-gate/v1",
		InputContractVersion:       "candle-frame/v1",
		ConfigurationSchemaVersion: "release-gate-config/v1",
		StateSchemaVersion:         "release-gate-state/v1",
		ResultSchemaVersion:        "strategy-result/v1", ProposalSchemaVersion: "proposal/v1",
	}, subscriptions)
}

func automaticResult(
	input strategymodel.EvaluationContext,
) (strategymodel.EvaluationResult, error) {
	price := input.Frame.Series()[0].Candles[0].Close().MinorUnits()
	switch price {
	case 100:
		return noAction(input)
	case 200:
		next, err := nextState(input.PriorState)
		if err != nil {
			return strategymodel.EvaluationResult{}, err
		}
		return strategymodel.NewObservationResult(next, strategymodel.ObservationDraft{
			Code: "RELEASE_GATE_OBSERVATION", Explanation: "deterministic release-gate observation",
			Evidence: []strategymodel.Evidence{{
				Code: "RELEASE_GATE_EVIDENCE", SourceEventIDs: input.Frame.SourceEventIDs(),
				Value: price, Unit: "PRICE_MINOR_UNITS", Explanation: "fixture evidence",
			}},
		})
	default:
		next, err := nextState(input.PriorState)
		if err != nil {
			return strategymodel.EvaluationResult{}, err
		}
		candle := input.Frame.Series()[0].Candles[0]
		return strategymodel.NewTradeProposalResult(next, strategymodel.ProposalDraft{
			SchemaVersion: "proposal/v1",
			Legs: []strategymodel.ProposalLeg{{
				InstrumentID: candle.InstrumentID(), Side: domain.SideBuy, Ratio: 1,
				ReferencePrice: candle.Close(), MaxDeviationBPS: 100,
			}},
			Sizing: strategymodel.SizingIntent{
				Kind: strategymodel.SizingStrategyBudgetBPS, ValueBPS: 100,
			},
			ValidFrom: input.LogicalTime, ExpiresAt: input.LogicalTime.Add(time.Minute),
			RationaleCode: "RELEASE_GATE_PROPOSAL", Explanation: "non-production advisory fixture",
			Evidence: []strategymodel.Evidence{{
				Code: "RELEASE_GATE_EVIDENCE", SourceEventIDs: input.Frame.SourceEventIDs(),
				Value: price, Unit: "PRICE_MINOR_UNITS", Explanation: "fixture evidence",
			}},
			ExitPolicyReference: "release-gate-exit/v1",
		})
	}
}

func noAction(
	input strategymodel.EvaluationContext,
) (strategymodel.EvaluationResult, error) {
	next, err := nextState(input.PriorState)
	if err != nil {
		return strategymodel.EvaluationResult{}, err
	}
	return strategymodel.NewNoActionResult(
		next, strategymodel.NoActionConditionsNotMet, "release-gate no action",
	)
}

func nextState(
	prior strategymodel.StrategyRuntimeState,
) (strategymodel.StrategyRuntimeState, error) {
	var value struct {
		Count uint64 `json:"count"`
	}
	if err := json.Unmarshal(prior.CanonicalJSON(), &value); err != nil {
		return strategymodel.StrategyRuntimeState{}, err
	}
	value.Count++
	return newState(value.Count)
}

func newState(count uint64) (strategymodel.StrategyRuntimeState, error) {
	return strategymodel.NewStrategyRuntimeState(
		"release-gate-state/v1",
		[]byte(fmt.Sprintf(`{"count":%d}`, count)),
	)
}

func assertNoPublication(
	ctx context.Context,
	store *strategymemory.Store,
	instanceID domain.StrategyID,
	name string,
) error {
	checkpoint, err := store.CurrentCheckpoint(ctx, instanceID)
	if err != nil || checkpoint.Revision() != 0 {
		return fmt.Errorf("%s advanced checkpoint: revision=%d err=%v",
			name, checkpoint.Revision(), err)
	}
	evaluations, err := store.Evaluations(ctx, instanceID)
	if err != nil {
		return err
	}
	if len(evaluations) != 0 {
		return fmt.Errorf("%s published %d evaluation records", name, len(evaluations))
	}
	return nil
}

func fixedTime() time.Time {
	return time.Date(2026, 7, 18, 4, 1, 0, 0, time.UTC)
}
