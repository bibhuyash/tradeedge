package model

import (
	"errors"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

func TestDatasetOrdersAndRejectsDuplicateObservations(t *testing.T) {
	instrument := testInstrument(t, time.Unix(1, 0))
	late := testObservation(t, instrument.ID(), time.Unix(3, 0), 102, 101, 103)
	early := testObservation(t, instrument.ID(), time.Unix(2, 0), 100, 99, 101)
	dataset, err := NewDataset(DatasetSpec{SchemaVersion: DatasetSchemaV1, Version: "synthetic/v1", Instruments: []ResearchInstrument{instrument}, Observations: []Observation{late, early}})
	if err != nil {
		t.Fatal(err)
	}
	if got := dataset.Observations(); len(got) != 2 || got[0].ID() != early.ID() {
		t.Fatalf("observations not deterministically ordered: %#v", got)
	}
	_, err = NewDataset(DatasetSpec{SchemaVersion: DatasetSchemaV1, Version: "synthetic/v1", Instruments: []ResearchInstrument{instrument}, Observations: []Observation{early, early}})
	if !errors.Is(err, ErrDuplicateEvent) {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestObservationValidation(t *testing.T) {
	instrument := testInstrument(t, time.Unix(1, 0))
	id := instrument.ID()
	price, _ := domain.NewPrice(100, "INR")
	bid, _ := domain.NewPrice(102, "INR")
	ask, _ := domain.NewPrice(101, "INR")
	if _, err := NewObservation(ObservationSpec{InstrumentID: id, ExchangeTime: time.Unix(2, 0), Price: price, Bid: &bid, Ask: &ask}); !errors.Is(err, ErrInvalidObservation) {
		t.Fatalf("crossed book error = %v", err)
	}
	observation := testObservation(t, id, time.Unix(0, 0), 100, 99, 101)
	_, err := NewDataset(DatasetSpec{SchemaVersion: DatasetSchemaV1, Version: "v1", Instruments: []ResearchInstrument{instrument}, Observations: []Observation{observation}})
	if !errors.Is(err, ErrInvalidDataset) {
		t.Fatalf("metadata time error = %v", err)
	}
	if _, err := NewObservation(ObservationSpec{InstrumentID: id, Price: price}); !errors.Is(err, ErrInvalidObservation) {
		t.Fatalf("zero timestamp error = %v", err)
	}
}

func TestCheckedArithmeticOverflow(t *testing.T) {
	if _, err := CheckedMul(1<<62, 4); !errors.Is(err, ErrArithmeticOverflow) {
		t.Fatalf("overflow error = %v", err)
	}
	if got, err := MulDiv(101, 1, 100, true); err != nil || got != 2 {
		t.Fatalf("MulDiv = %d, %v", got, err)
	}
}

func testInstrument(t *testing.T, available time.Time) ResearchInstrument {
	t.Helper()
	underlying, _ := domain.NewUnderlyingID("TEST")
	lot, _ := domain.NewQuantity(1)
	tick, _ := domain.NewPrice(1, "INR")
	currency, _ := domain.NewCurrency("INR")
	value, err := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentCash, UnderlyingID: underlying, Type: domain.InstrumentEquity, ExchangeSymbol: "TEST", LotSize: lot, TickSize: tick, Currency: currency})
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewResearchInstrument(InstrumentSpec{Instrument: value, AvailableFrom: available})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func testObservation(t *testing.T, id domain.InstrumentID, at time.Time, last, bid, ask int64) Observation {
	t.Helper()
	p, _ := domain.NewPrice(last, "INR")
	b, _ := domain.NewPrice(bid, "INR")
	a, _ := domain.NewPrice(ask, "INR")
	value, err := NewObservation(ObservationSpec{InstrumentID: id, ExchangeTime: at, Price: p, Bid: &b, Ask: &a, Volume: 1})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
