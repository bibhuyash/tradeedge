package model

import (
	"fmt"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

type BookLevel struct {
	Price    domain.Price
	Quantity int64
}

type QuoteSpec struct {
	InstrumentID domain.InstrumentID
	LastPrice    domain.Price
	BestBid      *BookLevel
	BestAsk      *BookLevel
	Volume       int64
	OpenInterest *int64
	ExchangeTime time.Time
	IngestedAt   time.Time
	Provenance   Provenance
}

type QuoteEvent struct {
	id           EventID
	instrumentID domain.InstrumentID
	lastPrice    domain.Price
	bestBid      *BookLevel
	bestAsk      *BookLevel
	volume       int64
	openInterest *int64
	exchangeTime time.Time
	ingestedAt   time.Time
	provenance   Provenance
}

func NewQuoteEvent(spec QuoteSpec) (QuoteEvent, error) {
	if spec.InstrumentID.IsZero() || spec.LastPrice.IsZeroValue() ||
		spec.ExchangeTime.IsZero() || spec.IngestedAt.IsZero() || spec.Volume < 0 ||
		spec.Provenance.Validate() != nil {
		return QuoteEvent{}, ErrInvalidEvent
	}
	if spec.OpenInterest != nil && *spec.OpenInterest < 0 {
		return QuoteEvent{}, ErrInvalidEvent
	}
	if err := validateLevel(spec.BestBid, spec.LastPrice.Currency()); err != nil {
		return QuoteEvent{}, err
	}
	if err := validateLevel(spec.BestAsk, spec.LastPrice.Currency()); err != nil {
		return QuoteEvent{}, err
	}
	payload := fmt.Sprintf("v1|%s|%s|%s|%s|%d|%s|%s|%d|%v",
		spec.Provenance.Provider, spec.Provenance.ProviderToken, spec.Provenance.MasterVersion,
		spec.ExchangeTime.UTC().Format(time.RFC3339Nano), spec.LastPrice.MinorUnits(),
		levelKey(spec.BestBid), levelKey(spec.BestAsk), spec.Volume, optionalInt64Key(spec.OpenInterest))
	if spec.Provenance.HasSequence {
		payload += fmt.Sprintf("|seq:%d", spec.Provenance.SourceSequence)
	}
	id, _ := NewEventID(payload)
	return QuoteEvent{
		id:           id,
		instrumentID: spec.InstrumentID,
		lastPrice:    spec.LastPrice,
		bestBid:      cloneLevel(spec.BestBid),
		bestAsk:      cloneLevel(spec.BestAsk),
		volume:       spec.Volume,
		openInterest: cloneInt64(spec.OpenInterest),
		exchangeTime: spec.ExchangeTime.UTC(),
		ingestedAt:   spec.IngestedAt.UTC(),
		provenance:   spec.Provenance,
	}, nil
}

func (q QuoteEvent) ID() EventID                       { return q.id }
func (q QuoteEvent) Kind() EventKind                   { return EventKindQuote }
func (q QuoteEvent) InstrumentID() domain.InstrumentID { return q.instrumentID }
func (q QuoteEvent) ExchangeTime() time.Time           { return q.exchangeTime }
func (q QuoteEvent) IngestedAt() time.Time             { return q.ingestedAt }
func (q QuoteEvent) Provenance() Provenance            { return q.provenance }
func (q QuoteEvent) LastPrice() domain.Price           { return q.lastPrice }
func (q QuoteEvent) BestBid() *BookLevel               { return cloneLevel(q.bestBid) }
func (q QuoteEvent) BestAsk() *BookLevel               { return cloneLevel(q.bestAsk) }
func (q QuoteEvent) Volume() int64                     { return q.volume }
func (q QuoteEvent) OpenInterest() *int64              { return cloneInt64(q.openInterest) }
func (QuoteEvent) eventMarker()                        {}

func validateLevel(level *BookLevel, currency domain.Currency) error {
	if level == nil {
		return nil
	}
	if level.Price.IsZeroValue() || level.Price.Currency() != currency || level.Quantity < 0 {
		return ErrInvalidEvent
	}
	return nil
}

func levelKey(level *BookLevel) string {
	if level == nil {
		return "-"
	}
	return fmt.Sprintf("%d:%d", level.Price.MinorUnits(), level.Quantity)
}

func cloneLevel(level *BookLevel) *BookLevel {
	if level == nil {
		return nil
	}
	copy := *level
	return &copy
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func optionalInt64Key(value *int64) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *value)
}
