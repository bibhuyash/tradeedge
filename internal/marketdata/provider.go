package marketdata

import (
	"context"
	"errors"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

var ErrInvalidObservation = errors.New("invalid provider observation")

type SourceMode string

const (
	SourceHistorical SourceMode = "HISTORICAL"
	SourceFollow     SourceMode = "FOLLOW"
)

type SourceQuery struct {
	InstrumentIDs []domain.InstrumentID
	Start         time.Time
	End           time.Time
	Mode          SourceMode
}

func (q SourceQuery) Includes(id domain.InstrumentID, at time.Time) bool {
	if !q.Start.IsZero() && at.Before(q.Start) {
		return false
	}
	if !q.End.IsZero() && !at.Before(q.End) {
		return false
	}
	if len(q.InstrumentIDs) == 0 {
		return true
	}
	for _, candidate := range q.InstrumentIDs {
		if candidate == id {
			return true
		}
	}
	return false
}

type ObservationKind string

const (
	ObservationQuote  ObservationKind = "QUOTE"
	ObservationCandle ObservationKind = "COMPLETED_CANDLE"
)

type Observation struct {
	Kind           ObservationKind
	Provider       domain.Provider
	ProviderToken  string
	ExchangeTime   time.Time
	IngestedAt     time.Time
	SourceSequence uint64
	HasSequence    bool
	LastMinor      int64
	BidMinor       *int64
	BidQuantity    int64
	AskMinor       *int64
	AskQuantity    int64
	OpenMinor      int64
	HighMinor      int64
	LowMinor       int64
	CloseMinor     int64
	Volume         int64
	OpenInterest   *int64
	EventCount     int64
	Interval       model.CandleInterval
	OpenTime       time.Time
	CloseTime      time.Time
	Currency       string
	SourcePosition int64
}

type ObservationSink func(context.Context, Observation) error

type Source interface {
	Stream(ctx context.Context, query SourceQuery, sink ObservationSink) error
}

type InstrumentResolver interface {
	ResolveProviderRef(
		ctx context.Context,
		provider domain.Provider,
		token string,
		exchangeTime time.Time,
	) (domain.Instrument, string, error)
}
