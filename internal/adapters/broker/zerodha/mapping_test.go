package zerodha

import (
	"errors"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
)

func TestMapperSuccessMissingStaleAndExpiredDerivative(t *testing.T) {
	at := time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	instrument := optionInstrument(t, 2026, time.August, 27)
	master, err := instrumentmaster.New(at, []domain.Instrument{instrument}, []domain.ProviderInstrumentRef{{
		Provider: Provider, Token: "123", TradingSymbol: "NIFTY26AUG25000CE", InstrumentID: instrument.ID(), ValidFrom: at, ValidUntil: at.Add(24 * time.Hour),
	}})
	if err != nil {
		t.Fatalf("instrumentmaster.New() error = %v", err)
	}
	mapper, _ := NewMapper(master, 12*time.Hour, &fixedClock{now: at}, nil)
	resolved, err := mapper.ResolveCanonical(instrument.ID(), at.Add(time.Hour))
	if err != nil || resolved.Token != "123" || resolved.InstrumentID != instrument.ID() {
		t.Fatalf("ResolveCanonical() = %#v, %v", resolved, err)
	}
	reverse, err := mapper.ResolveToken("123", at.Add(time.Hour))
	if err != nil || reverse.InstrumentID != instrument.ID() {
		t.Fatalf("ResolveToken() = %#v, %v", reverse, err)
	}
	missing, _ := domain.InstrumentIDFromCanonicalKey("missing")
	if _, err = mapper.ResolveCanonical(missing, at.Add(time.Hour)); !errors.Is(err, ErrMappingMissing) {
		t.Fatalf("missing mapping error = %v", err)
	}
	if _, err = mapper.ResolveToken("123", at.Add(13*time.Hour)); !errors.Is(err, ErrMappingStale) {
		t.Fatalf("stale mapping error = %v", err)
	}
	mapper, _ = NewMapper(master, 60*24*time.Hour, &fixedClock{now: at}, nil)
	if _, err = mapper.ResolveCanonical(instrument.ID(), time.Date(2026, 8, 28, 3, 0, 0, 0, time.UTC)); !errors.Is(err, ErrDerivativeExpired) {
		t.Fatalf("expired derivative error = %v", err)
	}
}

func TestInstrumentMasterRejectsAmbiguousAndDuplicateProviderMappings(t *testing.T) {
	at := time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	first := optionInstrument(t, 2026, time.August, 27)
	second := optionInstrumentWithStrike(t, 2600000)
	base := domain.ProviderInstrumentRef{Provider: Provider, Token: "duplicate", TradingSymbol: "FIRST", InstrumentID: first.ID(), ValidFrom: at, ValidUntil: at.Add(24 * time.Hour)}
	duplicate := base
	duplicate.InstrumentID = second.ID()
	duplicate.TradingSymbol = "SECOND"
	if _, err := instrumentmaster.New(at, []domain.Instrument{first, second}, []domain.ProviderInstrumentRef{base, duplicate}); !errors.Is(err, instrumentmaster.ErrAmbiguousMapping) {
		t.Fatalf("duplicate provider token error = %v", err)
	}
	secondToken := base
	secondToken.Token = "other"
	if _, err := instrumentmaster.New(at, []domain.Instrument{first}, []domain.ProviderInstrumentRef{base, secondToken}); !errors.Is(err, instrumentmaster.ErrAmbiguousMapping) {
		t.Fatalf("ambiguous canonical mapping error = %v", err)
	}
}

func optionInstrument(t *testing.T, year int, month time.Month, day int) domain.Instrument {
	t.Helper()
	expiry, _ := domain.NewCivilDate(year, month, day)
	return makeOption(t, expiry, 2500000)
}

func optionInstrumentWithStrike(t *testing.T, strikeValue int64) domain.Instrument {
	t.Helper()
	expiry, _ := domain.NewCivilDate(2026, time.August, 27)
	return makeOption(t, expiry, strikeValue)
}

func makeOption(t *testing.T, expiry domain.CivilDate, strikeValue int64) domain.Instrument {
	t.Helper()
	underlying, _ := domain.NewUnderlyingID("NIFTY")
	lot, _ := domain.NewQuantity(65)
	tick, _ := domain.NewPrice(5, "INR")
	strike, _ := domain.NewPrice(strikeValue, "INR")
	currency, _ := domain.NewCurrency("INR")
	instrument, err := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentOptions, UnderlyingID: underlying, Type: domain.InstrumentOption, ExchangeSymbol: "NIFTY", LotSize: lot, TickSize: tick, Currency: currency, Derivative: &domain.DerivativeSpec{Expiry: expiry, Strike: strike, OptionType: domain.OptionCall}})
	if err != nil {
		t.Fatalf("NewInstrument() error = %v", err)
	}
	return instrument
}
