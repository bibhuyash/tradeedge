package runner

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
	strategystorage "github.com/bibhuyash/tradeedge/internal/strategy/storage"
	strategytelemetry "github.com/bibhuyash/tradeedge/internal/strategy/telemetry"
)

const triggerSchema = "strategy-evaluation-trigger/v1"

type Runner struct {
	config     Config
	registry   *Registry
	repository Repository
	readiness  ReadinessGate
	clock      Clock
	telemetry  strategytelemetry.Recorder
	ctx        context.Context
	cancel     context.CancelFunc
	semaphore  chan struct{}
	mu         sync.Mutex
	running    map[domain.StrategyID]strategymodel.TriggerID
	closed     bool
	failures   []Failure
	wait       sync.WaitGroup
	inFlight   atomic.Int64
	shutdown   sync.Once
	done       chan struct{}
}

func New(
	config Config,
	registry *Registry,
	repository Repository,
	readiness ReadinessGate,
	clock Clock,
	recorder strategytelemetry.Recorder,
) (*Runner, error) {
	if config.MaxConcurrency <= 0 || config.MaxConcurrency > 64 ||
		config.Timeout <= 0 || config.Timeout > time.Minute || registry == nil ||
		repository == nil || readiness == nil {
		return nil, errors.New("invalid strategy runner dependencies")
	}
	if clock == nil {
		clock = RealClock{}
	}
	if recorder == nil {
		recorder = strategytelemetry.NopRecorder{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{
		config: config, registry: registry, repository: repository, readiness: readiness,
		clock: clock, telemetry: recorder, ctx: ctx, cancel: cancel,
		semaphore: make(chan struct{}, config.MaxConcurrency),
		running:   make(map[domain.StrategyID]strategymodel.TriggerID),
		done:      make(chan struct{}),
	}, nil
}

func (runner *Runner) EvaluateFrame(
	ctx context.Context,
	instanceID domain.StrategyID,
	frame strategymodel.CandleFrame,
) (receipt Receipt, err error) {
	if err := ctx.Err(); err != nil {
		return runner.failure(instanceID, OutcomeCancelled, strategymodel.TriggerID{}, err)
	}
	instance, err := runner.repository.Instance(ctx, instanceID)
	if err != nil {
		return runner.failure(instanceID, OutcomeInternalFailure, strategymodel.TriggerID{}, err)
	}
	definition, err := runner.registry.Resolve(instance.VersionID())
	if err != nil {
		return runner.failure(instanceID, OutcomeInternalFailure, strategymodel.TriggerID{}, err)
	}
	if err := definition.ValidateConfiguration(instance.Configuration()); err != nil {
		return runner.failure(instanceID, OutcomeInvalid, strategymodel.TriggerID{}, err)
	}
	triggerID, evaluationID, err := deriveIdentities(instance, frame)
	if err != nil {
		return runner.failure(instanceID, OutcomeInvalid, strategymodel.TriggerID{}, err)
	}
	receipt.TriggerID, receipt.EvaluationID = triggerID, evaluationID
	if existing, lookupErr := runner.repository.Evaluation(ctx, evaluationID); lookupErr == nil {
		runner.record(instance.DefinitionID(), OutcomeDuplicateCommitted, 0, 0, 0,
			int(runner.inFlight.Load()))
		return Receipt{
			Outcome: OutcomeDuplicateCommitted, TriggerID: triggerID,
			EvaluationID: existing.EvaluationID(), StateRevision: existing.CheckpointRevision(),
			ProposalID: existing.ProposalID(),
		}, ErrDuplicateTrigger
	} else if !errors.Is(lookupErr, strategystorage.ErrNotFound) {
		return runner.failure(instanceID, OutcomeInternalFailure, triggerID, lookupErr)
	}
	if !instance.Evaluates() {
		return runner.failure(instanceID, OutcomeLifecycleIneligible, triggerID, ErrLifecycle)
	}
	evidence := runner.readiness.Evidence(ctx, instance, frame)
	if !evidence.Ready() {
		reasons := make([]string, len(evidence.Reasons))
		for index, reason := range evidence.Reasons {
			reasons[index] = string(reason)
		}
		receipt.Outcome, receipt.Reasons = OutcomeReadinessBlocked, reasons
		runner.record(instance.DefinitionID(), receipt.Outcome, 0, 0, 0, 0)
		return receipt, ErrReadinessBlocked
	}
	if outcome := runner.reserve(instanceID, triggerID); outcome != "" {
		receipt.Outcome = outcome
		runner.record(instance.DefinitionID(), outcome, 0, 0, 0,
			int(runner.inFlight.Load()))
		if outcome == OutcomeShutdown {
			return receipt, ErrRunnerShutdown
		}
		if outcome == OutcomeDuplicateInProgress {
			return receipt, ErrDuplicateTrigger
		}
		return receipt, ErrInstanceBusy
	}
	defer func() {
		runner.release(instanceID)
		runner.wait.Done()
	}()
	select {
	case runner.semaphore <- struct{}{}:
	case <-ctx.Done():
		return runner.failure(instanceID, OutcomeCancelled, triggerID, ctx.Err())
	case <-runner.ctx.Done():
		return runner.failure(instanceID, OutcomeShutdown, triggerID, ErrRunnerShutdown)
	}
	inFlight := int(runner.inFlight.Add(1))
	runner.record(instance.DefinitionID(), OutcomeStarted, 0, 0, 0, inFlight)
	defer func() {
		runner.inFlight.Add(-1)
		<-runner.semaphore
	}()

	current, err := runner.repository.CurrentCheckpoint(ctx, instanceID)
	if err != nil {
		return runner.failure(instanceID, OutcomeInternalFailure, triggerID, err)
	}
	evaluationCtx, cancel := context.WithTimeout(ctx, runner.config.Timeout)
	stop := context.AfterFunc(runner.ctx, cancel)
	defer func() {
		stop()
		cancel()
	}()
	input := strategymodel.EvaluationContext{
		DefinitionID: instance.DefinitionID(), VersionID: instance.VersionID(),
		InstanceID: instance.ID(), InstanceRevisionID: instance.RevisionID(),
		InstanceGeneration: instance.Generation(), Configuration: instance.Configuration(),
		EvaluationID: evaluationID, TriggerID: frame.TriggerID(), LogicalTime: frame.LogicalTime(),
		Frame: frame, PriorState: current.State(), Readiness: evidence,
		Entropy: deterministicEntropy(binary.BigEndian.Uint64(triggerID[:8])),
	}
	if err := input.Validate(definition.Descriptor()); err != nil {
		return runner.failure(instanceID, OutcomeInvalid, triggerID, err)
	}
	started := runner.clock.Now()
	result, evaluateErr, panicText := invokeStrategy(evaluationCtx, definition, input)
	elapsed := runner.clock.Now().Sub(started)
	if panicText != "" {
		receipt, err = runner.failure(instanceID, OutcomePanic, triggerID, ErrStrategyPanic)
		receipt.Diagnostic = panicText
		return receipt, err
	}
	if errors.Is(evaluationCtx.Err(), context.DeadlineExceeded) {
		runner.record(instance.DefinitionID(), OutcomeTimedOut, elapsed, 0, 0, inFlight)
		return Receipt{Outcome: OutcomeTimedOut, TriggerID: triggerID, EvaluationID: evaluationID},
			ErrStrategyTimeout
	}
	if errors.Is(evaluationCtx.Err(), context.Canceled) {
		if runner.ctx.Err() != nil {
			return runner.failure(instanceID, OutcomeShutdown, triggerID, ErrRunnerShutdown)
		}
		return runner.failure(instanceID, OutcomeCancelled, triggerID, evaluationCtx.Err())
	}
	if evaluateErr != nil || validateResult(definition.Descriptor(), result) != nil {
		return runner.failure(instanceID, OutcomeInvalid, triggerID, ErrInvalidOutput)
	}
	publication, proposalID, err := runner.publication(instance, frame, current, result, evaluationID)
	if err != nil {
		return runner.failure(instanceID, OutcomeInvalid, triggerID, err)
	}
	publishStarted := runner.clock.Now()
	outcome, err := runner.repository.PublishEvaluation(evaluationCtx, publication)
	publishDuration := runner.clock.Now().Sub(publishStarted)
	if err != nil {
		if errors.Is(evaluationCtx.Err(), context.DeadlineExceeded) {
			runner.record(instance.DefinitionID(), OutcomeTimedOut, elapsed, publishDuration, 0, inFlight)
			return Receipt{Outcome: OutcomeTimedOut, TriggerID: triggerID, EvaluationID: evaluationID},
				ErrStrategyTimeout
		}
		if errors.Is(evaluationCtx.Err(), context.Canceled) {
			if runner.ctx.Err() != nil {
				return runner.failure(instanceID, OutcomeShutdown, triggerID, ErrRunnerShutdown)
			}
			return runner.failure(instanceID, OutcomeCancelled, triggerID, evaluationCtx.Err())
		}
		if errors.Is(err, strategystorage.ErrRevisionConflict) {
			runner.record(instance.DefinitionID(), OutcomeRevisionConflict, elapsed, publishDuration, 0, inFlight)
			return Receipt{Outcome: OutcomeRevisionConflict, TriggerID: triggerID, EvaluationID: evaluationID},
				err
		}
		runner.record(instance.DefinitionID(), OutcomePublicationFailure, elapsed, publishDuration, 0, inFlight)
		return Receipt{Outcome: OutcomePublicationFailure, TriggerID: triggerID, EvaluationID: evaluationID},
			err
	}
	receipt = Receipt{
		TriggerID: triggerID, EvaluationID: evaluationID, ProposalID: proposalID,
		StateRevision: outcome.CheckpointRevision,
	}
	switch result.Kind() {
	case strategymodel.ResultNoAction:
		receipt.Outcome = OutcomeNoAction
	case strategymodel.ResultObservation:
		receipt.Outcome = OutcomeObservation
	case strategymodel.ResultTradeProposal:
		receipt.Outcome = OutcomeTradeProposal
	default:
		return runner.failure(instanceID, OutcomeInvalid, triggerID, ErrInvalidOutput)
	}
	runner.record(instance.DefinitionID(), receipt.Outcome, elapsed, publishDuration,
		result.NextState().Size(), inFlight)
	return receipt, nil
}

func (runner *Runner) publication(
	instance strategymodel.StrategyInstance,
	frame strategymodel.CandleFrame,
	current strategystorage.RuntimeCheckpoint,
	result strategymodel.EvaluationResult,
	evaluationID strategymodel.EvaluationID,
) (strategystorage.EvaluationPublication, strategymodel.ProposalID, error) {
	next, err := strategystorage.NewRuntimeCheckpoint(strategystorage.RuntimeCheckpointSpec{
		InstanceID: instance.ID(), DefinitionID: instance.DefinitionID(),
		VersionID: instance.VersionID(), InstanceRevisionID: instance.RevisionID(),
		ConfigurationHash: instance.Configuration().Hash(), Revision: current.Revision() + 1,
		ParentChecksum: current.Checksum(), EvaluationID: evaluationID, State: result.NextState(),
	})
	if err != nil {
		return strategystorage.EvaluationPublication{}, strategymodel.ProposalID{}, err
	}
	spec := strategymodel.EvaluationRecordSpec{
		EvaluationID: evaluationID, DefinitionID: instance.DefinitionID(),
		VersionID: instance.VersionID(), InstanceID: instance.ID(),
		InstanceRevisionID: instance.RevisionID(),
		ConfigurationHash:  instance.Configuration().Hash(), FrameID: frame.ID(),
		LogicalTime: frame.LogicalTime(), ResultKind: result.Kind(),
		PriorStateHash: current.State().Hash(), NextStateHash: result.NextState().Hash(),
		CheckpointRevision: next.Revision(),
	}
	var observation *strategymodel.StrategyObservation
	var proposal *strategymodel.TradeProposal
	var proposalID strategymodel.ProposalID
	switch result.Kind() {
	case strategymodel.ResultNoAction:
		value, ok := result.NoAction()
		if !ok {
			return strategystorage.EvaluationPublication{}, proposalID, ErrInvalidOutput
		}
		spec.NoActionReason = value.Reason
	case strategymodel.ResultObservation:
		draft, ok := result.Observation()
		if !ok {
			return strategystorage.EvaluationPublication{}, proposalID, ErrInvalidOutput
		}
		value, createErr := strategymodel.NewStrategyObservation(evaluationID, frame.LogicalTime(), draft)
		if createErr != nil {
			return strategystorage.EvaluationPublication{}, proposalID, createErr
		}
		observation, spec.ObservationCode = &value, draft.Code
	case strategymodel.ResultTradeProposal:
		draft, ok := result.Proposal()
		if !ok {
			return strategystorage.EvaluationPublication{}, proposalID, ErrInvalidOutput
		}
		required := requiredInstruments(frame.Subscription())
		value, createErr := strategymodel.NewTradeProposal(strategymodel.ProposalMetadata{
			DefinitionID: instance.DefinitionID(), VersionID: instance.VersionID(),
			InstanceID: instance.ID(), InstanceRevisionID: instance.RevisionID(),
			EvaluationID: evaluationID, FrameID: frame.ID(), GeneratedAt: frame.LogicalTime(),
			SourceEventIDs: frame.SourceEventIDs(), RequiredInstrumentIDs: required,
		}, draft)
		if createErr != nil {
			return strategystorage.EvaluationPublication{}, proposalID, createErr
		}
		proposal, proposalID, spec.ProposalID = &value, value.ID(), value.ID()
	default:
		return strategystorage.EvaluationPublication{}, proposalID, ErrInvalidOutput
	}
	record, err := strategymodel.NewEvaluationRecord(spec)
	if err != nil {
		return strategystorage.EvaluationPublication{}, proposalID, err
	}
	publication, err := strategystorage.NewEvaluationPublication(strategystorage.EvaluationPublication{
		InstanceID: instance.ID(), DefinitionID: instance.DefinitionID(),
		VersionID: instance.VersionID(), InstanceRevisionID: instance.RevisionID(),
		ConfigurationHash: instance.Configuration().Hash(), EvaluationID: evaluationID,
		FrameID: frame.ID(), ExpectedStateRevision: current.Revision(),
		Checkpoint: next, Record: record, Observation: observation, Proposal: proposal,
	})
	return publication, proposalID, err
}

func deriveIdentities(
	instance strategymodel.StrategyInstance,
	frame strategymodel.CandleFrame,
) (strategymodel.TriggerID, strategymodel.EvaluationID, error) {
	key := fmt.Sprintf("%s|%s|%s|%s|%s", triggerSchema, instance.RevisionID(),
		instance.VersionID(), instance.Configuration().Hash(), frame.ID())
	trigger, err := strategymodel.NewTriggerID(key)
	if err != nil {
		return strategymodel.TriggerID{}, strategymodel.EvaluationID{}, err
	}
	evaluation, err := strategymodel.NewEvaluationID("strategy-evaluation/v1|" + trigger.String())
	return trigger, evaluation, err
}

func validateResult(
	descriptor strategymodel.Descriptor,
	result strategymodel.EvaluationResult,
) error {
	if result.NextState().IsZero() ||
		result.NextState().SchemaVersion() != descriptor.Manifest.StateSchemaVersion {
		return ErrInvalidOutput
	}
	switch result.Kind() {
	case strategymodel.ResultNoAction:
		_, ok := result.NoAction()
		if !ok {
			return ErrInvalidOutput
		}
	case strategymodel.ResultObservation:
		_, ok := result.Observation()
		if !ok {
			return ErrInvalidOutput
		}
	case strategymodel.ResultTradeProposal:
		value, ok := result.Proposal()
		if !ok || value.SchemaVersion != descriptor.Manifest.ProposalSchemaVersion {
			return ErrInvalidOutput
		}
	default:
		return ErrInvalidOutput
	}
	return nil
}

func requiredInstruments(spec strategymodel.SubscriptionSpec) []domain.InstrumentID {
	seen := make(map[domain.InstrumentID]struct{})
	var result []domain.InstrumentID
	for _, subscription := range spec.Subscriptions() {
		if subscription.Required {
			if _, found := seen[subscription.InstrumentID]; !found {
				seen[subscription.InstrumentID] = struct{}{}
				result = append(result, subscription.InstrumentID)
			}
		}
	}
	return result
}

func invokeStrategy(
	ctx context.Context,
	definition interface {
		Evaluate(context.Context, strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error)
	},
	input strategymodel.EvaluationContext,
) (result strategymodel.EvaluationResult, err error, diagnostic string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			value := fmt.Sprint(recovered)
			if len(value) > 256 {
				value = value[:256]
			}
			stack := debug.Stack()
			if len(stack) > 8192 {
				stack = stack[:8192]
			}
			diagnostic = value + "\n" + string(stack)
		}
	}()
	result, err = definition.Evaluate(ctx, input)
	return
}

func (runner *Runner) reserve(instanceID domain.StrategyID, trigger strategymodel.TriggerID) Outcome {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.closed {
		return OutcomeShutdown
	}
	if current, found := runner.running[instanceID]; found {
		if current == trigger {
			return OutcomeDuplicateInProgress
		}
		return OutcomeInstanceBusy
	}
	runner.running[instanceID] = trigger
	runner.wait.Add(1)
	return ""
}

func (runner *Runner) release(instanceID domain.StrategyID) {
	runner.mu.Lock()
	delete(runner.running, instanceID)
	runner.mu.Unlock()
}

func (runner *Runner) failure(
	instanceID domain.StrategyID,
	outcome Outcome,
	trigger strategymodel.TriggerID,
	err error,
) (Receipt, error) {
	diagnostic := ""
	if err != nil {
		diagnostic = err.Error()
	}
	if len(diagnostic) > 512 {
		diagnostic = diagnostic[:512]
	}
	runner.mu.Lock()
	runner.failures = append(runner.failures, Failure{
		At: runner.clock.Now(), InstanceID: instanceID, Outcome: outcome, Diagnostic: diagnostic,
	})
	if len(runner.failures) > 100 {
		runner.failures = append([]Failure(nil), runner.failures[len(runner.failures)-100:]...)
	}
	runner.mu.Unlock()
	runner.record("", outcome, 0, 0, 0, int(runner.inFlight.Load()))
	return Receipt{Outcome: outcome, TriggerID: trigger, Diagnostic: diagnostic}, err
}

func (runner *Runner) record(
	definition strategymodel.DefinitionID,
	outcome Outcome,
	duration, publish time.Duration,
	stateBytes int,
	inFlight int,
) {
	runner.telemetry.Record(strategytelemetry.Event{
		Definition: definition, Outcome: string(outcome), Duration: duration,
		Publish: publish, StateBytes: stateBytes, InFlight: inFlight,
	})
}

func (runner *Runner) Shutdown(ctx context.Context) error {
	runner.mu.Lock()
	if !runner.closed {
		runner.closed = true
		runner.cancel()
	}
	runner.mu.Unlock()
	runner.shutdown.Do(func() {
		go func() {
			runner.wait.Wait()
			close(runner.done)
		}()
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-runner.done:
		return nil
	}
}

func (runner *Runner) Health() (closed bool, inFlight int, keyed int, failures []Failure) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.closed, int(runner.inFlight.Load()), len(runner.running),
		append([]Failure(nil), runner.failures...)
}

type deterministicEntropy uint64

func (value deterministicEntropy) Uint64() uint64 { return uint64(value) }
