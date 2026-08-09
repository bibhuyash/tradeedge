package valuation

import (
	"bytes"
	"errors"
	"math"
	"testing"
	"time"

	accountingengine "github.com/bibhuyash/tradeedge/internal/accounting/engine"
	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	accountingfixture "github.com/bibhuyash/tradeedge/internal/accounting/testfixture"
	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
)

func testPosition(t *testing.T, side domain.Side, quantity, price int64) accountingmodel.PositionSnapshot {
	t.Helper()
	at := accountingfixture.BaseTime
	fill, err := accountingfixture.Fill(700, side, quantity, price, at, at.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	result, err := accountingengine.Apply(nil, fill)
	if err != nil {
		t.Fatal(err)
	}
	return result.Snapshot
}
func testMark(t *testing.T, p accountingmodel.PositionSnapshot, minor int64, at time.Time, state readiness.State) MarkPrice {
	t.Helper()
	price, err := domain.NewPrice(minor, "INR")
	if err != nil {
		t.Fatal(err)
	}
	quote, err := marketmodel.NewQuoteEvent(marketmodel.QuoteSpec{InstrumentID: p.Spec().InstrumentID, LastPrice: price, Volume: 1, ExchangeTime: at, IngestedAt: at.Add(time.Millisecond), Provenance: marketmodel.Provenance{Provider: "fixture", ProviderToken: "token", MasterVersion: "v1", DatasetRevision: "r1"}})
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := accountingmodel.NewStateChecksum("market", []byte("revision"))
	value, err := NewMarkPrice(quote, "r1", sum, state, readiness.ReasonNone)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func TestLongShortProfitLossAndAtCost(t *testing.T) {
	now := accountingfixture.BaseTime.Add(time.Second)
	tests := []struct {
		name       string
		side       domain.Side
		mark, want int64
	}{{"long profit", domain.SideBuy, 120, 200}, {"long loss", domain.SideBuy, 80, -200}, {"long at cost", domain.SideBuy, 100, 0}, {"short profit", domain.SideSell, 80, 200}, {"short loss", domain.SideSell, 120, -200}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			p := testPosition(t, test.side, 10, 100)
			m := testMark(t, p, test.mark, now, readiness.StateReady)
			value, err := EvaluatePosition(p, &m, now.Add(time.Millisecond), DefaultPolicy())
			if err != nil {
				t.Fatal(err)
			}
			if value.Status != StatusComplete || value.UnrealizedPnL.Value.MinorUnits() != test.want {
				t.Fatalf("status=%s pnl=%d", value.Status, value.UnrealizedPnL.Value.MinorUnits())
			}
			spec := p.Spec()
			if value.NetQuantity != spec.NetQuantity.Int64() || value.OpenBasis.MinorUnits() != spec.OpenCostBasis.MinorUnits() || value.RealizedPnL.MinorUnits() != spec.GrossRealizedPnL.MinorUnits() {
				t.Fatal("valuation changed accounting authority")
			}
		})
	}
}

func TestCASAndOfficialClosePricesCannotMasqueradeAsLTP(t *testing.T) {
	position := testPosition(t, domain.SideBuy, 10, 100)
	at := accountingfixture.BaseTime.Add(time.Second)
	for _, priceType := range []PriceType{CASIndicativePrice, CASReferencePrice, CASEquilibriumPrice, OfficialClosePrice} {
		mark := testMark(t, position, 120, at, readiness.StateReady)
		mark.PriceType = priceType
		value, err := EvaluatePosition(position, &mark, at.Add(time.Millisecond), DefaultPolicy())
		if err != nil || value.Status != StatusUnavailable || value.Reason != ReasonInvalidPrice || value.UnrealizedPnL.Known() {
			t.Fatalf("price type %s became valuation authority: value=%+v err=%v", priceType, value, err)
		}
	}
}
func TestFlatMissingStaleFutureAndOverflow(t *testing.T) {
	p := testPosition(t, domain.SideBuy, 10, 100)
	at := accountingfixture.BaseTime.Add(time.Second)
	missing, err := EvaluatePosition(p, nil, at, DefaultPolicy())
	if err != nil || missing.Status != StatusUnavailable || missing.UnrealizedPnL.Known() {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
	m := testMark(t, p, 100, at.Add(-time.Minute), readiness.StateSessionClosed)
	stale, err := EvaluatePosition(p, &m, at, DefaultPolicy())
	if err != nil || stale.Status != StatusStale {
		t.Fatalf("stale=%+v err=%v", stale, err)
	}
	future := testMark(t, p, 100, at.Add(time.Minute), readiness.StateReady)
	unavailable, err := EvaluatePosition(p, &future, at, DefaultPolicy())
	if err != nil || unavailable.Reason != ReasonClockSkew {
		t.Fatalf("future=%+v err=%v", unavailable, err)
	}
	huge := testMark(t, p, math.MaxInt64, at, readiness.StateReady)
	if _, err = EvaluatePosition(p, &huge, at, DefaultPolicy()); !errors.Is(err, domain.ErrMoneyOverflow) {
		t.Fatalf("overflow=%v", err)
	}
	fill, _ := accountingfixture.Fill(701, domain.SideSell, 10, 100, at.Add(time.Second), at.Add(time.Second))
	flatResult, err := accountingengine.Apply(&p, fill)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := EvaluatePosition(flatResult.Snapshot, nil, at.Add(2*time.Second), DefaultPolicy())
	if err != nil || flat.Status != StatusComplete || flat.UnrealizedPnL.Value.MinorUnits() != 0 {
		t.Fatalf("flat=%+v err=%v", flat, err)
	}
}
func TestAggregateCompletenessExposureAndDeterminism(t *testing.T) {
	at := accountingfixture.BaseTime.Add(time.Second)
	longPosition := testPosition(t, domain.SideBuy, 10, 100)
	shortPosition := cloneInstrument(t, testPosition(t, domain.SideSell, 5, 200), "second")
	longMark := testMark(t, longPosition, 120, at, readiness.StateReady)
	shortMark := testMark(t, shortPosition, 150, at, readiness.StateReady)
	longValue, _ := EvaluatePosition(longPosition, &longMark, at.Add(time.Millisecond), DefaultPolicy())
	shortValue, _ := EvaluatePosition(shortPosition, &shortMark, at.Add(time.Millisecond), DefaultPolicy())
	portfolio := longPosition.Spec().PortfolioID
	first, err := Aggregate(portfolio, 1, []PositionValuation{shortValue, longValue}, at)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Aggregate(portfolio, 1, []PositionValuation{longValue, shortValue}, at)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.CanonicalJSON(), second.CanonicalJSON()) || first.GrossExposure.Value.MinorUnits() != 1950 || first.NetExposure.Value.MinorUnits() != 450 || first.UnrealizedPnL.Value.MinorUnits() != 450 {
		t.Fatalf("aggregate gross=%d net=%d pnl=%d", first.GrossExposure.Value.MinorUnits(), first.NetExposure.Value.MinorUnits(), first.UnrealizedPnL.Value.MinorUnits())
	}
	missing, _ := EvaluatePosition(shortPosition, nil, at, DefaultPolicy())
	partial, err := Aggregate(portfolio, 2, []PositionValuation{longValue, missing}, at)
	if err != nil || partial.Status != StatusPartial || partial.TotalPnL.Known() {
		t.Fatalf("partial=%+v err=%v", partial, err)
	}
}
func cloneInstrument(t *testing.T, p accountingmodel.PositionSnapshot, key string) accountingmodel.PositionSnapshot {
	t.Helper()
	spec := p.Spec()
	id, _ := domain.InstrumentIDFromCanonicalKey(key)
	positionID, _ := accountingmodel.NewPositionID(spec.PortfolioID.String(), id.String())
	spec.InstrumentID = id
	spec.PositionID = positionID
	value, err := accountingmodel.NewPositionSnapshot(spec)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
