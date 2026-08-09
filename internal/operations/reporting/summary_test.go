package reporting

import (
	"github.com/bibhuyash/tradeedge/internal/notification"
	"testing"
	"time"
)

func TestDeterministicSummaryAndIncompleteValuation(t *testing.T) {
	a, _ := NewAccumulator(2)
	at := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	for i, k := range []notification.Kind{notification.KindProposalGenerated, notification.KindRiskRejected, notification.KindExecutionUnknown, notification.KindCASRestricted} {
		e, _ := notification.NewEvent(notification.EventSpec{SourceID: string(rune('a' + i)), TradingDate: "2026-08-10", OccurredAt: at, Mode: "PAPER", Category: notification.CategoryReporting, Kind: k, Severity: notification.SeverityWarning})
		a.Observe(e)
	}
	a.UpdateFinancial("PAPER", "2026-08-10", Financial{Status: "PARTIAL", RealizedPnL: Money{Availability: "KNOWN", Minor: 10, Currency: "INR"}, UnrealizedPnL: Money{Availability: "UNAVAILABLE", Reason: "MISSING_MARK"}, TotalPnL: Money{Availability: "UNAVAILABLE", Reason: "INCOMPLETE_VALUATION"}, MaxDrawdown: Money{Availability: "UNAVAILABLE", Reason: "INCOMPLETE_SERIES"}})
	first, err := a.Close("PAPER", "2026-08-10", at)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := a.Close("PAPER", "2026-08-10", at)
	if first.Checksum != second.Checksum || first.Counts.Proposals != 1 || first.Financial.TotalPnL.Availability != "UNAVAILABLE" {
		t.Fatalf("bad summary: %+v", first)
	}
}

func TestAuthoritativeCompleteSeriesComputesDrawdown(t *testing.T) {
	a, _ := NewAccumulator(2)
	a.UpdateFinancial("PAPER", "2026-08-10", Financial{Status: "COMPLETE", RealizedPnL: Money{Availability: "KNOWN", Currency: "INR"}, UnrealizedPnL: Money{Availability: "KNOWN", Currency: "INR", Minor: 100}, TotalPnL: Money{Availability: "KNOWN", Currency: "INR", Minor: 100}})
	a.UpdateFinancial("PAPER", "2026-08-10", Financial{Status: "COMPLETE", RealizedPnL: Money{Availability: "KNOWN", Currency: "INR"}, UnrealizedPnL: Money{Availability: "KNOWN", Currency: "INR", Minor: 60}, TotalPnL: Money{Availability: "KNOWN", Currency: "INR", Minor: 60}})
	summary, err := a.Close("PAPER", "2026-08-10", time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC))
	if err != nil || summary.Financial.MaxDrawdown.Availability != "KNOWN" || summary.Financial.MaxDrawdown.Minor != 40 {
		t.Fatalf("drawdown=%+v err=%v", summary.Financial.MaxDrawdown, err)
	}
}
