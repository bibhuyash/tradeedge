package ingest

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/marketdata"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

// LiveConsumer receives only canonical events accepted by Phase 1 ordering.
type LiveConsumer interface {
	Process(context.Context, model.Event) error
}

// LiveService is the bounded, serialized admission boundary for an indefinite
// provider stream. Provider observations cannot bypass Phase 1 normalization,
// duplicate suppression, ordering, quality, or readiness observation.
type LiveService struct {
	Normalizer Normalizer
	Observer   Observer
	Consumer   LiveConsumer
	orderer    *Orderer
	deduper    *Deduplicator
	mu         sync.Mutex
}

func NewLiveService(normalizer Normalizer, observer Observer, consumer LiveConsumer, allowedLateness time.Duration, capacity int) (*LiveService, error) {
	orderer, err := NewOrderer(allowedLateness, capacity)
	if err != nil || normalizer.Resolver == nil || consumer == nil {
		return nil, errors.Join(marketdata.ErrInvalidObservation, err)
	}
	return &LiveService{Normalizer: normalizer, Observer: observer, Consumer: consumer, orderer: orderer, deduper: NewDeduplicator(capacity)}, nil
}

func (s *LiveService) Accept(ctx context.Context, observation marketdata.Observation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	event, err := s.Normalizer.Normalize(ctx, observation)
	if err != nil {
		s.quality(observation, model.QualityMalformed, model.DispositionQuarantined, "NORMALIZATION_FAILED")
		return nil
	}
	if !s.deduper.First(event.ID()) {
		s.quality(observation, model.QualityDuplicate, model.DispositionSuppressed, "DUPLICATE_CANONICAL_EVENT")
		return nil
	}
	ready, disposition, err := s.orderer.Push(event)
	if err != nil {
		s.quality(observation, model.QualityMissing, model.DispositionQuarantined, "REORDER_CAPACITY_EXCEEDED")
		return err
	}
	switch disposition {
	case PushDuplicate:
		s.quality(observation, model.QualityDuplicate, model.DispositionSuppressed, "DUPLICATE_CANONICAL_EVENT")
	case PushLate:
		s.quality(observation, model.QualityLate, model.DispositionQuarantined, "EVENT_BEHIND_WATERMARK")
	}
	for _, accepted := range ready {
		if s.Observer != nil {
			s.Observer.Accepted(accepted)
		}
		if err := s.Consumer.Process(ctx, accepted); err != nil {
			return err
		}
	}
	return nil
}

func (s *LiveService) quality(observation marketdata.Observation, code model.QualityCode, disposition model.Disposition, reason string) {
	if s.Observer == nil {
		return
	}
	s.Observer.Quality(model.QualityRecord{Code: code, Disposition: disposition, Provider: observation.Provider, ExchangeTime: observation.ExchangeTime.UTC(), ObservedAt: observation.IngestedAt.UTC(), Reason: reason, SourcePosition: observation.SourcePosition})
}

// ObserverGroup fans accepted and quality events out to independent Phase 1
// observers. Observers remain non-authoritative and must not panic.
type ObserverGroup []Observer

func (g ObserverGroup) Accepted(event model.Event) {
	for _, observer := range g {
		if observer == nil {
			continue
		}
		func() { defer func() { _ = recover() }(); observer.Accepted(event) }()
	}
}
func (g ObserverGroup) Quality(record model.QualityRecord) {
	for _, observer := range g {
		if observer == nil {
			continue
		}
		func() { defer func() { _ = recover() }(); observer.Quality(record) }()
	}
}
