package memory

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/bibhuyash/tradeedge/internal/domain"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
	strategystorage "github.com/bibhuyash/tradeedge/internal/strategy/storage"
)

type FailurePoint string

const FailureBeforeCommit FailurePoint = "BEFORE_COMMIT"

type FailureInjector func(FailurePoint) error

type Limits struct {
	Definitions  int
	Versions     int
	Instances    int
	Evaluations  int
	Observations int
	Proposals    int
}

func DefaultLimits() Limits {
	return Limits{
		Definitions: 64, Versions: 256, Instances: 256,
		Evaluations: 100000, Observations: 100000, Proposals: 100000,
	}
}

type storedDefinition struct {
	value     strategystorage.DefinitionRecord
	canonical []byte
}

type storedVersion struct {
	value     strategymodel.Descriptor
	canonical []byte
}

type storedInstance struct {
	value     strategymodel.StrategyInstance
	canonical []byte
}

type storedPublication struct {
	checksum  strategystorage.PublicationChecksum
	outcome   strategystorage.PublicationOutcome
	canonical []byte
}

type snapshot struct {
	definitions  map[strategymodel.DefinitionID]storedDefinition
	versions     map[strategymodel.VersionID]storedVersion
	instances    map[domain.StrategyID]storedInstance
	checkpoints  map[domain.StrategyID][]strategystorage.RuntimeCheckpoint
	evaluations  map[strategymodel.EvaluationID]strategymodel.EvaluationRecord
	observations map[strategymodel.EvaluationID]strategymodel.StrategyObservation
	proposals    map[strategymodel.ProposalID]strategymodel.TradeProposal
	publications map[strategymodel.EvaluationID]storedPublication
	corrupted    map[domain.StrategyID]bool
}

type Store struct {
	mu       sync.RWMutex
	current  *snapshot
	injector FailureInjector
	limits   Limits
}

func NewStore() *Store {
	return NewStoreWithLimits(DefaultLimits())
}

func NewStoreWithLimits(limits Limits) *Store {
	if limits.Definitions <= 0 || limits.Versions <= 0 || limits.Instances <= 0 ||
		limits.Evaluations <= 0 || limits.Observations <= 0 || limits.Proposals <= 0 {
		limits = DefaultLimits()
	}
	return &Store{current: newSnapshot(), limits: limits}
}

func newSnapshot() *snapshot {
	return &snapshot{
		definitions:  make(map[strategymodel.DefinitionID]storedDefinition),
		versions:     make(map[strategymodel.VersionID]storedVersion),
		instances:    make(map[domain.StrategyID]storedInstance),
		checkpoints:  make(map[domain.StrategyID][]strategystorage.RuntimeCheckpoint),
		evaluations:  make(map[strategymodel.EvaluationID]strategymodel.EvaluationRecord),
		observations: make(map[strategymodel.EvaluationID]strategymodel.StrategyObservation),
		proposals:    make(map[strategymodel.ProposalID]strategymodel.TradeProposal),
		publications: make(map[strategymodel.EvaluationID]storedPublication),
		corrupted:    make(map[domain.StrategyID]bool),
	}
}

func cloneSnapshot(source *snapshot) *snapshot {
	result := newSnapshot()
	for key, value := range source.definitions {
		value.canonical = append([]byte(nil), value.canonical...)
		result.definitions[key] = value
	}
	for key, value := range source.versions {
		value.canonical = append([]byte(nil), value.canonical...)
		result.versions[key] = value
	}
	for key, value := range source.instances {
		value.canonical = append([]byte(nil), value.canonical...)
		result.instances[key] = value
	}
	for key, values := range source.checkpoints {
		result.checkpoints[key] = append([]strategystorage.RuntimeCheckpoint(nil), values...)
	}
	for key, value := range source.evaluations {
		result.evaluations[key] = value
	}
	for key, value := range source.observations {
		result.observations[key] = value
	}
	for key, value := range source.proposals {
		result.proposals[key] = value
	}
	for key, value := range source.publications {
		value.canonical = append([]byte(nil), value.canonical...)
		result.publications[key] = value
	}
	for key, value := range source.corrupted {
		result.corrupted[key] = value
	}
	return result
}

// SetFailureInjector installs a test-only callback invoked immediately before
// the immutable snapshot swap. Production composition must leave it nil.
func (store *Store) SetFailureInjector(injector FailureInjector) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.injector = injector
}

// CorruptCurrentCheckpointForTest marks an instance's prior checkpoint as
// corrupt without exposing mutable checkpoint fields.
func (store *Store) CorruptCurrentCheckpointForTest(instanceID domain.StrategyID) {
	store.mu.Lock()
	defer store.mu.Unlock()
	next := cloneSnapshot(store.current)
	next.corrupted[instanceID] = true
	store.current = next
}

func (store *Store) RegisterDefinition(
	ctx context.Context,
	record strategystorage.DefinitionRecord,
) (strategystorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return strategystorage.RegistrationOutcome{}, err
	}
	canonical, err := strategystorage.CanonicalDefinition(record)
	if err != nil {
		return strategystorage.RegistrationOutcome{}, strategystorage.ErrInvalidPublication
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return strategystorage.RegistrationOutcome{}, err
	}
	if existing, found := store.current.definitions[record.ID]; found {
		if bytes.Equal(existing.canonical, canonical) {
			return strategystorage.RegistrationOutcome{Status: strategystorage.RegistrationIdempotent}, nil
		}
		return strategystorage.RegistrationOutcome{}, &strategystorage.IdentityCollisionError{
			Kind: "definition", Identity: record.ID.String(),
		}
	}
	if len(store.current.definitions) >= store.limits.Definitions {
		return strategystorage.RegistrationOutcome{}, strategystorage.ErrStorageFailure
	}
	next := cloneSnapshot(store.current)
	next.definitions[record.ID] = storedDefinition{value: record, canonical: canonical}
	store.current = next
	return strategystorage.RegistrationOutcome{Status: strategystorage.RegistrationCommitted}, nil
}

func (store *Store) RegisterVersion(
	ctx context.Context,
	descriptor strategymodel.Descriptor,
) (strategystorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return strategystorage.RegistrationOutcome{}, err
	}
	if descriptor.Validate() != nil {
		return strategystorage.RegistrationOutcome{}, strategystorage.ErrInvalidPublication
	}
	canonical, err := strategystorage.CanonicalDescriptor(descriptor)
	if err != nil {
		return strategystorage.RegistrationOutcome{}, strategystorage.ErrInvalidPublication
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return strategystorage.RegistrationOutcome{}, err
	}
	if _, found := store.current.definitions[descriptor.Manifest.DefinitionID]; !found {
		return strategystorage.RegistrationOutcome{}, strategystorage.ErrNotFound
	}
	if existing, found := store.current.versions[descriptor.VersionID]; found {
		if bytes.Equal(existing.canonical, canonical) {
			return strategystorage.RegistrationOutcome{Status: strategystorage.RegistrationIdempotent}, nil
		}
		return strategystorage.RegistrationOutcome{}, &strategystorage.IdentityCollisionError{
			Kind: "version", Identity: descriptor.VersionID.String(),
		}
	}
	if len(store.current.versions) >= store.limits.Versions {
		return strategystorage.RegistrationOutcome{}, strategystorage.ErrStorageFailure
	}
	next := cloneSnapshot(store.current)
	next.versions[descriptor.VersionID] = storedVersion{value: descriptor, canonical: canonical}
	store.current = next
	return strategystorage.RegistrationOutcome{Status: strategystorage.RegistrationCommitted}, nil
}

func (store *Store) Definition(
	ctx context.Context,
	id strategymodel.DefinitionID,
) (strategystorage.DefinitionRecord, error) {
	if err := ctx.Err(); err != nil {
		return strategystorage.DefinitionRecord{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.current.definitions[id]
	if !found {
		return strategystorage.DefinitionRecord{}, strategystorage.ErrNotFound
	}
	return value.value, nil
}

func (store *Store) Version(
	ctx context.Context,
	id strategymodel.VersionID,
) (strategymodel.Descriptor, error) {
	if err := ctx.Err(); err != nil {
		return strategymodel.Descriptor{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.current.versions[id]
	if !found {
		return strategymodel.Descriptor{}, strategystorage.ErrNotFound
	}
	return value.value, nil
}

func (store *Store) Definitions(
	ctx context.Context,
) ([]strategystorage.DefinitionRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]strategystorage.DefinitionRecord, 0, len(store.current.definitions))
	for _, value := range store.current.definitions {
		result = append(result, value.value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID.String() < result[j].ID.String() })
	return result, nil
}

func (store *Store) Versions(
	ctx context.Context,
	definitionID strategymodel.DefinitionID,
) ([]strategymodel.Descriptor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	var result []strategymodel.Descriptor
	for _, value := range store.current.versions {
		if value.value.Manifest.DefinitionID == definitionID {
			result = append(result, value.value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].VersionID.String() < result[j].VersionID.String()
	})
	return result, nil
}

func (store *Store) PutInstance(
	ctx context.Context,
	expected strategymodel.InstanceRevisionID,
	instance strategymodel.StrategyInstance,
) (strategystorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return strategystorage.RegistrationOutcome{}, err
	}
	canonical, err := strategystorage.CanonicalInstance(instance)
	if err != nil || instance.ID() == "" || instance.RevisionID().IsZero() {
		return strategystorage.RegistrationOutcome{}, strategystorage.ErrInvalidPublication
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return strategystorage.RegistrationOutcome{}, err
	}
	version, found := store.current.versions[instance.VersionID()]
	if !found || version.value.Manifest.DefinitionID != instance.DefinitionID() {
		return strategystorage.RegistrationOutcome{}, strategystorage.ErrNotFound
	}
	existing, found := store.current.instances[instance.ID()]
	if found && bytes.Equal(existing.canonical, canonical) {
		return strategystorage.RegistrationOutcome{Status: strategystorage.RegistrationIdempotent}, nil
	}
	if !found {
		if !expected.IsZero() || instance.Generation() != 1 {
			return strategystorage.RegistrationOutcome{}, &strategystorage.InstanceRevisionConflictError{
				InstanceID: instance.ID(), Expected: expected,
			}
		}
		if len(store.current.instances) >= store.limits.Instances {
			return strategystorage.RegistrationOutcome{}, strategystorage.ErrStorageFailure
		}
	} else {
		if expected != existing.value.RevisionID() {
			return strategystorage.RegistrationOutcome{}, &strategystorage.InstanceRevisionConflictError{
				InstanceID: instance.ID(), Expected: expected, Actual: existing.value.RevisionID(),
			}
		}
		if instance.Generation() != existing.value.Generation()+1 {
			return strategystorage.RegistrationOutcome{}, strategystorage.ErrInvalidPublication
		}
	}
	next := cloneSnapshot(store.current)
	next.instances[instance.ID()] = storedInstance{value: instance, canonical: canonical}
	store.current = next
	return strategystorage.RegistrationOutcome{Status: strategystorage.RegistrationCommitted}, nil
}

func (store *Store) Instance(
	ctx context.Context,
	id domain.StrategyID,
) (strategymodel.StrategyInstance, error) {
	if err := ctx.Err(); err != nil {
		return strategymodel.StrategyInstance{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.current.instances[id]
	if !found {
		return strategymodel.StrategyInstance{}, strategystorage.ErrNotFound
	}
	return value.value, nil
}

func (store *Store) Instances(
	ctx context.Context,
) ([]strategymodel.StrategyInstance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]strategymodel.StrategyInstance, 0, len(store.current.instances))
	for _, value := range store.current.instances {
		result = append(result, value.value)
	}
	sort.Slice(result, func(i, j int) bool {
		return string(result[i].ID()) < string(result[j].ID())
	})
	return result, nil
}

func (store *Store) InitializeCheckpoint(
	ctx context.Context,
	checkpoint strategystorage.RuntimeCheckpoint,
) (strategystorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return strategystorage.RegistrationOutcome{}, err
	}
	if checkpoint.Revision() != 0 || strategystorage.VerifyCheckpoint(checkpoint) != nil {
		return strategystorage.RegistrationOutcome{}, strategystorage.ErrCorruptCheckpoint
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return strategystorage.RegistrationOutcome{}, err
	}
	instance, found := store.current.instances[checkpoint.InstanceID()]
	if !found || !checkpointMatchesInstance(checkpoint, instance.value) {
		return strategystorage.RegistrationOutcome{}, strategystorage.ErrInvalidPublication
	}
	existing := store.current.checkpoints[checkpoint.InstanceID()]
	if len(existing) > 0 {
		if existing[0].Checksum() == checkpoint.Checksum() {
			return strategystorage.RegistrationOutcome{Status: strategystorage.RegistrationIdempotent}, nil
		}
		return strategystorage.RegistrationOutcome{}, &strategystorage.IdentityCollisionError{
			Kind: "checkpoint", Identity: string(checkpoint.InstanceID()) + "/0",
		}
	}
	next := cloneSnapshot(store.current)
	next.checkpoints[checkpoint.InstanceID()] = []strategystorage.RuntimeCheckpoint{checkpoint}
	store.current = next
	return strategystorage.RegistrationOutcome{Status: strategystorage.RegistrationCommitted}, nil
}

func (store *Store) CurrentCheckpoint(
	ctx context.Context,
	instanceID domain.StrategyID,
) (strategystorage.RuntimeCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return strategystorage.RuntimeCheckpoint{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	values := store.current.checkpoints[instanceID]
	if len(values) == 0 {
		return strategystorage.RuntimeCheckpoint{}, strategystorage.ErrNotFound
	}
	if store.current.corrupted[instanceID] ||
		strategystorage.VerifyCheckpoint(values[len(values)-1]) != nil {
		return strategystorage.RuntimeCheckpoint{}, strategystorage.ErrCorruptCheckpoint
	}
	return values[len(values)-1], nil
}

func (store *Store) Checkpoint(
	ctx context.Context,
	instanceID domain.StrategyID,
	revision uint64,
) (strategystorage.RuntimeCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return strategystorage.RuntimeCheckpoint{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	values := store.current.checkpoints[instanceID]
	if revision >= uint64(len(values)) {
		return strategystorage.RuntimeCheckpoint{}, strategystorage.ErrNotFound
	}
	value := values[revision]
	if strategystorage.VerifyCheckpoint(value) != nil {
		return strategystorage.RuntimeCheckpoint{}, strategystorage.ErrCorruptCheckpoint
	}
	return value, nil
}

func (store *Store) Checkpoints(
	ctx context.Context,
	instanceID domain.StrategyID,
) ([]strategystorage.RuntimeCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	values := store.current.checkpoints[instanceID]
	if len(values) == 0 {
		return nil, strategystorage.ErrNotFound
	}
	return append([]strategystorage.RuntimeCheckpoint(nil), values...), nil
}

func (store *Store) Restore(
	ctx context.Context,
	data []byte,
	expectation strategystorage.RestoreExpectation,
) (strategystorage.RuntimeCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return strategystorage.RuntimeCheckpoint{}, err
	}
	checkpoint, err := strategystorage.DecodeCheckpoint(append([]byte(nil), data...))
	if err != nil || strategystorage.VerifyRestoration(checkpoint, expectation) != nil {
		return strategystorage.RuntimeCheckpoint{}, strategystorage.ErrCorruptCheckpoint
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	values := store.current.checkpoints[checkpoint.InstanceID()]
	if checkpoint.Revision() >= uint64(len(values)) ||
		values[checkpoint.Revision()].Checksum() != checkpoint.Checksum() {
		return strategystorage.RuntimeCheckpoint{}, strategystorage.ErrCorruptCheckpoint
	}
	if checkpoint.Revision() > 0 &&
		values[checkpoint.Revision()-1].Checksum() != checkpoint.ParentChecksum() {
		return strategystorage.RuntimeCheckpoint{}, strategystorage.ErrCorruptCheckpoint
	}
	return checkpoint, nil
}

func (store *Store) PublishEvaluation(
	ctx context.Context,
	input strategystorage.EvaluationPublication,
) (strategystorage.PublicationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return strategystorage.PublicationOutcome{Status: strategystorage.PublicationCancelled}, err
	}
	publication, err := strategystorage.NewEvaluationPublication(input)
	if err != nil {
		return strategystorage.PublicationOutcome{Status: strategystorage.PublicationInvalid}, err
	}
	canonical, err := strategystorage.CanonicalPublication(publication)
	if err != nil {
		return strategystorage.PublicationOutcome{Status: strategystorage.PublicationInvalid},
			strategystorage.ErrInvalidPublication
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return strategystorage.PublicationOutcome{Status: strategystorage.PublicationCancelled}, err
	}
	if existing, found := store.current.publications[publication.EvaluationID]; found {
		if existing.checksum == publication.Checksum &&
			bytes.Equal(existing.canonical, canonical) {
			outcome := existing.outcome
			outcome.Status = strategystorage.PublicationIdempotent
			return outcome, nil
		}
		return strategystorage.PublicationOutcome{
				Status: strategystorage.PublicationIdentityCollision,
			}, &strategystorage.IdentityCollisionError{
				Kind: "evaluation", Identity: publication.EvaluationID.String(),
			}
	}
	instance, found := store.current.instances[publication.InstanceID]
	if !found || instance.value.RevisionID() != publication.InstanceRevisionID ||
		instance.value.VersionID() != publication.VersionID ||
		instance.value.DefinitionID() != publication.DefinitionID ||
		instance.value.Configuration().Hash() != publication.ConfigurationHash {
		return strategystorage.PublicationOutcome{Status: strategystorage.PublicationInvalid},
			strategystorage.ErrInvalidPublication
	}
	values := store.current.checkpoints[publication.InstanceID]
	if len(values) == 0 {
		return strategystorage.PublicationOutcome{Status: strategystorage.PublicationCorruptPriorState},
			strategystorage.ErrCorruptCheckpoint
	}
	current := values[len(values)-1]
	if store.current.corrupted[publication.InstanceID] ||
		strategystorage.VerifyCheckpoint(current) != nil {
		return strategystorage.PublicationOutcome{Status: strategystorage.PublicationCorruptPriorState},
			strategystorage.ErrCorruptCheckpoint
	}
	actualRevision := current.Revision()
	if actualRevision != publication.ExpectedStateRevision {
		return strategystorage.PublicationOutcome{Status: strategystorage.PublicationRevisionConflict},
			&strategystorage.RevisionConflictError{
				InstanceID: publication.InstanceID,
				Expected:   publication.ExpectedStateRevision, Actual: actualRevision,
			}
	}
	if publication.Checkpoint.ParentChecksum() != current.Checksum() ||
		publication.Record.PriorStateHash() != current.State().Hash() {
		return strategystorage.PublicationOutcome{Status: strategystorage.PublicationInvalid},
			strategystorage.ErrInvalidPublication
	}
	if store.injector != nil {
		if injectedErr := store.injector(FailureBeforeCommit); injectedErr != nil {
			if errors.Is(injectedErr, context.Canceled) || errors.Is(injectedErr, context.DeadlineExceeded) {
				return strategystorage.PublicationOutcome{Status: strategystorage.PublicationCancelled},
					injectedErr
			}
			return strategystorage.PublicationOutcome{Status: strategystorage.PublicationInternalFailure},
				errors.Join(strategystorage.ErrStorageFailure, injectedErr)
		}
	}
	if err := ctx.Err(); err != nil {
		return strategystorage.PublicationOutcome{Status: strategystorage.PublicationCancelled}, err
	}
	if len(store.current.evaluations) >= store.limits.Evaluations ||
		(publication.Observation != nil &&
			len(store.current.observations) >= store.limits.Observations) ||
		(publication.Proposal != nil && len(store.current.proposals) >= store.limits.Proposals) {
		return strategystorage.PublicationOutcome{Status: strategystorage.PublicationInternalFailure},
			strategystorage.ErrStorageFailure
	}
	next := cloneSnapshot(store.current)
	next.checkpoints[publication.InstanceID] = append(
		next.checkpoints[publication.InstanceID], publication.Checkpoint,
	)
	next.evaluations[publication.EvaluationID] = publication.Record
	if publication.Observation != nil {
		next.observations[publication.EvaluationID] = *publication.Observation
	}
	if publication.Proposal != nil {
		next.proposals[publication.Proposal.ID()] = *publication.Proposal
	}
	outcome := strategystorage.PublicationOutcome{
		Status:              strategystorage.PublicationCommitted,
		CheckpointRevision:  publication.Checkpoint.Revision(),
		CheckpointChecksum:  publication.Checkpoint.Checksum(),
		PublicationChecksum: publication.Checksum,
	}
	next.publications[publication.EvaluationID] = storedPublication{
		checksum: publication.Checksum, outcome: outcome,
		canonical: append([]byte(nil), canonical...),
	}
	delete(next.corrupted, publication.InstanceID)
	store.current = next
	return outcome, nil
}

func (store *Store) Evaluation(
	ctx context.Context,
	id strategymodel.EvaluationID,
) (strategymodel.EvaluationRecord, error) {
	if err := ctx.Err(); err != nil {
		return strategymodel.EvaluationRecord{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.current.evaluations[id]
	if !found {
		return strategymodel.EvaluationRecord{}, strategystorage.ErrNotFound
	}
	return value, nil
}

func (store *Store) Evaluations(
	ctx context.Context,
	instanceID domain.StrategyID,
) ([]strategymodel.EvaluationRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	var result []strategymodel.EvaluationRecord
	for _, value := range store.current.evaluations {
		if value.InstanceID() == instanceID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CheckpointRevision() != result[j].CheckpointRevision() {
			return result[i].CheckpointRevision() < result[j].CheckpointRevision()
		}
		return result[i].EvaluationID().String() < result[j].EvaluationID().String()
	})
	return result, nil
}

func (store *Store) Observation(
	ctx context.Context,
	id strategymodel.EvaluationID,
) (strategymodel.StrategyObservation, error) {
	if err := ctx.Err(); err != nil {
		return strategymodel.StrategyObservation{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.current.observations[id]
	if !found {
		return strategymodel.StrategyObservation{}, strategystorage.ErrNotFound
	}
	return value, nil
}

func (store *Store) Observations(
	ctx context.Context,
	instanceID domain.StrategyID,
) ([]strategymodel.StrategyObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	var result []strategymodel.StrategyObservation
	for evaluationID, value := range store.current.observations {
		record := store.current.evaluations[evaluationID]
		if record.InstanceID() == instanceID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].GeneratedAt().Equal(result[j].GeneratedAt()) {
			return result[i].GeneratedAt().Before(result[j].GeneratedAt())
		}
		return result[i].EvaluationID().String() < result[j].EvaluationID().String()
	})
	return result, nil
}

func (store *Store) Proposal(
	ctx context.Context,
	id strategymodel.ProposalID,
) (strategymodel.TradeProposal, error) {
	if err := ctx.Err(); err != nil {
		return strategymodel.TradeProposal{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.current.proposals[id]
	if !found {
		return strategymodel.TradeProposal{}, strategystorage.ErrNotFound
	}
	return value, nil
}

func (store *Store) Proposals(
	ctx context.Context,
	instanceID domain.StrategyID,
) ([]strategymodel.TradeProposal, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	var result []strategymodel.TradeProposal
	for _, value := range store.current.proposals {
		if value.Metadata().InstanceID == instanceID {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i].Metadata(), result[j].Metadata()
		if !left.GeneratedAt.Equal(right.GeneratedAt) {
			return left.GeneratedAt.Before(right.GeneratedAt)
		}
		return result[i].ID().String() < result[j].ID().String()
	})
	return result, nil
}

func checkpointMatchesInstance(
	checkpoint strategystorage.RuntimeCheckpoint,
	instance strategymodel.StrategyInstance,
) bool {
	return checkpoint.InstanceID() == instance.ID() &&
		checkpoint.DefinitionID() == instance.DefinitionID() &&
		checkpoint.VersionID() == instance.VersionID() &&
		checkpoint.InstanceRevisionID() == instance.RevisionID() &&
		checkpoint.ConfigurationHash() == instance.Configuration().Hash() &&
		checkpoint.State().SchemaVersion() != ""
}

var (
	_ strategystorage.DefinitionRegistry         = (*Store)(nil)
	_ strategystorage.InstanceRepository         = (*Store)(nil)
	_ strategystorage.CheckpointRepository       = (*Store)(nil)
	_ strategystorage.EvaluationRecordRepository = (*Store)(nil)
	_ strategystorage.ObservationRepository      = (*Store)(nil)
	_ strategystorage.TradeProposalRepository    = (*Store)(nil)
	_ strategystorage.EvaluationPublisher        = (*Store)(nil)
)
