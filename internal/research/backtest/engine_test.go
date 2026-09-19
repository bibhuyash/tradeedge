package backtest

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/research/cost"
	"github.com/bibhuyash/tradeedge/internal/research/features"
	"github.com/bibhuyash/tradeedge/internal/research/model"
)

func TestDeterministicReplayAndLifecycle(t *testing.T) {
	engine := fixtureEngine(t, []int64{100, 110, 120, 130})
	first, err := engine.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("replay results differ")
	}
	if first.Evaluations != 4 || first.Signals != 2 || len(first.Trades) != 1 || len(first.OpenPositions) != 0 {
		t.Fatalf("unexpected result: %#v", first)
	}
	trade := first.Trades[0]
	if !trade.FillTime.After(trade.OrderTime) || !trade.ExitFillTime.After(trade.ExitOrderTime) {
		t.Fatal("fill chronology violated")
	}
	if trade.GrossPnLMinor != 10 || trade.Costs.SpreadImpact != 2 || trade.NetPnLMinor != 8 {
		t.Fatalf("unexpected trade: %#v", trade)
	}
}

func TestFutureCloseCannotChangeCurrentEntryAndDatasetEndStaysOpen(t *testing.T) {
	first, err := fixtureEngine(t, []int64{100, 110, 120, 130}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	changed, err := fixtureEngine(t, []int64{100, 110, 120, 900}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Trades[0].FillPriceMinor != changed.Trades[0].FillPriceMinor || first.Trades[0].FillTime != changed.Trades[0].FillTime {
		t.Fatal("future close changed the entry fill")
	}
	open, err := fixtureEngine(t, []int64{100, 110, 120}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(open.Trades) != 0 || len(open.OpenPositions) != 1 || open.UnrealizedPnL.MinorUnits() != -1 {
		t.Fatalf("dataset end synthesized a close or mis-valued position: %#v", open)
	}
}
func TestFillRejectsSameTimeAndMissingBook(t *testing.T) {
	instrument := researchInstrument(t)
	quantity, _ := domain.NewQuantity(1)
	at := time.Unix(2, 0)
	order := SimulatedOrder{ID: "order", SignalTime: at, OrderTime: at, InstrumentID: instrument.ID(), Side: domain.SideBuy, Quantity: quantity}
	price, _ := domain.NewPrice(100, "INR")
	same, _ := model.NewObservation(model.ObservationSpec{InstrumentID: instrument.ID(), ExchangeTime: at, Price: price})
	fills, _ := NewBaselineFillModel(FillPolicyV1, 0)
	if _, err := fills.Fill(order, same); err == nil {
		t.Fatal("same-time fill accepted")
	}
	later, _ := model.NewObservation(model.ObservationSpec{InstrumentID: instrument.ID(), ExchangeTime: at.Add(time.Second), Price: price})
	if _, err := fills.Fill(order, later); err != ErrNoLiquidity {
		t.Fatalf("missing book error = %v", err)
	}
}
func fixtureEngine(t *testing.T, prices []int64) Engine {
	instrument := researchInstrument(t)
	var observations []model.Observation
	for index, last := range prices {
		price, _ := domain.NewPrice(last, "INR")
		bid, _ := domain.NewPrice(last-1, "INR")
		ask, _ := domain.NewPrice(last+1, "INR")
		observation, err := model.NewObservation(model.ObservationSpec{InstrumentID: instrument.ID(), ExchangeTime: time.Unix(int64(index+2), 0), Price: price, Bid: &bid, Ask: &ask, Volume: 1})
		if err != nil {
			t.Fatal(err)
		}
		observations = append(observations, observation)
	}
	dataset, err := model.NewDataset(model.DatasetSpec{SchemaVersion: model.DatasetSchemaV1, Version: "SYNTHETIC_DATASET/v1", Instruments: []model.ResearchInstrument{instrument}, Observations: observations})
	if err != nil {
		t.Fatal(err)
	}
	zero := cost.Rate{Numerator: 0, Denominator: 1, Base: cost.BaseTotalNotional, Rounding: cost.RoundFloor}
	costModel, err := cost.New(cost.Config{SchemaVersion: cost.SchemaV1, Version: "SYNTHETIC_COST/v1", Brokerage: zero, STT: zero, ExchangeCharges: zero, GST: zero, StampDuty: zero, RegulatoryCharges: zero})
	if err != nil {
		t.Fatal(err)
	}
	strategy, _ := NewControlStrategy(domain.SideBuy)
	fills, _ := NewBaselineFillModel(FillPolicyV1, 0)
	capital, _ := domain.NewMoney(100000, "INR")
	quantity, _ := domain.NewQuantity(1)
	engine, err := NewEngine(NewDatasetSource(dataset), features.Engine{}, strategy, fills, costModel, Config{PolicyVersion: EnginePolicyV1, StartingCapital: capital, Quantity: quantity})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}
func researchInstrument(t *testing.T) model.ResearchInstrument {
	underlying, _ := domain.NewUnderlyingID("TEST")
	lot, _ := domain.NewQuantity(1)
	tick, _ := domain.NewPrice(1, "INR")
	currency, _ := domain.NewCurrency("INR")
	instrument, _ := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentCash, UnderlyingID: underlying, Type: domain.InstrumentEquity, ExchangeSymbol: "TEST", LotSize: lot, TickSize: tick, Currency: currency})
	value, err := model.NewResearchInstrument(model.InstrumentSpec{Instrument: instrument, AvailableFrom: time.Unix(1, 0)})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
