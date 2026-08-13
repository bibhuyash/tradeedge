// Package shadowruntime composes live read-only market observations into the
// Phase 8 qualification pipeline. It deliberately has no execution dependency.
package shadowruntime

import (
	"errors"
	"sort"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/qualification"
)

var (
	ErrInvalid       = errors.New("invalid live shadow value")
	ErrDuplicate     = errors.New("duplicate live shadow observation")
	ErrOutOfOrder    = errors.New("out-of-order live shadow observation")
	ErrNotReady      = errors.New("live shadow input not ready")
	ErrAuthorization = errors.New("live shadow authorization mismatch")
)

const (
	SchemaVersion       = "phase8-m4-live-shadow-runtime/v1"
	CandlePolicyVersion = "exchange-time-completed-1m/v1"
	MaximumCandles      = 64
)

type CandlePoint struct {
	EventID        string                   `json:"event_id"`
	InstrumentID   string                   `json:"instrument_id"`
	OpenTime       time.Time                `json:"open_time"`
	CloseTime      time.Time                `json:"close_time"`
	OpenMinor      int64                    `json:"open_minor"`
	HighMinor      int64                    `json:"high_minor"`
	LowMinor       int64                    `json:"low_minor"`
	CloseMinor     int64                    `json:"close_minor"`
	Ticks          uint64                   `json:"ticks"`
	LastExchangeAt time.Time                `json:"last_exchange_at"`
	SourceEventIDs []marketmodel.EventID    `json:"-"`
	Underlying     qualification.Underlying `json:"underlying"`
}

type CandleSeriesSnapshot struct {
	Underlying      qualification.Underlying `json:"underlying"`
	InstrumentID    string                   `json:"instrument_id"`
	Open            *CandlePoint             `json:"open,omitempty"`
	Completed       []CandlePoint            `json:"completed"`
	LastEventID     string                   `json:"last_event_id,omitempty"`
	LastExchangeAt  time.Time                `json:"last_exchange_at,omitempty"`
	DuplicateTicks  uint64                   `json:"duplicate_ticks"`
	OutOfOrderTicks uint64                   `json:"out_of_order_ticks"`
	GapMinutes      uint64                   `json:"gap_minutes"`
}

type CandleAggregator struct {
	series map[qualification.Underlying]*CandleSeriesSnapshot
}

func NewCandleAggregator(spots map[qualification.Underlying]domain.InstrumentID) (*CandleAggregator, error) {
	if len(spots) != 2 || spots[qualification.NIFTY].IsZero() || spots[qualification.BANKNIFTY].IsZero() || spots[qualification.NIFTY] == spots[qualification.BANKNIFTY] {
		return nil, ErrInvalid
	}
	return &CandleAggregator{series: map[qualification.Underlying]*CandleSeriesSnapshot{
		qualification.NIFTY:     {Underlying: qualification.NIFTY, InstrumentID: spots[qualification.NIFTY].String()},
		qualification.BANKNIFTY: {Underlying: qualification.BANKNIFTY, InstrumentID: spots[qualification.BANKNIFTY].String()},
	}}, nil
}

func (a *CandleAggregator) Accept(underlying qualification.Underlying, quote marketmodel.QuoteEvent) (*CandlePoint, error) {
	series := a.series[underlying]
	if series == nil || quote.InstrumentID().String() != series.InstrumentID || quote.ExchangeTime().IsZero() || quote.LastPrice().MinorUnits() <= 0 {
		return nil, ErrInvalid
	}
	eventID := quote.ID().String()
	if eventID == series.LastEventID {
		series.DuplicateTicks++
		return nil, ErrDuplicate
	}
	if !series.LastExchangeAt.IsZero() && !quote.ExchangeTime().After(series.LastExchangeAt) {
		series.OutOfOrderTicks++
		return nil, ErrOutOfOrder
	}
	minute := quote.ExchangeTime().UTC().Truncate(time.Minute)
	price := quote.LastPrice().MinorUnits()
	series.LastEventID, series.LastExchangeAt = eventID, quote.ExchangeTime().UTC()
	if series.Open == nil {
		series.Open = newCandlePoint(underlying, quote.InstrumentID(), quote.ID(), minute, price, quote.ExchangeTime())
		return nil, nil
	}
	if minute.Equal(series.Open.OpenTime) {
		updateCandlePoint(series.Open, quote.ID(), price, quote.ExchangeTime())
		return nil, nil
	}
	if minute.Before(series.Open.OpenTime) {
		series.OutOfOrderTicks++
		return nil, ErrOutOfOrder
	}
	completed := *series.Open
	completed.CloseTime = completed.OpenTime.Add(time.Minute)
	if gap := int(minute.Sub(completed.OpenTime)/time.Minute) - 1; gap > 0 {
		series.GapMinutes += uint64(gap)
	}
	series.Completed = append(series.Completed, completed)
	if len(series.Completed) > MaximumCandles {
		series.Completed = append([]CandlePoint(nil), series.Completed[len(series.Completed)-MaximumCandles:]...)
	}
	series.Open = newCandlePoint(underlying, quote.InstrumentID(), quote.ID(), minute, price, quote.ExchangeTime())
	copy := completed
	return &copy, nil
}

func newCandlePoint(underlying qualification.Underlying, instrument domain.InstrumentID, event marketmodel.EventID, minute time.Time, price int64, at time.Time) *CandlePoint {
	return &CandlePoint{EventID: event.String(), InstrumentID: instrument.String(), OpenTime: minute, OpenMinor: price, HighMinor: price, LowMinor: price, CloseMinor: price, Ticks: 1, LastExchangeAt: at.UTC(), SourceEventIDs: []marketmodel.EventID{event}, Underlying: underlying}
}

func updateCandlePoint(value *CandlePoint, event marketmodel.EventID, price int64, at time.Time) {
	if price > value.HighMinor {
		value.HighMinor = price
	}
	if price < value.LowMinor {
		value.LowMinor = price
	}
	value.CloseMinor, value.LastExchangeAt, value.EventID = price, at.UTC(), event.String()
	value.Ticks++
	value.SourceEventIDs = append(value.SourceEventIDs, event)
}

func (a *CandleAggregator) Snapshot() []CandleSeriesSnapshot {
	values := make([]CandleSeriesSnapshot, 0, 2)
	for _, underlying := range []qualification.Underlying{qualification.NIFTY, qualification.BANKNIFTY} {
		value := *a.series[underlying]
		if value.Open != nil {
			copy := *value.Open
			copy.SourceEventIDs = append([]marketmodel.EventID(nil), value.Open.SourceEventIDs...)
			value.Open = &copy
		}
		value.Completed = append([]CandlePoint(nil), value.Completed...)
		values = append(values, value)
	}
	return values
}

func (a *CandleAggregator) Restore(values []CandleSeriesSnapshot) error {
	if len(values) != 2 {
		return ErrInvalid
	}
	restored := map[qualification.Underlying]*CandleSeriesSnapshot{}
	for _, value := range values {
		if (value.Underlying != qualification.NIFTY && value.Underlying != qualification.BANKNIFTY) || value.InstrumentID == "" || restored[value.Underlying] != nil || len(value.Completed) > MaximumCandles {
			return ErrInvalid
		}
		copy := value
		copy.Completed = append([]CandlePoint(nil), value.Completed...)
		if value.Open != nil {
			open := *value.Open
			copy.Open = &open
		}
		restored[value.Underlying] = &copy
	}
	if restored[qualification.NIFTY] == nil || restored[qualification.BANKNIFTY] == nil {
		return ErrInvalid
	}
	a.series = restored
	return nil
}

func sortedEventIDs(values []marketmodel.EventID) []marketmodel.EventID {
	result := append([]marketmodel.EventID(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result
}
