package engine_test

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
)

type step struct {
	side                                                  domain.Side
	quantity, price, net, basis, realized, closed, opened int64
}

func TestWeightedAverageAccountingScenarios(t *testing.T) {
	tests := map[string][]step{
		"first BUY opens long":        {{domain.SideBuy, 10, 100, 10, 1000, 0, 0, 10}},
		"multiple BUY updates basis":  {{domain.SideBuy, 10, 100, 10, 1000, 0, 0, 10}, {domain.SideBuy, 10, 200, 20, 3000, 0, 0, 10}},
		"partial SELL long profit":    {{domain.SideBuy, 10, 100, 10, 1000, 0, 0, 10}, {domain.SideSell, 5, 120, 5, 500, 100, 5, 0}},
		"partial SELL long loss":      {{domain.SideBuy, 10, 100, 10, 1000, 0, 0, 10}, {domain.SideSell, 5, 80, 5, 500, -100, 5, 0}},
		"full long close":             {{domain.SideBuy, 10, 100, 10, 1000, 0, 0, 10}, {domain.SideSell, 10, 120, 0, 0, 200, 10, 0}},
		"long to short":               {{domain.SideBuy, 10, 100, 10, 1000, 0, 0, 10}, {domain.SideSell, 15, 150, -5, 750, 500, 10, 5}},
		"first SELL opens short":      {{domain.SideSell, 10, 100, -10, 1000, 0, 0, 10}},
		"multiple SELL updates basis": {{domain.SideSell, 10, 100, -10, 1000, 0, 0, 10}, {domain.SideSell, 10, 200, -20, 3000, 0, 0, 10}},
		"partial BUY short profit":    {{domain.SideSell, 10, 100, -10, 1000, 0, 0, 10}, {domain.SideBuy, 5, 80, -5, 500, 100, 5, 0}},
		"partial BUY short loss":      {{domain.SideSell, 10, 100, -10, 1000, 0, 0, 10}, {domain.SideBuy, 5, 120, -5, 500, -100, 5, 0}},
		"full short close":            {{domain.SideSell, 10, 100, -10, 1000, 0, 0, 10}, {domain.SideBuy, 10, 80, 0, 0, 200, 10, 0}},
		"short to long":               {{domain.SideSell, 10, 100, -10, 1000, 0, 0, 10}, {domain.SideBuy, 15, 50, 5, 250, 500, 10, 5}},
	}
	for name, steps := range tests {
		t.Run(name, func(t *testing.T) {
			var current *accountingmodel.PositionSnapshot
			for index, expected := range steps {
				at := accountingfixture.BaseTime.Add(time.Duration(index) * time.Second)
				fill, err := accountingfixture.Fill(index+1, expected.side, expected.quantity, expected.price, at, at.Add(time.Millisecond))
				if err != nil {
					t.Fatal(err)
				}
				result, err := accountingengine.Apply(current, fill)
				if err != nil {
					t.Fatal(err)
				}
				spec := result.Snapshot.Spec()
				if spec.NetQuantity.Int64() != expected.net || spec.OpenCostBasis.MinorUnits() != expected.basis || spec.GrossRealizedPnL.MinorUnits() != expected.realized || result.Application.Spec().ClosedQuantity.Int64() != expected.closed || result.Application.Spec().OpenedQuantity.Int64() != expected.opened {
					t.Fatalf("step %d: net=%d basis=%d realized=%d closed=%d opened=%d", index, spec.NetQuantity, spec.OpenCostBasis.MinorUnits(), spec.GrossRealizedPnL.MinorUnits(), result.Application.Spec().ClosedQuantity, result.Application.Spec().OpenedQuantity)
				}
				if spec.NetQuantity == 0 && (spec.OpenLot != nil || spec.OpenCostBasis.MinorUnits() != 0) {
					t.Fatal("flat position retained open basis")
				}
				if expected.closed == 0 && result.Application.Spec().GrossRealizedDelta.MinorUnits() != 0 {
					t.Fatal("opening exposure realized P&L")
				}
				if spec.ChargesAvailable || spec.AuthoritativeCharges.MinorUnits() != 0 {
					t.Fatal("M1 invented authoritative charges")
				}
				next := result.Snapshot
				current = &next
			}
		})
	}
}

func TestRemainderCarryAndFinalClose(t *testing.T) {
	steps := []step{{domain.SideBuy, 1, 1, 1, 1, 0, 0, 1}, {domain.SideBuy, 2, 2, 3, 5, 0, 0, 2}, {domain.SideSell, 1, 2, 2, 4, 1, 1, 0}, {domain.SideSell, 2, 2, 0, 0, 1, 2, 0}}
	var current *accountingmodel.PositionSnapshot
	for index, expected := range steps {
		at := accountingfixture.BaseTime.Add(time.Duration(index) * time.Second)
		fill, _ := accountingfixture.Fill(index+20, expected.side, expected.quantity, expected.price, at, at)
		result, err := accountingengine.Apply(current, fill)
		if err != nil {
			t.Fatal(err)
		}
		if result.Snapshot.Spec().OpenCostBasis.MinorUnits() != expected.basis || result.Snapshot.Spec().GrossRealizedPnL.MinorUnits() != expected.realized {
			t.Fatalf("step %d did not carry remainder", index)
		}
		next := result.Snapshot
		current = &next
	}
}

func TestInvalidPriceQuantityOverflowOrderingAndDefensiveCopies(t *testing.T) {
	at := accountingfixture.BaseTime
	if _, err := accountingfixture.Fill(1, domain.SideBuy, 1, 0, at, at); !errors.Is(err, accountingmodel.ErrInvalidAccountingFill) {
		t.Fatalf("zero price: %v", err)
	}
	if _, err := accountingfixture.Fill(1, domain.SideBuy, 0, 1, at, at); err == nil {
		t.Fatal("zero quantity accepted")
	}
	overflow, _ := accountingfixture.Fill(2, domain.SideBuy, 2, math.MaxInt64, at, at)
	if _, err := accountingengine.Apply(nil, overflow); !errors.Is(err, accountingengine.ErrArithmeticOverflow) {
		t.Fatalf("overflow: %v", err)
	}
	first, _ := accountingfixture.Fill(3, domain.SideBuy, 1, 10, at, at)
	result, _ := accountingengine.Apply(nil, first)
	snapshot := result.Snapshot
	late, _ := accountingfixture.Fill(4, domain.SideBuy, 1, 10, at.Add(-time.Second), at)
	if _, err := accountingengine.Apply(&snapshot, late); !errors.Is(err, accountingengine.ErrOutOfOrderFill) {
		t.Fatalf("late fill: %v", err)
	}
	raw := snapshot.CanonicalJSON()
	raw[0] = 'x'
	if bytes.Equal(raw, snapshot.CanonicalJSON()) {
		t.Fatal("snapshot canonical bytes were not defensive")
	}
	appRaw := result.Application.CanonicalJSON()
	appRaw[0] = 'x'
	if bytes.Equal(appRaw, result.Application.CanonicalJSON()) {
		t.Fatal("application canonical bytes were not defensive")
	}
}
