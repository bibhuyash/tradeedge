package memory

import (
	"bytes"
	"context"
	"sync"

	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	accountingreconciliation "github.com/bibhuyash/tradeedge/internal/accounting/reconciliation"
	accountingstorage "github.com/bibhuyash/tradeedge/internal/accounting/storage"
)

type ReconciliationStore struct {
	mu            sync.RWMutex
	limit         int
	byObservation map[accountingmodel.StateChecksum]accountingreconciliation.Evidence
	checkpoints   map[accountingmodel.StateChecksum]accountingreconciliation.Checkpoint
}

func NewReconciliationStore(limit int) (*ReconciliationStore, error) {
	if limit <= 0 || limit > 100000 {
		return nil, accountingreconciliation.ErrInvalidRequest
	}
	return &ReconciliationStore{limit: limit, byObservation: map[accountingmodel.StateChecksum]accountingreconciliation.Evidence{}, checkpoints: map[accountingmodel.StateChecksum]accountingreconciliation.Checkpoint{}}, nil
}
func (store *ReconciliationStore) Evidence(ctx context.Context, id accountingmodel.StateChecksum) (accountingreconciliation.Evidence, error) {
	if err := ctx.Err(); err != nil {
		return accountingreconciliation.Evidence{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.byObservation[id]
	if !ok {
		return accountingreconciliation.Evidence{}, accountingstorage.ErrNotFound
	}
	return value, nil
}
func (store *ReconciliationStore) Checkpoint(ctx context.Context, id accountingmodel.StateChecksum) (accountingreconciliation.Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return accountingreconciliation.Checkpoint{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.checkpoints[id]
	if !ok {
		return accountingreconciliation.Checkpoint{}, accountingstorage.ErrNotFound
	}
	return value, nil
}
func (store *ReconciliationStore) Publish(ctx context.Context, evidence accountingreconciliation.Evidence, checkpoint accountingreconciliation.Checkpoint) (accountingreconciliation.Evidence, error) {
	if err := ctx.Err(); err != nil {
		return accountingreconciliation.Evidence{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if current, ok := store.byObservation[evidence.ObservationID]; ok {
		if bytes.Equal(current.CanonicalJSON(), evidence.CanonicalJSON()) {
			return current, nil
		}
		return accountingreconciliation.Evidence{}, accountingreconciliation.ErrIdentityCollision
	}
	if len(store.byObservation) >= store.limit {
		return accountingreconciliation.Evidence{}, accountingstorage.ErrCapacityExhausted
	}
	expected, _ := accountingreconciliation.NewCheckpoint(evidence.ObservationID, evidence.ID)
	if expected.Checksum != checkpoint.Checksum {
		return accountingreconciliation.Evidence{}, accountingreconciliation.ErrInvalidRequest
	}
	store.byObservation[evidence.ObservationID] = evidence
	store.checkpoints[evidence.ObservationID] = checkpoint
	return evidence, nil
}

var _ accountingreconciliation.Repository = (*ReconciliationStore)(nil)
