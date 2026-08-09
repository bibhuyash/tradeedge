package operations

import (
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/notification"
	"github.com/bibhuyash/tradeedge/internal/operations/cas"
	"github.com/bibhuyash/tradeedge/internal/operations/reporting"
)

type publisher struct{ events []notification.Event }

func (p *publisher) Publish(event notification.Event, _ bool) { p.events = append(p.events, event) }
func TestObserverBuildsCASEvidenceAndEODIndependently(t *testing.T) {
	p := &publisher{}
	store, _ := cas.NewStore(10)
	recorder, _ := cas.NewRecorder(store)
	reports, _ := reporting.NewAccumulator(10)
	observer, err := NewObserver(p, recorder, reports, nil)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	normal, _ := notification.NewEvent(notification.EventSpec{SourceID: "normal", TradingDate: "2026-08-10", OccurredAt: at, Mode: "PAPER", Category: notification.CategoryProposal, Kind: notification.KindProposalGenerated, Severity: notification.SeverityInfo, Details: notification.Details{InstrumentID: "NIFTY", StrategyID: "fixture"}})
	observer.Observe(normal)
	if len(store.Recent(10)) != 0 {
		t.Fatal("normal session manufactured CAS evidence")
	}
	value, _ := notification.NewEvent(notification.EventSpec{SourceID: "cas", TradingDate: "2026-08-10", OccurredAt: at, Mode: "PAPER", Category: notification.CategoryCAS, Kind: notification.KindPreCAS, Severity: notification.SeverityInfo, Details: notification.Details{InstrumentID: "NIFTY", StrategyID: "fixture", State: "SESSION_RESTRICTED", PriceMinor: 10000, Currency: "INR"}})
	observer.Observe(value)
	eod, _ := notification.NewEvent(notification.EventSpec{SourceID: "eod", TradingDate: "2026-08-10", OccurredAt: at, Mode: "PAPER", Category: notification.CategoryReporting, Kind: notification.KindEndOfDay, Severity: notification.SeverityInfo})
	observer.Observe(eod)
	if len(store.Recent(10)) != 1 || store.Recent(1)[0].CASIndicative.Availability != cas.Unavailable {
		t.Fatal("CAS evidence missing")
	}
	if summary, ok := reports.Latest(); !ok || !summary.Final || summary.Counts.CASEvidence != 1 {
		t.Fatalf("EOD missing: %+v", summary)
	}
	if len(p.events) != 3 {
		t.Fatal("events not published")
	}
	if p.events[len(p.events)-1].Details.Reason == "" {
		t.Fatal("EOD presentation omitted summary")
	}
}
