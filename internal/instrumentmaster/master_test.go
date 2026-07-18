package instrumentmaster

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

func TestMasterResolvesProviderTokensWithoutUsingThemAsCanonicalIDs(t *testing.T) {
	instrument := testInstrument(t, "NIFTY")
	at := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	master, err := New(at, []domain.Instrument{instrument}, []domain.ProviderInstrumentRef{{
		Provider: "fixture", Token: "256265", TradingSymbol: "NIFTY 50",
		InstrumentID: instrument.ID(), ValidFrom: at, ValidUntil: at.Add(24 * time.Hour),
	}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	resolved, err := master.Resolve("fixture", "256265", at.Add(time.Hour))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved != instrument.ID() || resolved.String() == "256265" {
		t.Fatalf("resolved ID = %s", resolved)
	}
	if _, err := master.Resolve("fixture", "unknown", at); !errors.Is(err, ErrInstrumentNotFound) {
		t.Fatalf("Resolve() error = %v, want ErrInstrumentNotFound", err)
	}

	repository := NewMemoryRepository()
	if err := repository.Put(context.Background(), master); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	current, err := repository.Current(context.Background())
	if err != nil || current.Version() != master.Version() {
		t.Fatalf("Current() = %s, %v", current.Version(), err)
	}
}

func testInstrument(t *testing.T, symbol string) domain.Instrument {
	t.Helper()
	underlying, _ := domain.NewUnderlyingID(symbol)
	lot, _ := domain.NewQuantity(1)
	tick, _ := domain.NewPrice(5, "INR")
	currency, _ := domain.NewCurrency("INR")
	instrument, err := domain.NewInstrument(domain.InstrumentSpec{
		Exchange: domain.ExchangeNSE, Segment: domain.SegmentIndex, UnderlyingID: underlying,
		Type: domain.InstrumentIndex, ExchangeSymbol: symbol, LotSize: lot, TickSize: tick, Currency: currency,
	})
	if err != nil {
		t.Fatalf("NewInstrument() error = %v", err)
	}
	return instrument
}
