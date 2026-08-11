package ingest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	"github.com/bibhuyash/tradeedge/internal/marketdata"
)

func TestNormalizerAcceptsObservationOnlyIndexWithZeroTick(t *testing.T) {
	at := time.Date(2026, 8, 11, 4, 0, 0, 0, time.UTC)
	instrument := observationOnlyIndex(t, "NIFTY", "NIFTY 50")
	normalizer := Normalizer{Resolver: resolverForInstruments(t, at, []domain.Instrument{instrument}, []domain.ProviderInstrumentRef{{
		Provider: "zerodha", Token: "256265", TradingSymbol: "NIFTY 50", InstrumentID: instrument.ID(), ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour),
	}})}

	event, err := normalizer.Normalize(context.Background(), quoteForToken("256265", at, 2450123))
	if err != nil {
		t.Fatalf("Normalize() observation-only index error = %v", err)
	}
	if event.InstrumentID() != instrument.ID() || event.ExchangeTime() != at {
		t.Fatalf("normalized event = %#v", event)
	}
}

func TestNormalizerPreservesPositiveTickAlignment(t *testing.T) {
	at := time.Date(2026, 8, 11, 4, 0, 0, 0, time.UTC)
	instrument := tradableIndex(t, "NIFTY", "NIFTY 50", 5)
	normalizer := Normalizer{Resolver: resolverForInstruments(t, at, []domain.Instrument{instrument}, []domain.ProviderInstrumentRef{{
		Provider: "zerodha", Token: "256265", TradingSymbol: "NIFTY 50", InstrumentID: instrument.ID(), ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour),
	}})}

	if _, err := normalizer.Normalize(context.Background(), quoteForToken("256265", at, 2450120)); err != nil {
		t.Fatalf("Normalize() aligned positive-tick quote error = %v", err)
	}
	if _, err := normalizer.Normalize(context.Background(), quoteForToken("256265", at, 2450123)); !errors.Is(err, marketdata.ErrInvalidObservation) {
		t.Fatalf("Normalize() misaligned quote error = %v, want ErrInvalidObservation", err)
	}
}

func observationOnlyIndex(t *testing.T, underlying domain.UnderlyingID, symbol string) domain.Instrument {
	t.Helper()
	tick, _ := domain.NewPrice(0, "INR")
	currency, _ := domain.NewCurrency("INR")
	instrument, err := domain.NewInstrument(domain.InstrumentSpec{
		Exchange: domain.ExchangeNSE, Segment: domain.SegmentIndex, UnderlyingID: underlying,
		Type: domain.InstrumentIndex, ExchangeSymbol: symbol, LotSize: 0, TickSize: tick, Currency: currency,
	})
	if err != nil {
		t.Fatal(err)
	}
	return instrument
}

func tradableIndex(t *testing.T, underlying domain.UnderlyingID, symbol string, tickMinor int64) domain.Instrument {
	t.Helper()
	tick, _ := domain.NewPrice(tickMinor, "INR")
	currency, _ := domain.NewCurrency("INR")
	instrument, err := domain.NewInstrument(domain.InstrumentSpec{
		Exchange: domain.ExchangeNSE, Segment: domain.SegmentIndex, UnderlyingID: underlying,
		Type: domain.InstrumentIndex, ExchangeSymbol: symbol, LotSize: 1, TickSize: tick, Currency: currency,
	})
	if err != nil {
		t.Fatal(err)
	}
	return instrument
}

func resolverForInstruments(t *testing.T, at time.Time, instruments []domain.Instrument, mappings []domain.ProviderInstrumentRef) instrumentmaster.Resolver {
	t.Helper()
	master, err := instrumentmaster.New(at.Add(-time.Hour), instruments, mappings)
	if err != nil {
		t.Fatal(err)
	}
	repository := instrumentmaster.NewMemoryRepository()
	if err := repository.Put(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	return instrumentmaster.Resolver{Repository: repository}
}

func quoteForToken(token string, at time.Time, lastMinor int64) marketdata.Observation {
	return marketdata.Observation{
		Kind: marketdata.ObservationQuote, Provider: "zerodha", ProviderToken: token,
		ExchangeTime: at, IngestedAt: at.Add(time.Millisecond), LastMinor: lastMinor, Currency: "INR",
	}
}
