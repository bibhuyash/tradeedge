package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/bibhuyash/tradeedge/internal/marketdata"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/storage"
)

type Observer interface {
	Accepted(model.Event)
	Quality(model.QualityRecord)
}

type Service struct {
	Normalizer      Normalizer
	AllowedLateness time.Duration
	BufferCapacity  int
	Observer        Observer
}

func (s Service) Ingest(
	ctx context.Context,
	source marketdata.Source,
	query marketdata.SourceQuery,
	writer storage.DatasetWriter,
) error {
	if source == nil || writer == nil {
		return fmt.Errorf("market-data source and writer are required")
	}
	orderer, err := NewOrderer(s.AllowedLateness, s.BufferCapacity)
	if err != nil {
		return err
	}
	lastCandleClose := make(map[string]time.Time)
	emit := func(events []model.Event) error {
		for _, event := range events {
			if candle, ok := event.(model.CompletedCandleEvent); ok {
				key := event.InstrumentID().String() + "|" + string(candle.Interval())
				if previous := lastCandleClose[key]; !previous.IsZero() && candle.OpenTime().After(previous) {
					record := qualityRecord(event, model.QualityMissing, model.DispositionQuarantined,
						"completed candle interval gap", 0)
					if err := writer.RecordQuality(ctx, record); err != nil {
						return err
					}
					s.recordQuality(record)
				}
				lastCandleClose[key] = candle.CloseTime()
			}
			if err := writer.Append(ctx, event); err != nil {
				return err
			}
			if s.Observer != nil {
				s.Observer.Accepted(event)
			}
		}
		return nil
	}

	streamErr := source.Stream(ctx, query, func(ctx context.Context, observation marketdata.Observation) error {
		event, err := s.Normalizer.Normalize(ctx, observation)
		if err != nil {
			record := model.QualityRecord{
				Code:           model.QualityMalformed,
				Disposition:    model.DispositionQuarantined,
				Provider:       observation.Provider,
				ExchangeTime:   observation.ExchangeTime.UTC(),
				ObservedAt:     observation.IngestedAt.UTC(),
				Reason:         err.Error(),
				SourcePosition: observation.SourcePosition,
			}
			if writeErr := writer.RecordQuality(ctx, record); writeErr != nil {
				return writeErr
			}
			s.recordQuality(record)
			return nil
		}
		if !query.Includes(event.InstrumentID(), event.ExchangeTime()) {
			return nil
		}
		ready, disposition, err := orderer.Push(event)
		if err != nil {
			return err
		}
		switch disposition {
		case PushDuplicate:
			record := qualityRecord(event, model.QualityDuplicate, model.DispositionSuppressed,
				"duplicate canonical event", observation.SourcePosition)
			if err := writer.RecordQuality(ctx, record); err != nil {
				return err
			}
			s.recordQuality(record)
		case PushLate:
			record := qualityRecord(event, model.QualityLate, model.DispositionQuarantined,
				"event is older than committed watermark", observation.SourcePosition)
			if err := writer.RecordQuality(ctx, record); err != nil {
				return err
			}
			s.recordQuality(record)
		}
		return emit(ready)
	})
	if streamErr != nil {
		return fmt.Errorf("stream historical market data: %w", streamErr)
	}
	return emit(orderer.Flush())
}

func (s Service) recordQuality(record model.QualityRecord) {
	if s.Observer != nil {
		s.Observer.Quality(record)
	}
}

func qualityRecord(
	event model.Event,
	code model.QualityCode,
	disposition model.Disposition,
	reason string,
	position int64,
) model.QualityRecord {
	return model.QualityRecord{
		Code:            code,
		Disposition:     disposition,
		Provider:        event.Provenance().Provider,
		InstrumentID:    event.InstrumentID(),
		ExchangeTime:    event.ExchangeTime(),
		ObservedAt:      event.IngestedAt(),
		Reason:          reason,
		SourcePosition:  position,
		DatasetRevision: event.Provenance().DatasetRevision,
	}
}
