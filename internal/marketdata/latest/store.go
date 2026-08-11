// Package latest retains a bounded read-only view of the most recent accepted
// quote for each configured instrument. It is operational evidence only and
// has no trading authority.
package latest

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
)

var ErrInvalidConfiguration = errors.New("invalid latest-observation configuration")

type Observation struct {
	Provider          domain.Provider `json:"provider"`
	ProviderToken     string          `json:"provider_token"`
	InstrumentID      string          `json:"instrument_id"`
	Symbol            string          `json:"symbol"`
	Exchange          domain.Exchange `json:"exchange"`
	Segment           domain.Segment  `json:"segment"`
	Kind              model.EventKind `json:"event_kind"`
	LatestPriceMinor  int64           `json:"latest_price_minor"`
	Currency          domain.Currency `json:"currency"`
	ExchangeTimestamp time.Time       `json:"exchange_timestamp"`
	IngestedTimestamp time.Time       `json:"ingested_timestamp"`
}

type descriptor struct {
	provider   domain.Provider
	instrument domain.Instrument
}

// Store is bounded by the checksum-pinned watchlist supplied at construction.
type Store struct {
	mu      sync.RWMutex
	allowed map[domain.InstrumentID]descriptor
	values  map[domain.InstrumentID]Observation
}

func New(master instrumentmaster.Master, watchlist readiness.Watchlist) (*Store, error) {
	if len(watchlist.Requirements) == 0 || len(watchlist.Requirements) > 250 {
		return nil, ErrInvalidConfiguration
	}
	allowed := make(map[domain.InstrumentID]descriptor, len(watchlist.Requirements))
	for _, requirement := range watchlist.Requirements {
		instrument, found := master.Instrument(requirement.InstrumentID)
		if !found || requirement.EventKind != model.EventKindQuote || !requirement.Required ||
			instrument.Exchange() != requirement.Exchange || instrument.Segment() != requirement.Segment {
			return nil, ErrInvalidConfiguration
		}
		if _, duplicate := allowed[requirement.InstrumentID]; duplicate {
			return nil, ErrInvalidConfiguration
		}
		allowed[requirement.InstrumentID] = descriptor{provider: requirement.Provider, instrument: instrument}
	}
	return &Store{allowed: allowed, values: make(map[domain.InstrumentID]Observation, len(allowed))}, nil
}

// Accepted implements ingest.Observer. Only canonical quote events that have
// already passed normalization, deduplication, and ordering reach this method.
func (s *Store) Accepted(event model.Event) {
	quote, ok := event.(model.QuoteEvent)
	if !ok {
		return
	}
	descriptor, allowed := s.allowed[quote.InstrumentID()]
	if !allowed || quote.Provenance().Provider != descriptor.provider {
		return
	}
	value := Observation{
		Provider: quote.Provenance().Provider, ProviderToken: quote.Provenance().ProviderToken,
		InstrumentID: quote.InstrumentID().String(), Symbol: descriptor.instrument.Symbol(),
		Exchange: descriptor.instrument.Exchange(), Segment: descriptor.instrument.Segment(), Kind: quote.Kind(),
		LatestPriceMinor: quote.LastPrice().MinorUnits(), Currency: quote.LastPrice().Currency(),
		ExchangeTimestamp: quote.ExchangeTime().UTC(), IngestedTimestamp: quote.IngestedAt().UTC(),
	}
	s.mu.Lock()
	if current, found := s.values[quote.InstrumentID()]; !found || !value.ExchangeTimestamp.Before(current.ExchangeTimestamp) {
		s.values[quote.InstrumentID()] = value
	}
	s.mu.Unlock()
}

func (*Store) Quality(model.QualityRecord) {}

func (s *Store) Snapshot(context.Context) []Observation {
	s.mu.RLock()
	result := make([]Observation, 0, len(s.values))
	for _, value := range s.values {
		result = append(result, value)
	}
	s.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool { return result[i].InstrumentID < result[j].InstrumentID })
	return result
}
