package cost

import "testing"

func TestSyntheticCostCalculationIsComponentized(t *testing.T) {
	zero := Rate{Numerator: 0, Denominator: 1, Base: BaseTotalNotional, Rounding: RoundFloor}
	m, err := New(Config{SchemaVersion: SchemaV1, Version: "SYNTHETIC_TEST_ONLY/v1", Brokerage: Rate{Numerator: 1, Denominator: 100, Base: BaseTotalNotional, Rounding: RoundCeil}, STT: zero, ExchangeCharges: zero, GST: Rate{Numerator: 1, Denominator: 10, Base: BasePriorCharges, Rounding: RoundCeil}, StampDuty: zero, RegulatoryCharges: zero})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Calculate(Input{BuyNotional: 50, SellNotional: 51, GrossPnL: 10, Slippage: 2, SpreadImpact: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got.Brokerage != 2 || got.GST != 1 || got.TotalCosts != 7 || got.NetPnL != 3 {
		t.Fatalf("unexpected breakdown: %#v", got)
	}
}
