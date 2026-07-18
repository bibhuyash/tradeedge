package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

type memoryDataset struct {
	manifest DatasetManifest
	events   []model.Event
	quality  []model.QualityRecord
}

type MemoryRepository struct {
	mu       sync.RWMutex
	datasets map[DatasetID]memoryDataset
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{datasets: make(map[DatasetID]memoryDataset)}
}

func (r *MemoryRepository) Create(ctx context.Context, draft DraftManifest) (DatasetWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if draft.MasterVersion == "" || draft.Source == "" || draft.OrderingVersion == "" || draft.CreatedAt.IsZero() {
		return nil, ErrDatasetCorrupt
	}
	return &memoryWriter{repository: r, draft: draft}, nil
}

func (r *MemoryRepository) Open(ctx context.Context, id DatasetID) (DatasetReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	dataset, found := r.datasets[id]
	if !found {
		return nil, ErrDatasetNotFound
	}
	return &memoryReader{
		manifest: dataset.manifest,
		events:   append([]model.Event(nil), dataset.events...),
	}, nil
}

type memoryWriter struct {
	repository *MemoryRepository
	draft      DraftManifest
	events     []model.Event
	quality    []model.QualityRecord
	closed     bool
}

func (w *memoryWriter) Append(ctx context.Context, event model.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.closed {
		return ErrDatasetSealed
	}
	if event == nil {
		return ErrDatasetCorrupt
	}
	if len(w.events) > 0 && model.EventLess(event, w.events[len(w.events)-1]) {
		return ErrEventsOutOfOrder
	}
	w.events = append(w.events, event)
	return nil
}

func (w *memoryWriter) RecordQuality(ctx context.Context, record model.QualityRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.closed {
		return ErrDatasetSealed
	}
	w.quality = append(w.quality, record)
	return nil
}

func (w *memoryWriter) Commit(ctx context.Context) (DatasetManifest, error) {
	if err := ctx.Err(); err != nil {
		return DatasetManifest{}, err
	}
	if w.closed {
		return DatasetManifest{}, ErrDatasetSealed
	}
	w.closed = true
	eventKeys := make([]string, len(w.events))
	for index, event := range w.events {
		eventKeys[index] = event.ID().String()
	}
	qualityKeys := make([]string, len(w.quality))
	for index, record := range w.quality {
		qualityKeys[index] = fmt.Sprintf("%s|%s|%s|%s", record.Code, record.InstrumentID, record.ExchangeTime, record.Reason)
	}
	eventDigest := sha256.Sum256([]byte(strings.Join(eventKeys, "\n")))
	qualityDigest := sha256.Sum256([]byte(strings.Join(qualityKeys, "\n")))
	idDigest := sha256.Sum256([]byte(w.draft.MasterVersion + "|" + hex.EncodeToString(eventDigest[:]) + "|" + hex.EncodeToString(qualityDigest[:])))
	manifest := DatasetManifest{
		SchemaVersion:   1,
		ID:              DatasetID(hex.EncodeToString(idDigest[:])),
		ParentID:        w.draft.ParentID,
		MasterVersion:   w.draft.MasterVersion,
		Source:          w.draft.Source,
		OrderingVersion: w.draft.OrderingVersion,
		CreatedAt:       w.draft.CreatedAt.UTC(),
		EventCount:      int64(len(w.events)),
		QualityCount:    int64(len(w.quality)),
		EventsSHA256:    hex.EncodeToString(eventDigest[:]),
		QualitySHA256:   hex.EncodeToString(qualityDigest[:]),
	}
	if len(w.events) > 0 {
		manifest.Start = w.events[0].ExchangeTime()
		manifest.End = w.events[len(w.events)-1].ExchangeTime()
	}
	w.repository.mu.Lock()
	w.repository.datasets[manifest.ID] = memoryDataset{
		manifest: manifest,
		events:   append([]model.Event(nil), w.events...),
		quality:  append([]model.QualityRecord(nil), w.quality...),
	}
	w.repository.mu.Unlock()
	return manifest, nil
}

func (w *memoryWriter) Abort(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.closed {
		return ErrDatasetSealed
	}
	w.closed = true
	w.events = nil
	w.quality = nil
	return nil
}

type memoryReader struct {
	manifest DatasetManifest
	events   []model.Event
	closed   bool
}

func (r *memoryReader) Manifest() DatasetManifest { return r.manifest }

func (r *memoryReader) Scan(ctx context.Context, query EventQuery, sink EventSink) error {
	if r.closed {
		return ErrDatasetSealed
	}
	if sink == nil {
		return ErrDatasetCorrupt
	}
	events := append([]model.Event(nil), r.events...)
	sort.SliceStable(events, func(i, j int) bool { return model.EventLess(events[i], events[j]) })
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return err
		}
		if query.Includes(event) {
			if err := sink(ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *memoryReader) Close() error {
	r.closed = true
	return nil
}
