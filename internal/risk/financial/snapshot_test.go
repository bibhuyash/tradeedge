package financial

import (
	"errors"
	"github.com/bibhuyash/tradeedge/internal/accounting/valuation"
	"github.com/bibhuyash/tradeedge/internal/domain"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	"testing"
)

func TestRequireValuationFailsClosed(t *testing.T) {
	unknown := valuation.MoneyValue{}
	complete := Snapshot{Status: valuation.StatusComplete, UnrealizedPnL: unknown, TotalPnL: unknown, GrossExposure: unknown}
	if !errors.Is(complete.RequireValuation(), ErrFinancialStateNotReady) {
		t.Fatal("unknown complete values accepted")
	}
	money, _ := domain.NewMoney(0, "INR")
	known := valuation.MoneyValue{Availability: portfoliomodel.AvailabilityKnown, Value: money}
	complete = Snapshot{Status: valuation.StatusComplete, UnrealizedPnL: known, TotalPnL: known, GrossExposure: known}
	if err := complete.RequireValuation(); err != nil {
		t.Fatalf("complete rejected: %v", err)
	}
	for _, state := range []valuation.Status{valuation.StatusStale, valuation.StatusPartial, valuation.StatusUnavailable} {
		value := Snapshot{Status: state, UnrealizedPnL: known, TotalPnL: known, GrossExposure: known}
		if !errors.Is(value.RequireValuation(), ErrFinancialStateNotReady) {
			t.Fatalf("%s accepted", state)
		}
	}
}
