package report

import (
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/research/backtest"
	"github.com/bibhuyash/tradeedge/internal/research/cost"
)

func TestCanonicalReportAndDrawdown(t *testing.T) {
	money, _ := domain.NewMoney(1000, "INR")
	zero, _ := domain.NewMoney(0, "INR")
	costs, _ := domain.NewMoney(20, "INR")
	result := backtest.Result{DatasetVersion: "dataset/v1", StrategyVersion: "strategy/v1", CostVersion: "cost/v1", Start: time.Unix(1, 0), End: time.Unix(2, 0), StartingCapital: money, Cash: money, RealizedPnL: zero, UnrealizedPnL: zero, TransactionCosts: costs, EquityCurve: []int64{1000, 1100, 900, 950}, Trades: []backtest.Trade{{ID: "t", InstrumentID: "i", Side: "BUY", Quantity: 1, GrossPnLMinor: 100, NetPnLMinor: 80, Costs: cost.Breakdown{TotalCosts: 20}}}}
	renderer := Reporter{}
	a, err := renderer.Report(result)
	if err != nil {
		t.Fatal(err)
	}
	b, err := renderer.Report(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("canonical report differs")
	}
	report, err := Build(result)
	if err != nil {
		t.Fatal(err)
	}
	if report.MaxDrawdownMinor != 200 || report.WinRateBPS != 10000 || report.NetPnLMinor != 80 {
		t.Fatalf("unexpected metrics: %#v", report)
	}
}
