package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/bibhuyash/tradeedge/internal/marketdata"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/storage"
	"github.com/bibhuyash/tradeedge/internal/marketdata/telemetry"
)

type Observer interface {
	Accepted(model.Event)
	Quality(model.QualityRecord)
}

type CompletenessDetector interface {
	Observe(model.Event)
	Missing(context.Context) ([]model.QualityRecord, error)
}

type Service struct {
	Normalizer      Normalizer
	AllowedLateness time.Duration
	BufferCapacity  int
	Observer        Observer
	Completeness    CompletenessDetector
	Telemetry       telemetry.Recorder
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
	emit := func(events []model.Event) error {
		for _, event := range events {
			if s.Completeness != nil {
				s.Completeness.Observe(event)
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
		started := time.Now()
		dimensions := telemetry.Dimensions{Provider: observation.Provider}
		s.recorder().Observation(dimensions, "received")
		event, err := s.Normalizer.Normalize(ctx, observation)
		s.recorder().Normalization(dimensions, time.Since(started))
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
			s.recorder().Quality(dimensions, record.Code, record.Disposition)
			return nil
		}
		dimensions.Kind = event.Kind()
		if candle, ok := event.(model.CompletedCandleEvent); ok {
			dimensions.Interval = candle.Interval()
		}
		s.recorder().TransportLag(dimensions, event.IngestedAt().Sub(event.ExchangeTime()))
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
			s.recorder().Quality(dimensions, record.Code, record.Disposition)
		case PushLate:
			record := qualityRecord(event, model.QualityLate, model.DispositionQuarantined,
				"event is older than committed watermark", observation.SourcePosition)
			if err := writer.RecordQuality(ctx, record); err != nil {
				return err
			}
			s.recordQuality(record)
			s.recorder().Quality(dimensions, record.Code, record.Disposition)
		}
		s.recorder().ReorderDepth(dimensions, orderer.Depth())
		return emit(ready)
	})
	if streamErr != nil {
		return fmt.Errorf("stream historical market data: %w", streamErr)
	}
	if err := emit(orderer.Flush()); err != nil {
		return err
	}
	if s.Completeness != nil {
		records, err := s.Completeness.Missing(ctx)
		if err != nil {
			return fmt.Errorf("evaluate market-data completeness: %w", err)
		}
		for _, record := range records {
			if err := writer.RecordQuality(ctx, record); err != nil {
				return err
			}
			s.recordQuality(record)
			s.recorder().MissingIntervals(telemetry.Dimensions{
				Provider: record.Provider, Kind: model.EventKindCandle,
			}, 1)
		}
	}
	return nil
}

func (s Service) recorder() telemetry.Recorder {
	if s.Telemetry == nil {
		return telemetry.NopRecorder{}
	}
	return s.Telemetry
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
