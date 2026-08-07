package memory

import (
	"bytes"
	"context"
	"sort"
	"sync"

	accountingengine "github.com/bibhuyash/tradeedge/internal/accounting/engine"
	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	accountingstorage "github.com/bibhuyash/tradeedge/internal/accounting/storage"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
)

type Limits struct{ Positions, Revisions, Applications, Publications int }

func DefaultLimits() Limits {
	return Limits{Positions: 128, Revisions: 10000, Applications: 10000, Publications: 10000}
}
func (value Limits) valid() bool {
	return value.Positions > 0 && value.Revisions > 0 && value.Applications > 0 && value.Publications > 0
}

type storedPublication struct {
	value     accountingstorage.PositionPublication
	receipt   accountingstorage.PublicationReceipt
	canonical []byte
}
type storedFill struct {
	fill        accountingmodel.AccountingFill
	application accountingmodel.FillApplication
}

type Store struct {
	mu               sync.RWMutex
	limits           Limits
	current          map[accountingmodel.PositionID]accountingmodel.PositionRevision
	checkpoints      map[accountingmodel.PositionID]map[accountingmodel.PositionRevision]accountingstorage.PositionCheckpoint
	fills            map[executionmodel.FillID]storedFill
	applications     map[accountingmodel.PositionID][]accountingmodel.FillApplication
	publications     map[accountingmodel.PublicationID]storedPublication
	failBeforeCommit bool
}

func New(limits Limits) (*Store, error) {
	if !limits.valid() {
		return nil, accountingstorage.ErrCapacityExhausted
	}
	return &Store{limits: limits, current: map[accountingmodel.PositionID]accountingmodel.PositionRevision{}, checkpoints: map[accountingmodel.PositionID]map[accountingmodel.PositionRevision]accountingstorage.PositionCheckpoint{}, fills: map[executionmodel.FillID]storedFill{}, applications: map[accountingmodel.PositionID][]accountingmodel.FillApplication{}, publications: map[accountingmodel.PublicationID]storedPublication{}}, nil
}
func NewDefault() *Store { value, _ := New(DefaultLimits()); return value }
func (store *Store) SetFailBeforeCommit(value bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failBeforeCommit = value
}

func (store *Store) CurrentPositionCheckpoint(ctx context.Context, id accountingmodel.PositionID) (accountingstorage.PositionCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return accountingstorage.PositionCheckpoint{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	revision, ok := store.current[id]
	if !ok {
		return accountingstorage.PositionCheckpoint{}, accountingstorage.ErrNotFound
	}
	return store.checkpoints[id][revision], nil
}
func (store *Store) PositionCheckpoint(ctx context.Context, id accountingmodel.PositionID, revision accountingmodel.PositionRevision) (accountingstorage.PositionCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return accountingstorage.PositionCheckpoint{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.checkpoints[id][revision]
	if !ok {
		return accountingstorage.PositionCheckpoint{}, accountingstorage.ErrNotFound
	}
	return value, nil
}
func (store *Store) Position(ctx context.Context, id accountingmodel.PositionID) (accountingmodel.PositionSnapshot, error) {
	value, err := store.CurrentPositionCheckpoint(ctx, id)
	return value.Snapshot, err
}
func (store *Store) ApplicationByFill(ctx context.Context, id executionmodel.FillID) (accountingmodel.FillApplication, accountingmodel.AccountingFill, error) {
	if err := ctx.Err(); err != nil {
		return accountingmodel.FillApplication{}, accountingmodel.AccountingFill{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.fills[id]
	if !ok {
		return accountingmodel.FillApplication{}, accountingmodel.AccountingFill{}, accountingstorage.ErrNotFound
	}
	return value.application, value.fill, nil
}
func (store *Store) Applications(ctx context.Context, id accountingmodel.PositionID) ([]accountingmodel.FillApplication, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	values, ok := store.applications[id]
	if !ok {
		return nil, accountingstorage.ErrNotFound
	}
	return append([]accountingmodel.FillApplication(nil), values...), nil
}
func (store *Store) Publications(ctx context.Context, id accountingmodel.PositionID) ([]accountingstorage.PositionPublication, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	values := make([]accountingstorage.PositionPublication, 0)
	for _, value := range store.publications {
		if value.receipt.PositionID == id {
			values = append(values, value.value)
		}
	}
	if len(values) == 0 {
		return nil, accountingstorage.ErrNotFound
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].NextCheckpoint.Snapshot.Revision() < values[j].NextCheckpoint.Snapshot.Revision()
	})
	return values, nil
}
func (store *Store) CommittedPublication(ctx context.Context, id accountingmodel.PublicationID) (accountingstorage.PublicationReceipt, error) {
	if err := ctx.Err(); err != nil {
		return accountingstorage.PublicationReceipt{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.publications[id]
	if !ok {
		return accountingstorage.PublicationReceipt{}, accountingstorage.ErrNotFound
	}
	return value.receipt, nil
}

func (store *Store) PublishPosition(ctx context.Context, publication accountingstorage.PositionPublication) (accountingstorage.PublicationReceipt, error) {
	if err := ctx.Err(); err != nil {
		return accountingstorage.PublicationReceipt{}, err
	}
	validated, err := accountingstorage.NewPositionPublication(publication)
	if err != nil {
		return accountingstorage.PublicationReceipt{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return accountingstorage.PublicationReceipt{}, err
	}
	if existing, ok := store.publications[validated.PublicationID]; ok {
		if bytes.Equal(existing.canonical, validated.CanonicalJSON()) {
			receipt := existing.receipt
			receipt.Status = accountingstorage.PublicationIdempotent
			return receipt, nil
		}
		return accountingstorage.PublicationReceipt{}, &accountingstorage.IdentityCollisionError{Kind: "publication", Identity: validated.PublicationID.String()}
	}
	fillID := validated.Fill.Spec().Fill.ID()
	if existing, ok := store.fills[fillID]; ok {
		if !bytes.Equal(existing.fill.CanonicalJSON(), validated.Fill.CanonicalJSON()) {
			return accountingstorage.PublicationReceipt{}, &accountingstorage.IdentityCollisionError{Kind: "fill", Identity: fillID.String()}
		}
		return accountingstorage.PublicationReceipt{}, &accountingstorage.IdentityCollisionError{Kind: "fill_application", Identity: fillID.String()}
	}
	positionID := validated.Fill.PositionID()
	actual := store.current[positionID]
	if actual != validated.ExpectedRevision {
		return accountingstorage.PublicationReceipt{}, &accountingstorage.RevisionConflictError{PositionID: positionID, Expected: validated.ExpectedRevision, Actual: actual}
	}
	if actual > 0 {
		current := store.checkpoints[positionID][actual]
		if current.CheckpointChecksum != validated.ExpectedCheckpoint || current.Snapshot.Checksum() != validated.Application.Spec().PreviousSnapshotChecksum {
			return accountingstorage.PublicationReceipt{}, &accountingstorage.RevisionConflictError{PositionID: positionID, Expected: validated.ExpectedRevision, Actual: actual}
		}
	}
	var currentSnapshot *accountingmodel.PositionSnapshot
	if actual > 0 {
		value := store.checkpoints[positionID][actual].Snapshot
		currentSnapshot = &value
	}
	recomputed, recomputeErr := accountingengine.Apply(currentSnapshot, validated.Fill)
	if recomputeErr != nil || !bytes.Equal(recomputed.Snapshot.CanonicalJSON(), validated.NextCheckpoint.Snapshot.CanonicalJSON()) || !bytes.Equal(recomputed.Application.CanonicalJSON(), validated.Application.CanonicalJSON()) {
		return accountingstorage.PublicationReceipt{}, accountingstorage.ErrInvalidPublication
	}
	if actual == 0 && len(store.current) >= store.limits.Positions || totalRevisions(store) >= store.limits.Revisions || len(store.fills) >= store.limits.Applications || len(store.publications) >= store.limits.Publications {
		return accountingstorage.PublicationReceipt{}, accountingstorage.ErrCapacityExhausted
	}
	if store.failBeforeCommit {
		return accountingstorage.PublicationReceipt{}, accountingstorage.ErrInternal
	}
	receipt := accountingstorage.PublicationReceipt{Status: accountingstorage.PublicationCommitted, PublicationID: validated.PublicationID, PositionID: positionID, Revision: validated.NextCheckpoint.Snapshot.Revision(), FillID: fillID, ApplicationID: validated.Application.ID(), CheckpointChecksum: validated.NextCheckpoint.CheckpointChecksum, PublicationChecksum: validated.PublicationChecksum}
	if store.checkpoints[positionID] == nil {
		store.checkpoints[positionID] = map[accountingmodel.PositionRevision]accountingstorage.PositionCheckpoint{}
	}
	store.checkpoints[positionID][receipt.Revision] = validated.NextCheckpoint
	store.current[positionID] = receipt.Revision
	store.fills[fillID] = storedFill{validated.Fill, validated.Application}
	store.applications[positionID] = append(store.applications[positionID], validated.Application)
	store.publications[validated.PublicationID] = storedPublication{validated, receipt, validated.CanonicalJSON()}
	return receipt, nil
}

func (store *Store) RestorePosition(ctx context.Context, publications []accountingstorage.PositionPublication) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(publications) == 0 {
		return accountingstorage.ErrCorruptCheckpoint
	}
	ordered := append([]accountingstorage.PositionPublication(nil), publications...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].NextCheckpoint.Snapshot.Revision() < ordered[j].NextCheckpoint.Snapshot.Revision()
	})
	temporary, _ := New(store.limits)
	positionID := ordered[0].Fill.PositionID()
	for _, publication := range ordered {
		if publication.Fill.PositionID() != positionID {
			return accountingstorage.ErrCorruptCheckpoint
		}
		if _, err := temporary.PublishPosition(ctx, publication); err != nil {
			return accountingstorage.ErrCorruptCheckpoint
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.current[positionID]; exists {
		return &accountingstorage.IdentityCollisionError{Kind: "position", Identity: positionID.String()}
	}
	if len(store.current)+1 > store.limits.Positions || totalRevisions(store)+len(ordered) > store.limits.Revisions || len(store.fills)+len(ordered) > store.limits.Applications || len(store.publications)+len(ordered) > store.limits.Publications {
		return accountingstorage.ErrCapacityExhausted
	}
	for id, value := range temporary.fills {
		if existing, ok := store.fills[id]; ok && !bytes.Equal(existing.fill.CanonicalJSON(), value.fill.CanonicalJSON()) {
			return &accountingstorage.IdentityCollisionError{Kind: "fill", Identity: id.String()}
		}
	}
	store.current[positionID] = temporary.current[positionID]
	store.checkpoints[positionID] = temporary.checkpoints[positionID]
	store.applications[positionID] = temporary.applications[positionID]
	for id, value := range temporary.fills {
		store.fills[id] = value
	}
	for id, value := range temporary.publications {
		store.publications[id] = value
	}
	return nil
}

func totalRevisions(store *Store) int {
	total := 0
	for _, values := range store.checkpoints {
		total += len(values)
	}
	return total
}
