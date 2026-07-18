package model

import (
	"fmt"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

type CandleInterval string

const (
	Interval1Minute   CandleInterval = "1m"
	Interval5Minutes  CandleInterval = "5m"
	Interval15Minutes CandleInterval = "15m"
	Interval1Day      CandleInterval = "1d"
)

func (i CandleInterval) Duration() (time.Duration, bool) {
	switch i {
	case Interval1Minute:
		return time.Minute, true
	case Interval5Minutes:
		return 5 * time.Minute, true
	case Interval15Minutes:
		return 15 * time.Minute, true
	case Interval1Day:
		return 0, true
	default:
		return 0, false
	}
}

type CandleSpec struct {
	InstrumentID domain.InstrumentID
	Interval     CandleInterval
	OpenTime     time.Time
	CloseTime    time.Time
	Open         domain.Price
	High         domain.Price
	Low          domain.Price
	Close        domain.Price
	Volume       int64
	EventCount   int64
	OpenInterest *int64
	IngestedAt   time.Time
	Provenance   Provenance
}

type CompletedCandleEvent struct {
	id           EventID
	instrumentID domain.InstrumentID
	interval     CandleInterval
	openTime     time.Time
	closeTime    time.Time
	open         domain.Price
	high         domain.Price
	low          domain.Price
	close        domain.Price
	volume       int64
	eventCount   int64
	openInterest *int64
	ingestedAt   time.Time
	provenance   Provenance
}

func NewCompletedCandleEvent(spec CandleSpec) (CompletedCandleEvent, error) {
	duration, validInterval := spec.Interval.Duration()
	if spec.InstrumentID.IsZero() || !validInterval || !spec.OpenTime.Before(spec.CloseTime) ||
		spec.IngestedAt.IsZero() || spec.Volume < 0 || spec.EventCount < 0 ||
		spec.Provenance.Validate() != nil {
		return CompletedCandleEvent{}, ErrInvalidEvent
	}
	if spec.Interval != Interval1Day && spec.CloseTime.Sub(spec.OpenTime) != duration {
		return CompletedCandleEvent{}, ErrInvalidEvent
	}
	if spec.OpenInterest != nil && *spec.OpenInterest < 0 {
		return CompletedCandleEvent{}, ErrInvalidEvent
	}
	currency := spec.Open.Currency()
	prices := []domain.Price{spec.Open, spec.High, spec.Low, spec.Close}
	for _, price := range prices {
		if price.IsZeroValue() || price.Currency() != currency {
			return CompletedCandleEvent{}, ErrCurrencyMismatch
		}
	}
	open, high, low, close := spec.Open.MinorUnits(), spec.High.MinorUnits(), spec.Low.MinorUnits(), spec.Close.MinorUnits()
	if high < open || high < close || high < low || low > open || low > close || low > high {
		return CompletedCandleEvent{}, ErrInvalidEvent
	}
	payload := fmt.Sprintf("v1|%s|%s|%s|%s|%s|%s|%d|%d|%d|%d|%d|%d|%v",
		spec.Provenance.Provider, spec.Provenance.ProviderToken, spec.Provenance.MasterVersion,
		spec.Interval, spec.OpenTime.UTC().Format(time.RFC3339Nano),
		spec.CloseTime.UTC().Format(time.RFC3339Nano), open, high, low, close,
		spec.Volume, spec.EventCount, optionalInt64Key(spec.OpenInterest))
	if spec.Provenance.HasSequence {
		payload += fmt.Sprintf("|seq:%d", spec.Provenance.SourceSequence)
	}
	id, _ := NewEventID(payload)
	return CompletedCandleEvent{
		id:           id,
		instrumentID: spec.InstrumentID,
		interval:     spec.Interval,
		openTime:     spec.OpenTime.UTC(),
		closeTime:    spec.CloseTime.UTC(),
		open:         spec.Open,
		high:         spec.High,
		low:          spec.Low,
		close:        spec.Close,
		volume:       spec.Volume,
		eventCount:   spec.EventCount,
		openInterest: cloneInt64(spec.OpenInterest),
		ingestedAt:   spec.IngestedAt.UTC(),
		provenance:   spec.Provenance,
	}, nil
}

func (c CompletedCandleEvent) ID() EventID                       { return c.id }
func (c CompletedCandleEvent) Kind() EventKind                   { return EventKindCandle }
func (c CompletedCandleEvent) InstrumentID() domain.InstrumentID { return c.instrumentID }
func (c CompletedCandleEvent) ExchangeTime() time.Time           { return c.closeTime }
func (c CompletedCandleEvent) IngestedAt() time.Time             { return c.ingestedAt }
func (c CompletedCandleEvent) Provenance() Provenance            { return c.provenance }
func (c CompletedCandleEvent) Interval() CandleInterval          { return c.interval }
func (c CompletedCandleEvent) OpenTime() time.Time               { return c.openTime }
func (c CompletedCandleEvent) CloseTime() time.Time              { return c.closeTime }
func (c CompletedCandleEvent) Open() domain.Price                { return c.open }
func (c CompletedCandleEvent) High() domain.Price                { return c.high }
func (c CompletedCandleEvent) Low() domain.Price                 { return c.low }
func (c CompletedCandleEvent) Close() domain.Price               { return c.close }
func (c CompletedCandleEvent) Volume() int64                     { return c.volume }
func (c CompletedCandleEvent) EventCount() int64                 { return c.eventCount }
func (c CompletedCandleEvent) OpenInterest() *int64              { return cloneInt64(c.openInterest) }
func (CompletedCandleEvent) eventMarker()                        {}

type InProgressCandleSnapshot struct {
	InstrumentID domain.InstrumentID
	Interval     CandleInterval
	OpenTime     time.Time
	ObservedAt   time.Time
	Open         domain.Price
	High         domain.Price
	Low          domain.Price
	Last         domain.Price
	Volume       int64
	EventCount   int64
}
