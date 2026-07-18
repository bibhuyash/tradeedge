package file

import (
	"fmt"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

type eventRecord struct {
	ID           string               `json:"id"`
	Kind         model.EventKind      `json:"kind"`
	InstrumentID string               `json:"instrument_id"`
	ExchangeTime time.Time            `json:"exchange_time"`
	IngestedAt   time.Time            `json:"ingested_at"`
	Provenance   model.Provenance     `json:"provenance"`
	Currency     string               `json:"currency"`
	LastMinor    int64                `json:"last_minor,omitempty"`
	BidMinor     *int64               `json:"bid_minor,omitempty"`
	BidQuantity  int64                `json:"bid_quantity,omitempty"`
	AskMinor     *int64               `json:"ask_minor,omitempty"`
	AskQuantity  int64                `json:"ask_quantity,omitempty"`
	Volume       int64                `json:"volume"`
	OpenInterest *int64               `json:"open_interest,omitempty"`
	Interval     model.CandleInterval `json:"interval,omitempty"`
	OpenTime     time.Time            `json:"open_time,omitempty"`
	CloseTime    time.Time            `json:"close_time,omitempty"`
	OpenMinor    int64                `json:"open_minor,omitempty"`
	HighMinor    int64                `json:"high_minor,omitempty"`
	LowMinor     int64                `json:"low_minor,omitempty"`
	CloseMinor   int64                `json:"close_minor,omitempty"`
	EventCount   int64                `json:"event_count,omitempty"`
}

func recordFromEvent(event model.Event) (eventRecord, error) {
	record := eventRecord{
		ID:           event.ID().String(),
		Kind:         event.Kind(),
		InstrumentID: event.InstrumentID().String(),
		ExchangeTime: event.ExchangeTime().UTC(),
		IngestedAt:   event.IngestedAt().UTC(),
		Provenance:   event.Provenance(),
	}
	switch typed := event.(type) {
	case model.QuoteEvent:
		record.Currency = typed.LastPrice().Currency().String()
		record.LastMinor = typed.LastPrice().MinorUnits()
		record.Volume = typed.Volume()
		record.OpenInterest = typed.OpenInterest()
		if level := typed.BestBid(); level != nil {
			minor := level.Price.MinorUnits()
			record.BidMinor = &minor
			record.BidQuantity = level.Quantity
		}
		if level := typed.BestAsk(); level != nil {
			minor := level.Price.MinorUnits()
			record.AskMinor = &minor
			record.AskQuantity = level.Quantity
		}
	case model.CompletedCandleEvent:
		record.Currency = typed.Open().Currency().String()
		record.Interval = typed.Interval()
		record.OpenTime = typed.OpenTime()
		record.CloseTime = typed.CloseTime()
		record.OpenMinor = typed.Open().MinorUnits()
		record.HighMinor = typed.High().MinorUnits()
		record.LowMinor = typed.Low().MinorUnits()
		record.CloseMinor = typed.Close().MinorUnits()
		record.Volume = typed.Volume()
		record.EventCount = typed.EventCount()
		record.OpenInterest = typed.OpenInterest()
	default:
		return eventRecord{}, model.ErrInvalidEvent
	}
	return record, nil
}

func (r eventRecord) event() (model.Event, error) {
	id, err := domain.ParseInstrumentID(r.InstrumentID)
	if err != nil {
		return nil, err
	}
	switch r.Kind {
	case model.EventKindQuote:
		last, err := domain.NewPrice(r.LastMinor, r.Currency)
		if err != nil {
			return nil, err
		}
		bid, err := recordLevel(r.BidMinor, r.BidQuantity, r.Currency)
		if err != nil {
			return nil, err
		}
		ask, err := recordLevel(r.AskMinor, r.AskQuantity, r.Currency)
		if err != nil {
			return nil, err
		}
		event, err := model.NewQuoteEvent(model.QuoteSpec{
			InstrumentID: id, LastPrice: last, BestBid: bid, BestAsk: ask,
			Volume: r.Volume, OpenInterest: r.OpenInterest,
			ExchangeTime: r.ExchangeTime, IngestedAt: r.IngestedAt, Provenance: r.Provenance,
		})
		if err != nil || event.ID().String() != r.ID {
			return nil, fmt.Errorf("%w: quote event identity mismatch", model.ErrInvalidEvent)
		}
		return event, nil
	case model.EventKindCandle:
		open, _ := domain.NewPrice(r.OpenMinor, r.Currency)
		high, _ := domain.NewPrice(r.HighMinor, r.Currency)
		low, _ := domain.NewPrice(r.LowMinor, r.Currency)
		closePrice, _ := domain.NewPrice(r.CloseMinor, r.Currency)
		event, err := model.NewCompletedCandleEvent(model.CandleSpec{
			InstrumentID: id, Interval: r.Interval, OpenTime: r.OpenTime, CloseTime: r.CloseTime,
			Open: open, High: high, Low: low, Close: closePrice, Volume: r.Volume,
			EventCount: r.EventCount, OpenInterest: r.OpenInterest,
			IngestedAt: r.IngestedAt, Provenance: r.Provenance,
		})
		if err != nil || event.ID().String() != r.ID {
			return nil, fmt.Errorf("%w: candle event identity mismatch", model.ErrInvalidEvent)
		}
		return event, nil
	default:
		return nil, model.ErrInvalidEvent
	}
}

func recordLevel(minor *int64, quantity int64, currency string) (*model.BookLevel, error) {
	if minor == nil {
		return nil, nil
	}
	price, err := domain.NewPrice(*minor, currency)
	if err != nil {
		return nil, err
	}
	return &model.BookLevel{Price: price, Quantity: quantity}, nil
}

type qualityRecord struct {
	Code            model.QualityCode `json:"code"`
	Disposition     model.Disposition `json:"disposition"`
	Provider        domain.Provider   `json:"provider"`
	InstrumentID    string            `json:"instrument_id,omitempty"`
	ExchangeTime    time.Time         `json:"exchange_time,omitempty"`
	ObservedAt      time.Time         `json:"observed_at"`
	Reason          string            `json:"reason"`
	SourcePosition  int64             `json:"source_position,omitempty"`
	DatasetRevision string            `json:"dataset_revision,omitempty"`
}

func qualityRecordFromModel(record model.QualityRecord) qualityRecord {
	return qualityRecord{
		Code: record.Code, Disposition: record.Disposition, Provider: record.Provider,
		InstrumentID: record.InstrumentID.String(), ExchangeTime: record.ExchangeTime,
		ObservedAt: record.ObservedAt, Reason: record.Reason,
		SourcePosition: record.SourcePosition, DatasetRevision: record.DatasetRevision,
	}
}
