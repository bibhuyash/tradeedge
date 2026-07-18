package model

import (
	"errors"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

func TestCompletedCandleValidation(t *testing.T) {
	valid := validCandleSpec(t)
	candle, err := NewCompletedCandleEvent(valid)
	if err != nil {
		t.Fatalf("NewCompletedCandleEvent() error = %v", err)
	}
	if candle.OpenTime() != valid.OpenTime.UTC() || candle.CloseTime() != valid.CloseTime.UTC() {
		t.Fatalf("candle times changed: %#v", candle)
	}

	tests := map[string]func(CandleSpec) CandleSpec{
		"open after close": func(spec CandleSpec) CandleSpec {
			spec.OpenTime = spec.CloseTime
			return spec
		},
		"high below open": func(spec CandleSpec) CandleSpec {
			spec.High, _ = domain.NewPrice(9900, "INR")
			return spec
		},
		"low above close": func(spec CandleSpec) CandleSpec {
			spec.Low, _ = domain.NewPrice(10200, "INR")
			return spec
		},
		"negative volume": func(spec CandleSpec) CandleSpec {
			spec.Volume = -1
			return spec
		},
		"wrong interval width": func(spec CandleSpec) CandleSpec {
			spec.CloseTime = spec.CloseTime.Add(time.Second)
			return spec
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewCompletedCandleEvent(mutate(valid)); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("error = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

func TestEqualTimestampOrderingUsesEventID(t *testing.T) {
	left := validQuote(t, 10000, 0)
	right := validQuote(t, 10100, 0)
	if EventLess(left, right) == EventLess(right, left) {
		t.Fatal("equal-timestamp events do not have a stable total order")
	}
}

func TestEventIDIsStableAcrossOpenInterestAllocations(t *testing.T) {
	firstValue, secondValue := int64(123), int64(123)
	firstSpec := validCandleSpec(t)
	firstSpec.OpenInterest = &firstValue
	secondSpec := validCandleSpec(t)
	secondSpec.OpenInterest = &secondValue
	first, err := NewCompletedCandleEvent(firstSpec)
	if err != nil {
		t.Fatalf("first NewCompletedCandleEvent() error = %v", err)
	}
	second, err := NewCompletedCandleEvent(secondSpec)
	if err != nil {
		t.Fatalf("second NewCompletedCandleEvent() error = %v", err)
	}
	if first.ID() != second.ID() {
		t.Fatalf("event IDs differ across pointer allocations: %s != %s", first.ID(), second.ID())
	}
}

func validCandleSpec(t *testing.T) CandleSpec {
	t.Helper()
	id, _ := domain.InstrumentIDFromCanonicalKey("instrument")
	open, _ := domain.NewPrice(10000, "INR")
	high, _ := domain.NewPrice(10200, "INR")
	low, _ := domain.NewPrice(9900, "INR")
	closePrice, _ := domain.NewPrice(10100, "INR")
	start := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	return CandleSpec{
		InstrumentID: id, Interval: Interval1Minute, OpenTime: start,
		CloseTime: start.Add(time.Minute), Open: open, High: high, Low: low, Close: closePrice,
		Volume: 10, EventCount: 5, IngestedAt: start.Add(time.Minute + time.Second),
		Provenance: Provenance{Provider: "fixture", ProviderToken: "1", MasterVersion: "v1"},
	}
}

func validQuote(t *testing.T, minor int64, sequence uint64) QuoteEvent {
	t.Helper()
	id, _ := domain.InstrumentIDFromCanonicalKey("instrument")
	price, _ := domain.NewPrice(minor, "INR")
	at := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	event, err := NewQuoteEvent(QuoteSpec{
		InstrumentID: id, LastPrice: price, ExchangeTime: at, IngestedAt: at.Add(time.Millisecond),
		Provenance: Provenance{
			Provider: "fixture", ProviderToken: "1", MasterVersion: "v1",
			SourceSequence: sequence, HasSequence: sequence > 0,
		},
	})
	if err != nil {
		t.Fatalf("NewQuoteEvent() error = %v", err)
	}
	return event
}
