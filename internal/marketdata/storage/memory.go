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
	mu           sync.RWMutex
	datasets     map[DatasetID]memoryDataset
	publications map[string][]Publication
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		datasets: make(map[DatasetID]memoryDataset), publications: make(map[string][]Publication),
	}
}

func (r *MemoryRepository) Create(ctx context.Context, draft DraftManifest) (DatasetWriter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var parent *DatasetManifest
	if draft.ParentID != "" {
		r.mu.RLock()
		dataset, found := r.datasets[draft.ParentID]
		r.mu.RUnlock()
		if !found {
			return nil, ErrDatasetNotFound
		}
		value := dataset.manifest
		parent = &value
	}
	normalized, err := NormalizeDraft(draft, parent)
	if err != nil {
		return nil, err
	}
	return &memoryWriter{repository: r, draft: normalized}, nil
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
	masterBytes := w.draft.InstrumentMaster
	if len(masterBytes) == 0 {
		masterBytes = []byte(w.draft.MasterVersion)
	}
	masterDigest := sha256.Sum256(masterBytes)
	idDigest := sha256.Sum256([]byte(fmt.Sprintf(
		"v2|%s|%d|%s|%s|%s|%s|%s|%s|%s|%s",
		w.draft.ParentID, w.draft.Revision, w.draft.MasterVersion, w.draft.CalendarVersion,
		w.draft.SourceSHA256, w.draft.OrderingVersion, w.draft.BuildKey,
		hex.EncodeToString(eventDigest[:]), hex.EncodeToString(qualityDigest[:]),
		hex.EncodeToString(masterDigest[:]),
	)))
	manifest := DatasetManifest{
		SchemaVersion:    2,
		ID:               DatasetID(hex.EncodeToString(idDigest[:])),
		ParentID:         w.draft.ParentID,
		Revision:         w.draft.Revision,
		MasterVersion:    w.draft.MasterVersion,
		CalendarVersion:  w.draft.CalendarVersion,
		Source:           w.draft.Source,
		SourceSHA256:     w.draft.SourceSHA256,
		OrderingVersion:  w.draft.OrderingVersion,
		BuildKey:         w.draft.BuildKey,
		CorrectionReason: w.draft.CorrectionReason,
		RequestID:        w.draft.RequestID,
		Series:           w.draft.Series,
		CreatedAt:        w.draft.CreatedAt.UTC(),
		EventCount:       int64(len(w.events)),
		QualityCount:     int64(len(w.quality)),
		EventsSHA256:     hex.EncodeToString(eventDigest[:]),
		QualitySHA256:    hex.EncodeToString(qualityDigest[:]),
		MasterSHA256:     hex.EncodeToString(masterDigest[:]),
	}
	if len(w.events) > 0 {
		manifest.Start = w.events[0].ExchangeTime()
		manifest.End = w.events[len(w.events)-1].ExchangeTime()
	}
	w.repository.mu.Lock()
	for _, existing := range w.repository.datasets {
		if existing.manifest.BuildKey != manifest.BuildKey {
			continue
		}
		w.repository.mu.Unlock()
		if existing.manifest.EventsSHA256 == manifest.EventsSHA256 &&
			existing.manifest.QualitySHA256 == manifest.QualitySHA256 &&
			existing.manifest.MasterSHA256 == manifest.MasterSHA256 {
			return existing.manifest, nil
		}
		return DatasetManifest{}, ErrDatasetCorrupt
	}
	if existing, found := w.repository.datasets[manifest.ID]; found {
		w.repository.mu.Unlock()
		if existing.manifest.BuildKey == manifest.BuildKey &&
			existing.manifest.EventsSHA256 == manifest.EventsSHA256 &&
			existing.manifest.QualitySHA256 == manifest.QualitySHA256 {
			return existing.manifest, nil
		}
		return DatasetManifest{}, ErrDatasetCorrupt
	}
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

func (r *MemoryRepository) Lineage(
	ctx context.Context,
	id DatasetID,
	limit int,
) ([]DatasetManifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []DatasetManifest
	for id != "" && len(result) < limit {
		dataset, found := r.datasets[id]
		if !found {
			return nil, ErrDatasetNotFound
		}
		result = append(result, dataset.manifest)
		id = dataset.manifest.ParentID
	}
	return result, nil
}

func (r *MemoryRepository) Publish(
	ctx context.Context,
	request PublicationRequest,
) (Publication, error) {
	if err := ctx.Err(); err != nil {
		return Publication{}, err
	}
	if request.Series == "" || request.DatasetID == "" || request.RequestID == "" ||
		request.Reason == "" || request.PublishedAt.IsZero() ||
		(request.Action != PublicationPublish && request.Action != PublicationRollback) {
		return Publication{}, ErrPublicationConflict
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, found := r.datasets[request.DatasetID]; !found {
		return Publication{}, ErrDatasetNotFound
	}
	history := r.publications[request.Series]
	for _, existing := range history {
		if existing.RequestID == request.RequestID {
			if existing.DatasetID == request.DatasetID &&
				existing.Action == request.Action &&
				existing.Reason == strings.TrimSpace(request.Reason) {
				return existing, nil
			}
			return Publication{}, ErrPublicationConflict
		}
	}
	var current DatasetID
	if len(history) > 0 {
		current = history[len(history)-1].DatasetID
	}
	if current != request.ExpectedCurrentID {
		return Publication{}, ErrPublicationConflict
	}
	publication := Publication{
		Series: request.Series, Generation: int64(len(history) + 1),
		DatasetID: request.DatasetID, PreviousDatasetID: current,
		Action: request.Action, Reason: request.Reason, RequestID: request.RequestID,
		PublishedAt: request.PublishedAt.UTC(),
	}
	r.publications[request.Series] = append(history, publication)
	return publication, nil
}

func (r *MemoryRepository) CurrentPublication(
	ctx context.Context,
	series string,
) (Publication, error) {
	if err := ctx.Err(); err != nil {
		return Publication{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	history := r.publications[series]
	if len(history) == 0 {
		return Publication{}, ErrDatasetNotFound
	}
	return history[len(history)-1], nil
}

func (r *MemoryRepository) Publications(
	ctx context.Context,
	series string,
	limit int,
) ([]Publication, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	history := r.publications[series]
	if len(history) == 0 {
		return nil, ErrDatasetNotFound
	}
	start := 0
	if len(history) > limit {
		start = len(history) - limit
	}
	result := append([]Publication(nil), history[start:]...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result, nil
}

var _ RevisionRepository = (*MemoryRepository)(nil)
