package features

import (
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/research/model"
)

func TestPointInTimeFrameCannotContainFutureObservation(t *testing.T) {
	instrument := fixtureInstrument(t, time.Unix(1, 0))
	past := fixtureObservation(t, instrument.ID(), time.Unix(2, 0), 100)
	future := fixtureObservation(t, instrument.ID(), time.Unix(3, 0), 200)
	if _, err := NewPointInTimeView(time.Unix(2, 0), instrument, []model.Observation{past, future}); err == nil {
		t.Fatal("future observation accepted")
	}
	view, _ := NewPointInTimeView(time.Unix(2, 0), instrument, []model.Observation{past})
	frame, err := (Engine{}).Build(view)
	if err != nil {
		t.Fatal(err)
	}
	price, _ := frame.Value(FeatureLastPrice)
	if price.Value != 100 {
		t.Fatalf("last price = %d", price.Value)
	}
}
func TestMetadataUnavailableAtFrameTime(t *testing.T) {
	instrument := fixtureInstrument(t, time.Unix(3, 0))
	if _, err := NewPointInTimeView(time.Unix(2, 0), instrument, nil); err == nil {
		t.Fatal("future metadata accepted")
	}
}
func fixtureInstrument(t *testing.T, available time.Time) model.ResearchInstrument {
	underlying, _ := domain.NewUnderlyingID("TEST")
	lot, _ := domain.NewQuantity(1)
	tick, _ := domain.NewPrice(1, "INR")
	currency, _ := domain.NewCurrency("INR")
	instrument, _ := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentCash, UnderlyingID: underlying, Type: domain.InstrumentEquity, ExchangeSymbol: "TEST", LotSize: lot, TickSize: tick, Currency: currency})
	value, err := model.NewResearchInstrument(model.InstrumentSpec{Instrument: instrument, AvailableFrom: available})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func fixtureObservation(t *testing.T, id domain.InstrumentID, at time.Time, value int64) model.Observation {
	price, _ := domain.NewPrice(value, "INR")
	result, err := model.NewObservation(model.ObservationSpec{InstrumentID: id, ExchangeTime: at, Price: price, Volume: 1})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
