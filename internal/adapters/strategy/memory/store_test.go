package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
	strategystorage "github.com/bibhuyash/tradeedge/internal/strategy/storage"
)

func TestDefinitionVersionAndInstanceRegistration(t *testing.T) {
	t.Parallel()
	store := NewStore()
	fixture := newFixtureValues(t, "registry")
	ctx := context.Background()
	outcome, err := store.RegisterDefinition(ctx, fixture.definition)
	if err != nil || outcome.Status != strategystorage.RegistrationCommitted {
		t.Fatalf("RegisterDefinition() = %#v, %v", outcome, err)
	}
	outcome, err = store.RegisterDefinition(ctx, fixture.definition)
	if err != nil || outcome.Status != strategystorage.RegistrationIdempotent {
		t.Fatalf("duplicate RegisterDefinition() = %#v, %v", outcome, err)
	}
	conflict, _ := strategystorage.NewDefinitionRecord(
		fixture.definition.ID, "definition/v1", "Different name", "different content",
	)
	if _, err := store.RegisterDefinition(ctx, conflict); !errors.Is(err, strategystorage.ErrIdentityCollision) {
		t.Fatalf("definition collision error = %v", err)
	}
	outcome, err = store.RegisterVersion(ctx, fixture.descriptor)
	if err != nil || outcome.Status != strategystorage.RegistrationCommitted {
		t.Fatalf("RegisterVersion() = %#v, %v", outcome, err)
	}
	outcome, err = store.RegisterVersion(ctx, fixture.descriptor)
	if err != nil || outcome.Status != strategystorage.RegistrationIdempotent {
		t.Fatalf("duplicate RegisterVersion() = %#v, %v", outcome, err)
	}
	outcome, err = store.PutInstance(ctx, strategymodel.InstanceRevisionID{}, fixture.instance)
	if err != nil || outcome.Status != strategystorage.RegistrationCommitted {
		t.Fatalf("PutInstance() = %#v, %v", outcome, err)
	}
	outcome, err = store.PutInstance(ctx, strategymodel.InstanceRevisionID{}, fixture.instance)
	if err != nil || outcome.Status != strategystorage.RegistrationIdempotent {
		t.Fatalf("duplicate PutInstance() = %#v, %v", outcome, err)
	}
}

func TestLifecycleRevisionProgression(t *testing.T) {
	t.Parallel()
	store, fixture := initializedFixture(t, "lifecycle")
	next, err := strategymodel.NewStrategyInstance(
		fixture.instance.ID(), fixture.descriptor, fixture.configuration, 2,
		strategymodel.LifecycleProbation,
	)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := store.PutInstance(
		context.Background(), fixture.instance.RevisionID(), next,
	)
	if err != nil || outcome.Status != strategystorage.RegistrationCommitted {
		t.Fatalf("PutInstance(next) = %#v, %v", outcome, err)
	}
	if _, err := store.PutInstance(
		context.Background(), fixture.instance.RevisionID(), next,
	); err != nil {
		t.Fatalf("exact lifecycle retry should be idempotent: %v", err)
	}
	stale, err := strategymodel.NewStrategyInstance(
		fixture.instance.ID(), fixture.descriptor, fixture.configuration, 3,
		strategymodel.LifecycleActive,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutInstance(
		context.Background(), fixture.instance.RevisionID(), stale,
	); !errors.Is(err, strategystorage.ErrRevisionConflict) {
		t.Fatalf("stale lifecycle revision error = %v", err)
	}
}

func TestInitialCheckpointPublicationAndRestoration(t *testing.T) {
	t.Parallel()
	store, fixture := initializedFixture(t, "initial")
	checkpoint, err := store.CurrentCheckpoint(context.Background(), fixture.instance.ID())
	if err != nil || checkpoint.Revision() != 0 {
		t.Fatalf("CurrentCheckpoint() = %#v, %v", checkpoint, err)
	}
	outcome, err := store.InitializeCheckpoint(context.Background(), fixture.root)
	if err != nil || outcome.Status != strategystorage.RegistrationIdempotent {
		t.Fatalf("duplicate InitializeCheckpoint() = %#v, %v", outcome, err)
	}
	encoded, err := strategystorage.EncodeCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := store.Restore(context.Background(), encoded, fixture.expectation(0))
	if err != nil || restored.Checksum() != checkpoint.Checksum() {
		t.Fatalf("Restore() = %#v, %v", restored, err)
	}
}

func TestAtomicPublicationVariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind strategymodel.ResultKind
	}{
		{"no action", strategymodel.ResultNoAction},
		{"observation", strategymodel.ResultObservation},
		{"proposal", strategymodel.ResultTradeProposal},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store, fixture := initializedFixture(t, "variant-"+test.name)
			publication := fixture.publication(t, fixture.root, test.kind, "one", 1)
			outcome, err := store.PublishEvaluation(context.Background(), publication)
			if err != nil || outcome.Status != strategystorage.PublicationCommitted ||
				outcome.CheckpointRevision != 1 {
				t.Fatalf("PublishEvaluation() = %#v, %v", outcome, err)
			}
			if _, err := store.Evaluation(context.Background(), publication.EvaluationID); err != nil {
				t.Fatalf("evaluation not visible: %v", err)
			}
			current, err := store.CurrentCheckpoint(context.Background(), fixture.instance.ID())
			if err != nil || current.Revision() != 1 {
				t.Fatalf("checkpoint not visible: %#v, %v", current, err)
			}
			switch test.kind {
			case strategymodel.ResultObservation:
				if _, err := store.Observation(context.Background(), publication.EvaluationID); err != nil {
					t.Fatalf("observation not visible: %v", err)
				}
			case strategymodel.ResultTradeProposal:
				if _, err := store.Proposal(
					context.Background(), publication.Proposal.ID(),
				); err != nil {
					t.Fatalf("proposal not visible: %v", err)
				}
			}
		})
	}
}

func TestExactRetryAndIdentityCollision(t *testing.T) {
	t.Parallel()
	store, fixture := initializedFixture(t, "retry")
	publication := fixture.publication(
		t, fixture.root, strategymodel.ResultNoAction, "same-evaluation", 1,
	)
	first, err := store.PublishEvaluation(context.Background(), publication)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PublishEvaluation(context.Background(), publication)
	if err != nil || second.Status != strategystorage.PublicationIdempotent ||
		second.PublicationChecksum != first.PublicationChecksum {
		t.Fatalf("idempotent retry = %#v, %v", second, err)
	}
	changed := fixture.publication(
		t, fixture.root, strategymodel.ResultNoAction, "same-evaluation", 999,
	)
	outcome, err := store.PublishEvaluation(context.Background(), changed)
	if !errors.Is(err, strategystorage.ErrIdentityCollision) ||
		outcome.Status != strategystorage.PublicationIdentityCollision {
		t.Fatalf("changed identity outcome = %#v, %v", outcome, err)
	}
}

func TestStaleAndConcurrentPublication(t *testing.T) {
	t.Parallel()
	store, fixture := initializedFixture(t, "concurrent")
	left := fixture.publication(t, fixture.root, strategymodel.ResultNoAction, "left", 1)
	right := fixture.publication(t, fixture.root, strategymodel.ResultNoAction, "right", 2)
	start := make(chan struct{})
	results := make(chan strategystorage.PublicationOutcome, 2)
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, publication := range []strategystorage.EvaluationPublication{left, right} {
		publication := publication
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			outcome, err := store.PublishEvaluation(context.Background(), publication)
			results <- outcome
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errs)
	committed, conflicts := 0, 0
	for outcome := range results {
		switch outcome.Status {
		case strategystorage.PublicationCommitted:
			committed++
		case strategystorage.PublicationRevisionConflict:
			conflicts++
		}
	}
	for err := range errs {
		if err != nil && !errors.Is(err, strategystorage.ErrRevisionConflict) {
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if committed != 1 || conflicts != 1 {
		t.Fatalf("committed=%d conflicts=%d", committed, conflicts)
	}
	evaluations, _ := store.Evaluations(context.Background(), fixture.instance.ID())
	checkpoints, _ := store.Checkpoints(context.Background(), fixture.instance.ID())
	if len(evaluations) != 1 || len(checkpoints) != 2 {
		t.Fatalf("partial or duplicate commit: evaluations=%d checkpoints=%d",
			len(evaluations), len(checkpoints))
	}
}

func TestInjectedFailurePublishesNothing(t *testing.T) {
	t.Parallel()
	tests := []strategymodel.ResultKind{
		strategymodel.ResultNoAction,
		strategymodel.ResultObservation,
		strategymodel.ResultTradeProposal,
	}
	for _, kind := range tests {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			store, fixture := initializedFixture(t, "failure-"+string(kind))
			publication := fixture.publication(t, fixture.root, kind, "failed", 1)
			store.SetFailureInjector(func(FailurePoint) error { return errors.New("injected") })
			outcome, err := store.PublishEvaluation(context.Background(), publication)
			if !errors.Is(err, strategystorage.ErrStorageFailure) ||
				outcome.Status != strategystorage.PublicationInternalFailure {
				t.Fatalf("PublishEvaluation() = %#v, %v", outcome, err)
			}
			current, _ := store.CurrentCheckpoint(context.Background(), fixture.instance.ID())
			if current.Revision() != 0 {
				t.Fatal("checkpoint changed after failed commit")
			}
			if _, err := store.Evaluation(
				context.Background(), publication.EvaluationID,
			); !errors.Is(err, strategystorage.ErrNotFound) {
				t.Fatalf("evaluation became visible: %v", err)
			}
			if publication.Observation != nil {
				if _, err := store.Observation(
					context.Background(), publication.EvaluationID,
				); !errors.Is(err, strategystorage.ErrNotFound) {
					t.Fatalf("observation became visible: %v", err)
				}
			}
			if publication.Proposal != nil {
				if _, err := store.Proposal(
					context.Background(), publication.Proposal.ID(),
				); !errors.Is(err, strategystorage.ErrNotFound) {
					t.Fatalf("proposal became visible: %v", err)
				}
			}
		})
	}
}

func TestCancellationBeforeCommitPublishesNothing(t *testing.T) {
	t.Parallel()
	store, fixture := initializedFixture(t, "cancel")
	publication := fixture.publication(
		t, fixture.root, strategymodel.ResultNoAction, "cancelled", 1,
	)
	ctx, cancel := context.WithCancel(context.Background())
	store.SetFailureInjector(func(FailurePoint) error {
		cancel()
		return nil
	})
	outcome, err := store.PublishEvaluation(ctx, publication)
	if !errors.Is(err, context.Canceled) ||
		outcome.Status != strategystorage.PublicationCancelled {
		t.Fatalf("PublishEvaluation() = %#v, %v", outcome, err)
	}
	current, _ := store.CurrentCheckpoint(context.Background(), fixture.instance.ID())
	if current.Revision() != 0 {
		t.Fatal("cancelled publication changed checkpoint")
	}
}

func TestCorruptPriorStateFailsClosed(t *testing.T) {
	t.Parallel()
	store, fixture := initializedFixture(t, "corrupt")
	store.CorruptCurrentCheckpointForTest(fixture.instance.ID())
	publication := fixture.publication(
		t, fixture.root, strategymodel.ResultNoAction, "blocked", 1,
	)
	outcome, err := store.PublishEvaluation(context.Background(), publication)
	if !errors.Is(err, strategystorage.ErrCorruptCheckpoint) ||
		outcome.Status != strategystorage.PublicationCorruptPriorState {
		t.Fatalf("PublishEvaluation() = %#v, %v", outcome, err)
	}
}

func TestRestorationRejectsBrokenParentLineage(t *testing.T) {
	t.Parallel()
	store, fixture := initializedFixture(t, "lineage")
	publication := fixture.publication(
		t, fixture.root, strategymodel.ResultNoAction, "valid", 1,
	)
	if _, err := store.PublishEvaluation(context.Background(), publication); err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := store.CurrentCheckpoint(context.Background(), fixture.instance.ID())
	encoded, _ := strategystorage.EncodeCheckpoint(checkpoint)
	expectation := fixture.expectation(1)
	if _, err := store.Restore(context.Background(), encoded, expectation); err != nil {
		t.Fatalf("valid restore: %v", err)
	}
	wrongParent := strategystorage.CheckpointChecksum{1}
	broken, err := strategystorage.NewRuntimeCheckpoint(strategystorage.RuntimeCheckpointSpec{
		InstanceID: checkpoint.InstanceID(), DefinitionID: checkpoint.DefinitionID(),
		VersionID: checkpoint.VersionID(), InstanceRevisionID: checkpoint.InstanceRevisionID(),
		ConfigurationHash: checkpoint.ConfigurationHash(), Revision: checkpoint.Revision(),
		ParentChecksum: wrongParent, EvaluationID: checkpoint.EvaluationID(),
		State: checkpoint.State(),
	})
	if err != nil {
		t.Fatal(err)
	}
	brokenBytes, _ := strategystorage.EncodeCheckpoint(broken)
	if _, err := store.Restore(
		context.Background(), brokenBytes, expectation,
	); !errors.Is(err, strategystorage.ErrCorruptCheckpoint) {
		t.Fatalf("broken lineage error = %v", err)
	}
}

func TestDeterministicQueriesAndReturnedMutationSafety(t *testing.T) {
	t.Parallel()
	store, fixture := initializedFixture(t, "queries")
	current := fixture.root
	for revision := 1; revision <= 3; revision++ {
		publication := fixture.publication(
			t, current, strategymodel.ResultObservation,
			fmt.Sprintf("evaluation-%d", 4-revision), int64(revision),
		)
		if _, err := store.PublishEvaluation(context.Background(), publication); err != nil {
			t.Fatal(err)
		}
		current = publication.Checkpoint
	}
	evaluations, _ := store.Evaluations(context.Background(), fixture.instance.ID())
	for index, record := range evaluations {
		if record.CheckpointRevision() != uint64(index+1) {
			t.Fatalf("non-deterministic evaluation order: %#v", evaluations)
		}
	}
	checkpoints, _ := store.Checkpoints(context.Background(), fixture.instance.ID())
	mutated := checkpoints[0].State().CanonicalJSON()
	mutated[0] = 'X'
	currentStored, _ := store.Checkpoint(context.Background(), fixture.instance.ID(), 0)
	if currentStored.State().CanonicalJSON()[0] == 'X' {
		t.Fatal("returned state bytes mutated repository")
	}
	observations, _ := store.Observations(context.Background(), fixture.instance.ID())
	draft := observations[0].Draft()
	draft.Evidence[0].SourceEventIDs[0] = marketmodel.EventID{}
	again, _ := store.Observation(context.Background(), observations[0].EvaluationID())
	if again.Draft().Evidence[0].SourceEventIDs[0].IsZero() {
		t.Fatal("returned observation mutated repository")
	}
}

func TestConcurrentReadsDuringRevisionProgression(t *testing.T) {
	store, fixture := initializedFixture(t, "race-heavy")
	ctx := context.Background()
	var stop atomic.Bool
	var readers sync.WaitGroup
	for reader := 0; reader < 12; reader++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for !stop.Load() {
				_, _ = store.CurrentCheckpoint(ctx, fixture.instance.ID())
				_, _ = store.Evaluations(ctx, fixture.instance.ID())
				_, _ = store.Instances(ctx)
			}
		}()
	}
	current := fixture.root
	for revision := 1; revision <= 50; revision++ {
		publication := fixture.publication(
			t, current, strategymodel.ResultNoAction,
			fmt.Sprintf("race-%03d", revision), int64(revision),
		)
		if _, err := store.PublishEvaluation(ctx, publication); err != nil {
			t.Fatal(err)
		}
		current = publication.Checkpoint
	}
	stop.Store(true)
	readers.Wait()
	checkpoint, err := store.CurrentCheckpoint(ctx, fixture.instance.ID())
	if err != nil || checkpoint.Revision() != 50 {
		t.Fatalf("final checkpoint = %#v, %v", checkpoint, err)
	}
}

func TestRepositoryErrorClassification(t *testing.T) {
	t.Parallel()
	revision := &strategystorage.RevisionConflictError{
		InstanceID: "instance", Expected: 1, Actual: 2,
	}
	collision := &strategystorage.IdentityCollisionError{Kind: "evaluation", Identity: "id"}
	if !errors.Is(revision, strategystorage.ErrRevisionConflict) ||
		!errors.Is(collision, strategystorage.ErrIdentityCollision) ||
		errors.Is(revision, strategystorage.ErrIdentityCollision) {
		t.Fatal("repository typed error classification failed")
	}
}

func TestConfiguredCapacityFailsClosed(t *testing.T) {
	t.Parallel()
	store := NewStoreWithLimits(Limits{
		Definitions: 1, Versions: 1, Instances: 1,
		Evaluations: 1, Observations: 1, Proposals: 1,
	})
	first := newFixtureValues(t, "capacity-one")
	second := newFixtureValues(t, "capacity-two")
	if _, err := store.RegisterDefinition(context.Background(), first.definition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterDefinition(
		context.Background(), second.definition,
	); !errors.Is(err, strategystorage.ErrStorageFailure) {
		t.Fatalf("capacity error = %v", err)
	}
	definitions, err := store.Definitions(context.Background())
	if err != nil || len(definitions) != 1 || definitions[0].ID != first.definition.ID {
		t.Fatalf("capacity failure mutated store: %#v, %v", definitions, err)
	}
}

type fixtureValues struct {
	definition    strategystorage.DefinitionRecord
	descriptor    strategymodel.Descriptor
	configuration strategymodel.StrategyConfiguration
	instance      strategymodel.StrategyInstance
	root          strategystorage.RuntimeCheckpoint
	instrumentID  domain.InstrumentID
	eventID       marketmodel.EventID
}

func newFixtureValues(t *testing.T, suffix string) fixtureValues {
	t.Helper()
	definitionID, _ := strategymodel.NewDefinitionID("fixture-" + sanitizeSuffix(suffix))
	instrumentID, _ := domain.InstrumentIDFromCanonicalKey("NSE|INDEX|" + suffix)
	subscriptions, err := strategymodel.NewSubscriptionSpec(
		strategymodel.SubscriptionSingleStream,
		[]strategymodel.InputSubscription{{
			Role: "primary", InstrumentID: instrumentID, Interval: marketmodel.Interval1Minute,
			Required: true, Trigger: true, Lookback: 1, MaximumAge: time.Minute,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := strategymodel.NewDescriptor(strategymodel.VersionManifest{
		DefinitionID: definitionID, ImplementationVersion: "fixture/v1",
		InputContractVersion: "frame/v1", ConfigurationSchemaVersion: "config/v1",
		StateSchemaVersion: "state/v1", ResultSchemaVersion: "result/v1",
		ProposalSchemaVersion: "proposal/v1",
	}, subscriptions)
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := strategystorage.NewDefinitionRecord(
		definitionID, "definition/v1", "Fixture "+suffix, "deterministic repository fixture",
	)
	configuration, _ := strategymodel.NewStrategyConfiguration(
		"config/v1", []byte(`{"period":5}`),
	)
	instanceID, _ := domain.NewStrategyID("instance-" + suffix)
	instance, err := strategymodel.NewStrategyInstance(
		instanceID, descriptor, configuration, 1, strategymodel.LifecycleCandidate,
	)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := strategymodel.NewStrategyRuntimeState("state/v1", []byte(`{"count":0}`))
	root, err := strategystorage.NewRuntimeCheckpoint(strategystorage.RuntimeCheckpointSpec{
		InstanceID: instance.ID(), DefinitionID: definitionID, VersionID: descriptor.VersionID,
		InstanceRevisionID: instance.RevisionID(), ConfigurationHash: configuration.Hash(),
		State: state,
	})
	if err != nil {
		t.Fatal(err)
	}
	eventID, _ := marketmodel.NewEventID("event-" + suffix)
	return fixtureValues{
		definition: definition, descriptor: descriptor, configuration: configuration,
		instance: instance, root: root, instrumentID: instrumentID, eventID: eventID,
	}
}

func initializedFixture(t *testing.T, suffix string) (*Store, fixtureValues) {
	t.Helper()
	store := NewStore()
	fixture := newFixtureValues(t, suffix)
	ctx := context.Background()
	if _, err := store.RegisterDefinition(ctx, fixture.definition); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterVersion(ctx, fixture.descriptor); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutInstance(
		ctx, strategymodel.InstanceRevisionID{}, fixture.instance,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InitializeCheckpoint(ctx, fixture.root); err != nil {
		t.Fatal(err)
	}
	return store, fixture
}

func (fixture fixtureValues) publication(
	t *testing.T,
	parent strategystorage.RuntimeCheckpoint,
	kind strategymodel.ResultKind,
	key string,
	count int64,
) strategystorage.EvaluationPublication {
	t.Helper()
	evaluationID, _ := strategymodel.NewEvaluationID(key)
	frameID, _ := strategymodel.NewFrameID("frame-" + key)
	state, _ := strategymodel.NewStrategyRuntimeState(
		"state/v1", []byte(fmt.Sprintf(`{"count":%d}`, count)),
	)
	checkpoint, err := strategystorage.NewRuntimeCheckpoint(strategystorage.RuntimeCheckpointSpec{
		InstanceID: fixture.instance.ID(), DefinitionID: fixture.instance.DefinitionID(),
		VersionID: fixture.instance.VersionID(), InstanceRevisionID: fixture.instance.RevisionID(),
		ConfigurationHash: fixture.configuration.Hash(), Revision: parent.Revision() + 1,
		ParentChecksum: parent.Checksum(), EvaluationID: evaluationID, State: state,
	})
	if err != nil {
		t.Fatal(err)
	}
	recordSpec := strategymodel.EvaluationRecordSpec{
		EvaluationID: evaluationID, DefinitionID: fixture.instance.DefinitionID(),
		VersionID: fixture.instance.VersionID(), InstanceID: fixture.instance.ID(),
		InstanceRevisionID: fixture.instance.RevisionID(),
		ConfigurationHash:  fixture.configuration.Hash(), FrameID: frameID,
		LogicalTime: time.Date(2026, 7, 18, 4, int(parent.Revision())+1, 0, 0, time.UTC),
		ResultKind:  kind, PriorStateHash: parent.State().Hash(), NextStateHash: state.Hash(),
		CheckpointRevision: checkpoint.Revision(),
	}
	var observation *strategymodel.StrategyObservation
	var proposal *strategymodel.TradeProposal
	switch kind {
	case strategymodel.ResultNoAction:
		recordSpec.NoActionReason = strategymodel.NoActionConditionsNotMet
	case strategymodel.ResultObservation:
		recordSpec.ObservationCode = "FIXTURE_OBSERVATION"
		value, observationErr := strategymodel.NewStrategyObservation(
			evaluationID, recordSpec.LogicalTime,
			strategymodel.ObservationDraft{
				Code: recordSpec.ObservationCode, Explanation: "fixture observation",
				Evidence: []strategymodel.Evidence{{
					Code:           "FIXTURE_EVIDENCE",
					SourceEventIDs: []marketmodel.EventID{fixture.eventID},
					Value:          count, Unit: "COUNT", Explanation: "fixture evidence",
				}},
			},
		)
		if observationErr != nil {
			t.Fatal(observationErr)
		}
		observation = &value
	case strategymodel.ResultTradeProposal:
		price, _ := domain.NewPrice(10000+count, "INR")
		draft := strategymodel.ProposalDraft{
			SchemaVersion: "proposal/v1",
			Legs: []strategymodel.ProposalLeg{{
				InstrumentID: fixture.instrumentID, Side: domain.SideBuy, Ratio: 1,
				ReferencePrice: price, MaxDeviationBPS: 100,
			}},
			Sizing: strategymodel.SizingIntent{
				Kind: strategymodel.SizingStrategyBudgetBPS, ValueBPS: 500,
			},
			ValidFrom: recordSpec.LogicalTime, ExpiresAt: recordSpec.LogicalTime.Add(time.Minute),
			RationaleCode: "FIXTURE_PROPOSAL", Explanation: "fixture only",
			Evidence: []strategymodel.Evidence{{
				Code:           "FIXTURE_EVIDENCE",
				SourceEventIDs: []marketmodel.EventID{fixture.eventID},
				Value:          count, Unit: "COUNT", Explanation: "fixture evidence",
			}},
			ExitPolicyReference: "fixture-exit/v1",
		}
		value, proposalErr := strategymodel.NewTradeProposal(strategymodel.ProposalMetadata{
			DefinitionID: fixture.instance.DefinitionID(),
			VersionID:    fixture.instance.VersionID(), InstanceID: fixture.instance.ID(),
			InstanceRevisionID: fixture.instance.RevisionID(), EvaluationID: evaluationID,
			FrameID: frameID, GeneratedAt: recordSpec.LogicalTime,
			SourceEventIDs:        []marketmodel.EventID{fixture.eventID},
			RequiredInstrumentIDs: []domain.InstrumentID{fixture.instrumentID},
		}, draft)
		if proposalErr != nil {
			t.Fatal(proposalErr)
		}
		proposal = &value
		recordSpec.ProposalID = value.ID()
	default:
		t.Fatalf("unsupported kind %s", kind)
	}
	record, err := strategymodel.NewEvaluationRecord(recordSpec)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := strategystorage.NewEvaluationPublication(
		strategystorage.EvaluationPublication{
			InstanceID: fixture.instance.ID(), DefinitionID: fixture.instance.DefinitionID(),
			VersionID:          fixture.instance.VersionID(),
			InstanceRevisionID: fixture.instance.RevisionID(),
			ConfigurationHash:  fixture.configuration.Hash(), EvaluationID: evaluationID,
			FrameID: frameID, ExpectedStateRevision: parent.Revision(),
			Checkpoint: checkpoint, Record: record, Observation: observation, Proposal: proposal,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return publication
}

func (fixture fixtureValues) expectation(revision uint64) strategystorage.RestoreExpectation {
	return strategystorage.RestoreExpectation{
		InstanceID: fixture.instance.ID(), DefinitionID: fixture.instance.DefinitionID(),
		VersionID:          fixture.instance.VersionID(),
		ConfigurationHash:  fixture.configuration.Hash(),
		InstanceRevisionID: fixture.instance.RevisionID(),
		StateSchemaVersion: "state/v1", Revision: revision,
	}
}

func sanitizeSuffix(value string) string {
	result := ""
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '-' {
			result += string(character)
		} else {
			result += "-"
		}
	}
	return result
}
