package runner

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	strategymemory "github.com/bibhuyash/tradeedge/internal/adapters/strategy/memory"
	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
	strategystorage "github.com/bibhuyash/tradeedge/internal/strategy/storage"
	strategytelemetry "github.com/bibhuyash/tradeedge/internal/strategy/telemetry"
)

type testDefinition struct {
	descriptor strategymodel.Descriptor
	evaluate   func(context.Context, strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error)
	calls      atomic.Int64
}

func (definition *testDefinition) Descriptor() strategymodel.Descriptor {
	return definition.descriptor
}
func (*testDefinition) ValidateConfiguration(strategymodel.StrategyConfiguration) error {
	return nil
}
func (*testDefinition) InitialState(
	strategymodel.StrategyConfiguration,
) (strategymodel.StrategyRuntimeState, error) {
	return state(0), nil
}
func (definition *testDefinition) Evaluate(
	ctx context.Context,
	input strategymodel.EvaluationContext,
) (strategymodel.EvaluationResult, error) {
	definition.calls.Add(1)
	return definition.evaluate(ctx, input)
}

type gate struct {
	evidence strategymodel.ReadinessEvidence
}

func (value gate) Evidence(
	context.Context,
	strategymodel.StrategyInstance,
	strategymodel.CandleFrame,
) strategymodel.ReadinessEvidence {
	return value.evidence
}

type snapshotSource struct {
	snapshot readiness.Snapshot
}

func (source snapshotSource) Snapshot(context.Context) readiness.Snapshot {
	return source.snapshot
}

type runnerFixture struct {
	store      *strategymemory.Store
	definition *testDefinition
	runner     *Runner
	instance   strategymodel.StrategyInstance
	frame      strategymodel.CandleFrame
	instrument domain.InstrumentID
	recorder   *strategytelemetry.MemoryRecorder
}

func TestRegistryRejectsDifferentImplementationForSameVersion(t *testing.T) {
	t.Parallel()
	instrument, _ := domain.InstrumentIDFromCanonicalKey("NSE|INDEX|registry")
	value := descriptor(t, instrument, 1)
	first := &testDefinition{descriptor: value}
	second := &testDefinition{descriptor: value}
	registry := NewRegistry()
	if err := registry.Register(first); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(first); err != nil {
		t.Fatalf("exact registration retry failed: %v", err)
	}
	if err := registry.Register(second); !errors.Is(err, ErrDefinitionNotRegistered) {
		t.Fatalf("implementation collision error = %v", err)
	}
}

func TestSnapshotReadinessGateRequiresExactCandleCoverage(t *testing.T) {
	t.Parallel()
	fixture := newRunnerFixture(t, strategymodel.LifecycleCandidate,
		func(_ context.Context, input strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
			return noAction(input), nil
		})
	snapshot := readiness.Snapshot{
		EvaluatedAt: fixedTime(), CalendarVersion: "calendar/v1", PolicyVersion: "policy/v1",
		State: readiness.StateReady, TradingPermitted: true,
		Diagnostics: []readiness.Diagnostic{{
			InstrumentID: fixture.instrument, Instrument: fixture.instrument.String(),
			EventKind: marketmodel.EventKindCandle, Interval: marketmodel.Interval1Minute,
			Required: true, State: readiness.StateReady, Reason: readiness.ReasonNone,
		}},
	}
	value, err := NewSnapshotReadinessGate(snapshotSource{snapshot: snapshot})
	if err != nil || !value.Evidence(
		context.Background(), fixture.instance, fixture.frame,
	).Ready() {
		t.Fatalf("ready evidence failed: %v", err)
	}
	snapshot.Diagnostics = nil
	value, _ = NewSnapshotReadinessGate(snapshotSource{snapshot: snapshot})
	evidence := value.Evidence(context.Background(), fixture.instance, fixture.frame)
	if evidence.Ready() || evidence.Reasons[0] != readiness.ReasonCoverageIncomplete {
		t.Fatalf("missing coverage evidence = %#v", evidence)
	}
}

func TestCommittedOutcomesAndDuplicate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		outcome Outcome
		result  func(strategymodel.EvaluationContext) strategymodel.EvaluationResult
	}{
		{"no action", OutcomeNoAction, noAction},
		{"observation", OutcomeObservation, observation},
		{"proposal", OutcomeTradeProposal, proposal},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRunnerFixture(t, strategymodel.LifecycleCandidate,
				func(_ context.Context, input strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
					return test.result(input), nil
				})
			first, err := fixture.runner.EvaluateFrame(context.Background(), fixture.instance.ID(), fixture.frame)
			if err != nil || first.Outcome != test.outcome || first.StateRevision != 1 {
				t.Fatalf("EvaluateFrame() = %#v, %v", first, err)
			}
			second, err := fixture.runner.EvaluateFrame(context.Background(), fixture.instance.ID(), fixture.frame)
			if !errors.Is(err, ErrDuplicateTrigger) ||
				second.Outcome != OutcomeDuplicateCommitted ||
				second.EvaluationID != first.EvaluationID ||
				second.TriggerID != first.TriggerID {
				t.Fatalf("duplicate = %#v, %v", second, err)
			}
			if fixture.definition.calls.Load() != 1 {
				t.Fatalf("strategy calls = %d", fixture.definition.calls.Load())
			}
			metrics := fixture.recorder.Snapshot()
			if metrics.Counts["STARTED"] != 1 ||
				metrics.Counts[string(test.outcome)] != 1 || metrics.LastStateSize == 0 {
				t.Fatalf("telemetry = %#v", metrics)
			}
		})
	}
}

func TestReadinessAndLifecycleBlockBeforeInvocation(t *testing.T) {
	t.Parallel()
	blocked := newRunnerFixture(t, strategymodel.LifecycleCandidate,
		func(_ context.Context, input strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
			return noAction(input), nil
		})
	blocked.runner.readiness = gate{evidence: strategymodel.ReadinessEvidence{
		State: readiness.StateStale, Reasons: []readiness.ReasonCode{readiness.ReasonExchangeTimeStale},
		PolicyVersion: "policy/v1", CalendarVersion: "calendar/v1",
		EvaluatedAt: fixedTime(),
	}}
	receipt, err := blocked.runner.EvaluateFrame(
		context.Background(), blocked.instance.ID(), blocked.frame,
	)
	if !errors.Is(err, ErrReadinessBlocked) || receipt.Outcome != OutcomeReadinessBlocked ||
		blocked.definition.calls.Load() != 0 {
		t.Fatalf("blocked = %#v, %v calls=%d", receipt, err, blocked.definition.calls.Load())
	}

	ineligible := newRunnerFixture(t, strategymodel.LifecycleSuspended,
		func(_ context.Context, input strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
			return noAction(input), nil
		})
	receipt, err = ineligible.runner.EvaluateFrame(
		context.Background(), ineligible.instance.ID(), ineligible.frame,
	)
	if !errors.Is(err, ErrLifecycle) || receipt.Outcome != OutcomeLifecycleIneligible ||
		ineligible.definition.calls.Load() != 0 {
		t.Fatalf("ineligible = %#v, %v", receipt, err)
	}
}

func TestRetryAfterNonCommittedFailures(t *testing.T) {
	t.Parallel()
	t.Run("panic", func(t *testing.T) {
		var calls atomic.Int64
		fixture := newRunnerFixture(t, strategymodel.LifecycleCandidate,
			func(_ context.Context, input strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
				if calls.Add(1) == 1 {
					panic("bounded fixture panic")
				}
				return noAction(input), nil
			})
		receipt, err := fixture.runner.EvaluateFrame(context.Background(), fixture.instance.ID(), fixture.frame)
		if !errors.Is(err, ErrStrategyPanic) || receipt.Outcome != OutcomePanic ||
			len(receipt.Diagnostic) == 0 || len(receipt.Diagnostic) > 8450 {
			t.Fatalf("panic = %#v, %v", receipt, err)
		}
		assertRevision(t, fixture, 0)
		receipt, err = fixture.runner.EvaluateFrame(context.Background(), fixture.instance.ID(), fixture.frame)
		if err != nil || receipt.Outcome != OutcomeNoAction {
			t.Fatalf("panic recovery = %#v, %v", receipt, err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		var calls atomic.Int64
		fixture := newRunnerFixture(t, strategymodel.LifecycleCandidate,
			func(ctx context.Context, input strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
				if calls.Add(1) == 1 {
					<-ctx.Done()
					return strategymodel.EvaluationResult{}, ctx.Err()
				}
				return noAction(input), nil
			})
		fixture.runner.config.Timeout = 10 * time.Millisecond
		receipt, err := fixture.runner.EvaluateFrame(context.Background(), fixture.instance.ID(), fixture.frame)
		if !errors.Is(err, ErrStrategyTimeout) || receipt.Outcome != OutcomeTimedOut {
			t.Fatalf("timeout = %#v, %v", receipt, err)
		}
		assertRevision(t, fixture, 0)
		receipt, err = fixture.runner.EvaluateFrame(context.Background(), fixture.instance.ID(), fixture.frame)
		if err != nil || receipt.Outcome != OutcomeNoAction {
			t.Fatalf("timeout recovery = %#v, %v", receipt, err)
		}
	})
	t.Run("publication", func(t *testing.T) {
		fixture := newRunnerFixture(t, strategymodel.LifecycleCandidate,
			func(_ context.Context, input strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
				return noAction(input), nil
			})
		fixture.store.SetFailureInjector(func(strategymemory.FailurePoint) error {
			return errors.New("injected")
		})
		receipt, err := fixture.runner.EvaluateFrame(context.Background(), fixture.instance.ID(), fixture.frame)
		if err == nil || receipt.Outcome != OutcomePublicationFailure {
			t.Fatalf("publication failure = %#v, %v", receipt, err)
		}
		assertRevision(t, fixture, 0)
		fixture.store.SetFailureInjector(nil)
		receipt, err = fixture.runner.EvaluateFrame(context.Background(), fixture.instance.ID(), fixture.frame)
		if err != nil || receipt.Outcome != OutcomeNoAction {
			t.Fatalf("publication retry = %#v, %v", receipt, err)
		}
	})
}

func TestCancellationAndInvalidOutputPublishNothing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		evaluate func(context.Context, strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error)
		cancel   bool
		outcome  Outcome
	}{
		{"cancelled", func(ctx context.Context, _ strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
			<-ctx.Done()
			return strategymodel.EvaluationResult{}, ctx.Err()
		}, true, OutcomeCancelled},
		{"invalid", func(context.Context, strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
			return strategymodel.EvaluationResult{}, nil
		}, false, OutcomeInvalid},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerFixture(t, strategymodel.LifecycleCandidate, test.evaluate)
			ctx := context.Background()
			if test.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			receipt, err := fixture.runner.EvaluateFrame(ctx, fixture.instance.ID(), fixture.frame)
			if err == nil || receipt.Outcome != test.outcome {
				t.Fatalf("EvaluateFrame() = %#v, %v", receipt, err)
			}
			current, _ := fixture.store.CurrentCheckpoint(context.Background(), fixture.instance.ID())
			if current.Revision() != 0 {
				t.Fatalf("failed evaluation published revision %d", current.Revision())
			}
		})
	}
}

type conflictRepository struct{ *strategymemory.Store }

func (repository conflictRepository) PublishEvaluation(
	context.Context,
	strategystorage.EvaluationPublication,
) (strategystorage.PublicationOutcome, error) {
	return strategystorage.PublicationOutcome{
			Status: strategystorage.PublicationRevisionConflict,
		}, &strategystorage.RevisionConflictError{
			InstanceID: "conflict", Expected: 0, Actual: 1,
		}
}

func TestRevisionConflictPublishesNothing(t *testing.T) {
	t.Parallel()
	fixture := newRunnerFixture(t, strategymodel.LifecycleCandidate,
		func(_ context.Context, input strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
			return noAction(input), nil
		})
	value, err := New(DefaultConfig(), fixture.runner.registry,
		conflictRepository{Store: fixture.store}, gate{evidence: readyEvidence()},
		RealClock{}, strategytelemetry.NopRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := value.EvaluateFrame(context.Background(), fixture.instance.ID(), fixture.frame)
	if !errors.Is(err, strategystorage.ErrRevisionConflict) ||
		receipt.Outcome != OutcomeRevisionConflict {
		t.Fatalf("conflict = %#v, %v", receipt, err)
	}
	current, _ := fixture.store.CurrentCheckpoint(context.Background(), fixture.instance.ID())
	if current.Revision() != 0 {
		t.Fatalf("conflict published revision %d", current.Revision())
	}
}

func TestPerInstanceSerializationAndInProgressDuplicate(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture := newRunnerFixture(t, strategymodel.LifecycleCandidate,
		func(ctx context.Context, input strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
			select {
			case <-entered:
			default:
				close(entered)
			}
			select {
			case <-release:
				return noAction(input), nil
			case <-ctx.Done():
				return strategymodel.EvaluationResult{}, ctx.Err()
			}
		})
	done := make(chan error, 1)
	go func() {
		_, err := fixture.runner.EvaluateFrame(context.Background(), fixture.instance.ID(), fixture.frame)
		done <- err
	}()
	<-entered
	receipt, err := fixture.runner.EvaluateFrame(context.Background(), fixture.instance.ID(), fixture.frame)
	if !errors.Is(err, ErrDuplicateTrigger) || receipt.Outcome != OutcomeDuplicateInProgress {
		t.Fatalf("in-progress duplicate = %#v, %v", receipt, err)
	}
	other := frameAt(t, fixture.definition.descriptor.Subscriptions, fixture.instrument, fixedTime().Add(time.Minute), 101)
	receipt, err = fixture.runner.EvaluateFrame(context.Background(), fixture.instance.ID(), other)
	if !errors.Is(err, ErrInstanceBusy) || receipt.Outcome != OutcomeInstanceBusy {
		t.Fatalf("instance busy = %#v, %v", receipt, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, _, keyed, _ := fixture.runner.Health()
	if keyed != 0 {
		t.Fatalf("retained keyed state = %d", keyed)
	}
}

func TestShutdownCancelsInflightAndRejectsNewWork(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	fixture := newRunnerFixture(t, strategymodel.LifecycleCandidate,
		func(ctx context.Context, _ strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
			close(entered)
			<-ctx.Done()
			return strategymodel.EvaluationResult{}, ctx.Err()
		})
	done := make(chan struct{})
	go func() {
		_, _ = fixture.runner.EvaluateFrame(context.Background(), fixture.instance.ID(), fixture.frame)
		close(done)
	}()
	<-entered
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := fixture.runner.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	<-done
	receipt, err := fixture.runner.EvaluateFrame(context.Background(), fixture.instance.ID(),
		frameAt(t, fixture.definition.descriptor.Subscriptions, fixture.instrument, fixedTime().Add(time.Minute), 101))
	if !errors.Is(err, ErrRunnerShutdown) || receipt.Outcome != OutcomeShutdown {
		t.Fatalf("after shutdown = %#v, %v", receipt, err)
	}
}

func TestBoundedParallelismAcrossInstances(t *testing.T) {
	store := strategymemory.NewStore()
	instrument, _ := domain.InstrumentIDFromCanonicalKey("NSE|INDEX|parallel")
	descriptor := descriptor(t, instrument, 1)
	var active atomic.Int64
	var maximum atomic.Int64
	release := make(chan struct{})
	definition := &testDefinition{descriptor: descriptor}
	definition.evaluate = func(ctx context.Context, input strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			seen := maximum.Load()
			if current <= seen || maximum.CompareAndSwap(seen, current) {
				break
			}
		}
		select {
		case <-release:
			return noAction(input), nil
		case <-ctx.Done():
			return strategymodel.EvaluationResult{}, ctx.Err()
		}
	}
	registry := NewRegistry()
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	initializeDefinition(t, store, descriptor)
	instances := make([]strategymodel.StrategyInstance, 3)
	frames := make([]strategymodel.CandleFrame, 3)
	for index := range instances {
		instances[index] = initializeInstance(t, store, descriptor,
			fmt.Sprintf("parallel-%d", index), strategymodel.LifecycleCandidate)
		frames[index] = frameAt(t, descriptor.Subscriptions, instrument,
			fixedTime().Add(time.Duration(index)*time.Minute), int64(100+index))
	}
	value, err := New(Config{MaxConcurrency: 2, Timeout: time.Second}, registry, store,
		gate{evidence: readyEvidence()}, RealClock{}, strategytelemetry.NopRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errs := make(chan error, len(instances))
	for index := range instances {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, evaluateErr := value.EvaluateFrame(context.Background(), instances[index].ID(), frames[index])
			errs <- evaluateErr
		}(index)
	}
	deadline := time.Now().Add(time.Second)
	for maximum.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency before release = %d", maximum.Load())
	}
	close(release)
	wait.Wait()
	close(errs)
	for evaluateErr := range errs {
		if evaluateErr != nil {
			t.Fatal(evaluateErr)
		}
	}
	if maximum.Load() > 2 {
		t.Fatalf("maximum concurrency = %d", maximum.Load())
	}
}

func TestDeterministicRepeatedAndRestartedEvaluation(t *testing.T) {
	build := func(t *testing.T) runnerFixture {
		return newRunnerFixture(t, strategymodel.LifecycleCandidate,
			func(_ context.Context, input strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
				return proposal(input), nil
			})
	}
	uninterrupted := build(t)
	firstA, err := uninterrupted.runner.EvaluateFrame(
		context.Background(), uninterrupted.instance.ID(), uninterrupted.frame,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondFrameA := frameAt(t, uninterrupted.definition.descriptor.Subscriptions,
		uninterrupted.instrument, fixedTime().Add(time.Minute), 101)
	secondA, err := uninterrupted.runner.EvaluateFrame(
		context.Background(), uninterrupted.instance.ID(), secondFrameA,
	)
	if err != nil {
		t.Fatal(err)
	}
	finalA, _ := uninterrupted.store.CurrentCheckpoint(
		context.Background(), uninterrupted.instance.ID(),
	)
	recordsA, _ := uninterrupted.store.Evaluations(
		context.Background(), uninterrupted.instance.ID(),
	)
	bytesA, _ := strategystorage.EncodeCheckpoint(finalA)

	restarted := build(t)
	firstB, err := restarted.runner.EvaluateFrame(
		context.Background(), restarted.instance.ID(), restarted.frame,
	)
	if err != nil {
		t.Fatal(err)
	}
	intermediate, _ := restarted.store.CurrentCheckpoint(
		context.Background(), restarted.instance.ID(),
	)
	encoded, _ := strategystorage.EncodeCheckpoint(intermediate)
	if _, err := restarted.store.Restore(context.Background(), encoded,
		strategystorage.RestoreExpectation{
			InstanceID: restarted.instance.ID(), DefinitionID: restarted.instance.DefinitionID(),
			VersionID:          restarted.instance.VersionID(),
			ConfigurationHash:  restarted.instance.Configuration().Hash(),
			InstanceRevisionID: restarted.instance.RevisionID(),
			StateSchemaVersion: "state/v1", Revision: 1,
		}); err != nil {
		t.Fatal(err)
	}
	restartedRunner, err := New(DefaultConfig(), restarted.runner.registry, restarted.store,
		gate{evidence: readyEvidence()}, RealClock{}, strategytelemetry.NopRecorder{})
	if err != nil {
		t.Fatal(err)
	}
	secondFrameB := frameAt(t, restarted.definition.descriptor.Subscriptions,
		restarted.instrument, fixedTime().Add(time.Minute), 101)
	secondB, err := restartedRunner.EvaluateFrame(
		context.Background(), restarted.instance.ID(), secondFrameB,
	)
	if err != nil {
		t.Fatal(err)
	}
	finalB, _ := restarted.store.CurrentCheckpoint(context.Background(), restarted.instance.ID())
	recordsB, _ := restarted.store.Evaluations(context.Background(), restarted.instance.ID())
	bytesB, _ := strategystorage.EncodeCheckpoint(finalB)

	if firstA.TriggerID != firstB.TriggerID || firstA.EvaluationID != firstB.EvaluationID ||
		firstA.ProposalID != firstB.ProposalID || secondA.TriggerID != secondB.TriggerID ||
		secondA.EvaluationID != secondB.EvaluationID || secondA.ProposalID != secondB.ProposalID ||
		string(bytesA) != string(bytesB) || finalA.Checksum() != finalB.Checksum() ||
		!reflect.DeepEqual(recordsA, recordsB) {
		t.Fatalf("determinism mismatch:\nA=%#v %#v %s\nB=%#v %#v %s",
			firstA, secondA, bytesA, firstB, secondB, bytesB)
	}
}

func newRunnerFixture(
	t *testing.T,
	lifecycle strategymodel.LifecycleState,
	evaluate func(context.Context, strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error),
) runnerFixture {
	t.Helper()
	store := strategymemory.NewStore()
	instrument, _ := domain.InstrumentIDFromCanonicalKey("NSE|INDEX|" + t.Name())
	descriptor := descriptor(t, instrument, 1)
	definition := &testDefinition{descriptor: descriptor, evaluate: evaluate}
	registry := NewRegistry()
	if err := registry.Register(definition); err != nil {
		t.Fatal(err)
	}
	initializeDefinition(t, store, descriptor)
	instance := initializeInstance(t, store, descriptor, "instance-"+sanitize(t.Name()), lifecycle)
	recorder := strategytelemetry.NewMemoryRecorder()
	value, err := New(DefaultConfig(), registry, store, gate{evidence: readyEvidence()},
		RealClock{}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	return runnerFixture{
		store: store, definition: definition, runner: value, instance: instance,
		frame:      frameAt(t, descriptor.Subscriptions, instrument, fixedTime(), 100),
		instrument: instrument, recorder: recorder,
	}
}

func descriptor(t *testing.T, instrument domain.InstrumentID, lookback int) strategymodel.Descriptor {
	t.Helper()
	definitionID, _ := strategymodel.NewDefinitionID("runner-fixture")
	subscriptions, err := strategymodel.NewSubscriptionSpec(
		strategymodel.SubscriptionSingleStream,
		[]strategymodel.InputSubscription{{
			Role: "primary", InstrumentID: instrument, Interval: marketmodel.Interval1Minute,
			Required: true, Trigger: true, Lookback: lookback, MaximumAge: time.Minute,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	value, err := strategymodel.NewDescriptor(strategymodel.VersionManifest{
		DefinitionID: definitionID, ImplementationVersion: "runner-fixture/v1",
		InputContractVersion: "candle-frame/v1", ConfigurationSchemaVersion: "config/v1",
		StateSchemaVersion: "state/v1", ResultSchemaVersion: "result/v1",
		ProposalSchemaVersion: "proposal/v1",
	}, subscriptions)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func initializeDefinition(
	t *testing.T,
	store *strategymemory.Store,
	descriptor strategymodel.Descriptor,
) {
	t.Helper()
	record, _ := strategystorage.NewDefinitionRecord(
		descriptor.Manifest.DefinitionID, "definition/v1", "Runner fixture", "runner test fixture",
	)
	if _, err := store.RegisterDefinition(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterVersion(context.Background(), descriptor); err != nil {
		t.Fatal(err)
	}
}

func initializeInstance(
	t *testing.T,
	store *strategymemory.Store,
	descriptor strategymodel.Descriptor,
	id string,
	lifecycle strategymodel.LifecycleState,
) strategymodel.StrategyInstance {
	t.Helper()
	configuration, _ := strategymodel.NewStrategyConfiguration("config/v1", []byte(`{"value":1}`))
	instanceID, _ := domain.NewStrategyID(id)
	instance, err := strategymodel.NewStrategyInstance(
		instanceID, descriptor, configuration, 1, lifecycle,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutInstance(
		context.Background(), strategymodel.InstanceRevisionID{}, instance,
	); err != nil {
		t.Fatal(err)
	}
	root, err := strategystorage.NewRuntimeCheckpoint(strategystorage.RuntimeCheckpointSpec{
		InstanceID: instance.ID(), DefinitionID: instance.DefinitionID(),
		VersionID: instance.VersionID(), InstanceRevisionID: instance.RevisionID(),
		ConfigurationHash: instance.Configuration().Hash(), State: state(0),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InitializeCheckpoint(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	return instance
}

func frameAt(
	t *testing.T,
	subscriptions strategymodel.SubscriptionSpec,
	instrument domain.InstrumentID,
	closeTime time.Time,
	closePrice int64,
) strategymodel.CandleFrame {
	t.Helper()
	price, _ := domain.NewPrice(closePrice, "INR")
	low, _ := domain.NewPrice(closePrice-1, "INR")
	high, _ := domain.NewPrice(closePrice+1, "INR")
	candle, err := marketmodel.NewCompletedCandleEvent(marketmodel.CandleSpec{
		InstrumentID: instrument, Interval: marketmodel.Interval1Minute,
		OpenTime: closeTime.Add(-time.Minute), CloseTime: closeTime,
		Open: price, High: high, Low: low, Close: price, Volume: 1, EventCount: 1,
		IngestedAt: closeTime, Provenance: marketmodel.Provenance{
			Provider: "fixture", ProviderToken: "redacted-fixture",
			MasterVersion: "master/v1", DatasetRevision: "dataset/v1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	subscription := subscriptions.Subscriptions()[0]
	series, err := strategymodel.NewCandleSeries(subscription, []marketmodel.CompletedCandleEvent{candle})
	if err != nil {
		t.Fatal(err)
	}
	trigger, _ := strategymodel.NewTriggerID("frame-trigger|" + candle.ID().String())
	value, err := strategymodel.NewCandleFrame(strategymodel.CandleFrameSpec{
		TriggerID: trigger, LogicalTime: closeTime, Subscription: subscriptions,
		Series: []strategymodel.CandleSeries{series}, MasterVersion: "master/v1",
		CalendarVersion: "calendar/v1", DatasetRevision: "dataset/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func readyEvidence() strategymodel.ReadinessEvidence {
	return strategymodel.ReadinessEvidence{
		State: readiness.StateReady, Reasons: []readiness.ReasonCode{readiness.ReasonNone},
		PolicyVersion: "policy/v1", CalendarVersion: "calendar/v1", EvaluatedAt: fixedTime(),
	}
}

func noAction(input strategymodel.EvaluationContext) strategymodel.EvaluationResult {
	value, _ := strategymodel.NewNoActionResult(
		state(1), strategymodel.NoActionConditionsNotMet, "fixture no action",
	)
	return value
}

func observation(input strategymodel.EvaluationContext) strategymodel.EvaluationResult {
	value, _ := strategymodel.NewObservationResult(state(1), strategymodel.ObservationDraft{
		Code: "FIXTURE_OBSERVATION", Explanation: "bounded fixture observation",
		Evidence: []strategymodel.Evidence{{
			Code: "FIXTURE_EVIDENCE", SourceEventIDs: input.Frame.SourceEventIDs(),
			Value: 1, Unit: "COUNT", Explanation: "fixture evidence",
		}},
	})
	return value
}

func proposal(input strategymodel.EvaluationContext) strategymodel.EvaluationResult {
	candle := input.Frame.Series()[0].Candles[0]
	value, _ := strategymodel.NewTradeProposalResult(state(1), strategymodel.ProposalDraft{
		SchemaVersion: "proposal/v1",
		Legs: []strategymodel.ProposalLeg{{
			InstrumentID: candle.InstrumentID(), Side: domain.SideBuy, Ratio: 1,
			ReferencePrice: candle.Close(), MaxDeviationBPS: 100,
		}},
		Sizing: strategymodel.SizingIntent{
			Kind: strategymodel.SizingStrategyBudgetBPS, ValueBPS: 100,
		},
		ValidFrom: input.LogicalTime, ExpiresAt: input.LogicalTime.Add(time.Minute),
		RationaleCode: "FIXTURE_PROPOSAL", Explanation: "advisory fixture only",
		Evidence: []strategymodel.Evidence{{
			Code: "FIXTURE_EVIDENCE", SourceEventIDs: input.Frame.SourceEventIDs(),
			Value: 1, Unit: "COUNT", Explanation: "fixture evidence",
		}},
		ExitPolicyReference: "fixture-exit/v1",
	})
	return value
}

func state(value int) strategymodel.StrategyRuntimeState {
	result, _ := strategymodel.NewStrategyRuntimeState(
		"state/v1", []byte(fmt.Sprintf(`{"count":%d}`, value)),
	)
	return result
}

func fixedTime() time.Time {
	return time.Date(2026, 7, 18, 4, 1, 0, 0, time.UTC)
}

func sanitize(value string) string {
	result := ""
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			result += string(character)
		default:
			result += "-"
		}
	}
	return result
}

func assertRevision(t *testing.T, fixture runnerFixture, want uint64) {
	t.Helper()
	current, err := fixture.store.CurrentCheckpoint(context.Background(), fixture.instance.ID())
	if err != nil || current.Revision() != want {
		t.Fatalf("current revision = %d, %v; want %d", current.Revision(), err, want)
	}
}
