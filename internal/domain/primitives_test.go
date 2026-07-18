package domain

import (
	"errors"
	"math"
	"testing"
)

func TestNewInstrument(t *testing.T) {
	underlying, _ := NewUnderlyingID("nifty")
	lot, _ := NewQuantity(50)
	tick, _ := NewPrice(5, "INR")
	currency, _ := NewCurrency("INR")
	instrument, err := NewInstrument(InstrumentSpec{
		Exchange:       ExchangeNSE,
		Segment:        SegmentIndex,
		UnderlyingID:   underlying,
		Type:           InstrumentIndex,
		ExchangeSymbol: " nifty ",
		LotSize:        lot,
		TickSize:       tick,
		Currency:       currency,
	})
	if err != nil {
		t.Fatalf("NewInstrument() error = %v", err)
	}
	if instrument.Exchange() != "NSE" || instrument.Symbol() != "NIFTY" {
		t.Fatalf("instrument was not normalized: %#v", instrument)
	}
	if instrument.ID().IsZero() {
		t.Fatal("instrument ID is zero")
	}
	if _, err := NewInstrument(InstrumentSpec{}); !errors.Is(err, ErrInvalidInstrument) {
		t.Fatalf("NewInstrument() error = %v, want ErrInvalidInstrument", err)
	}
}

func TestMoneyAdd(t *testing.T) {
	left, _ := NewMoney(-100, "inr")
	right, _ := NewMoney(250, "INR")
	sum, err := left.Add(right)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if sum.MinorUnits() != 150 || sum.Currency() != Currency("INR") {
		t.Fatalf("Add() = %v", sum)
	}

	usd, _ := NewMoney(1, "USD")
	if _, err := left.Add(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Add() error = %v, want ErrCurrencyMismatch", err)
	}
	max, _ := NewMoney(math.MaxInt64, "INR")
	one, _ := NewMoney(1, "INR")
	if _, err := max.Add(one); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatalf("Add() error = %v, want ErrMoneyOverflow", err)
	}
}

func TestQuantityAndPriceValidation(t *testing.T) {
	if _, err := NewQuantity(0); !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("NewQuantity() error = %v", err)
	}
	if _, err := NewQuantity(1); err != nil {
		t.Fatalf("NewQuantity() error = %v", err)
	}
	if _, err := NewPrice(-1, "INR"); !errors.Is(err, ErrInvalidPrice) {
		t.Fatalf("NewPrice() error = %v", err)
	}
	if _, err := NewPrice(0, "INR"); err != nil {
		t.Fatalf("NewPrice() error = %v", err)
	}
	if _, err := NewPrice(1, "rupees"); !errors.Is(err, ErrInvalidCurrency) {
		t.Fatalf("NewPrice() error = %v", err)
	}
}

func TestTypedIDsRejectEmptyValues(t *testing.T) {
	tests := []struct {
		name string
		new  func(string) error
	}{
		{"order", func(value string) error { _, err := NewOrderID(value); return err }},
		{"strategy", func(value string) error { _, err := NewStrategyID(value); return err }},
		{"account", func(value string) error { _, err := NewAccountID(value); return err }},
		{"client request", func(value string) error { _, err := NewClientRequestID(value); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.new(" "); !errors.Is(err, ErrInvalidID) {
				t.Fatalf("constructor error = %v, want ErrInvalidID", err)
			}
		})
	}
}
