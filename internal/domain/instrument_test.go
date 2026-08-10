package domain

import (
	"errors"
	"testing"
	"time"
)

func TestInstrumentConstruction(t *testing.T) {
	cases := []struct {
		name string
		spec func(t *testing.T) InstrumentSpec
	}{
		{"index", validIndexSpec},
		{"option", validOptionSpec},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, err := NewInstrument(tc.spec(t))
			if err != nil {
				t.Fatalf("NewInstrument() error = %v", err)
			}
			second, err := NewInstrument(tc.spec(t))
			if err != nil {
				t.Fatalf("NewInstrument() second error = %v", err)
			}
			if first.ID().IsZero() || first.ID() != second.ID() {
				t.Fatalf("instrument identity is not deterministic: %s != %s", first.ID(), second.ID())
			}
			if first.Symbol() == "" || first.TickSize().MinorUnits() <= 0 {
				t.Fatalf("invalid constructed instrument: %#v", first)
			}
		})
	}
}

func TestInvalidInstrumentCombinations(t *testing.T) {
	tests := map[string]func(InstrumentSpec) InstrumentSpec{
		"option in cash segment": func(spec InstrumentSpec) InstrumentSpec {
			spec.Segment = SegmentCash
			return spec
		},
		"option without derivative": func(spec InstrumentSpec) InstrumentSpec {
			spec.Derivative = nil
			return spec
		},
		"option without expiry": func(spec InstrumentSpec) InstrumentSpec {
			spec.Derivative.Expiry = CivilDate{}
			return spec
		},
		"option without side": func(spec InstrumentSpec) InstrumentSpec {
			spec.Derivative.OptionType = OptionNone
			return spec
		},
		"wrong currency tick": func(spec InstrumentSpec) InstrumentSpec {
			spec.TickSize, _ = NewPrice(5, "USD")
			return spec
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			spec := mutate(validOptionSpec(t))
			if _, err := NewInstrument(spec); !errors.Is(err, ErrInvalidInstrument) {
				t.Fatalf("NewInstrument() error = %v, want ErrInvalidInstrument", err)
			}
		})
	}
}

func TestObservationOnlyIndexAllowsZeroTradingMetadata(t *testing.T) {
	spec := validIndexSpec(t)
	spec.LotSize = 0
	spec.TickSize, _ = NewPrice(0, "INR")
	if _, err := NewInstrument(spec); err != nil {
		t.Fatalf("NewInstrument() observation-only index error = %v", err)
	}

	spec.Type = InstrumentEquity
	spec.Segment = SegmentCash
	if _, err := NewInstrument(spec); !errors.Is(err, ErrInvalidInstrument) {
		t.Fatalf("NewInstrument() accepted zero trading metadata for an equity: %v", err)
	}
}

func validIndexSpec(t *testing.T) InstrumentSpec {
	t.Helper()
	underlying, _ := NewUnderlyingID("NIFTY")
	lot, _ := NewQuantity(1)
	tick, _ := NewPrice(5, "INR")
	currency, _ := NewCurrency("INR")
	return InstrumentSpec{
		Exchange: ExchangeNSE, Segment: SegmentIndex, UnderlyingID: underlying,
		Type: InstrumentIndex, ExchangeSymbol: "NIFTY", LotSize: lot,
		TickSize: tick, Currency: currency,
	}
}

func validOptionSpec(t *testing.T) InstrumentSpec {
	t.Helper()
	spec := validIndexSpec(t)
	expiry, _ := NewCivilDate(2026, time.July, 30)
	strike, _ := NewPrice(2500000, "INR")
	lot, _ := NewQuantity(75)
	spec.Segment = SegmentOptions
	spec.Type = InstrumentOption
	spec.ExchangeSymbol = "NIFTY26JUL25000CE"
	spec.LotSize = lot
	spec.Derivative = &DerivativeSpec{Expiry: expiry, Strike: strike, OptionType: OptionCall}
	return spec
}
