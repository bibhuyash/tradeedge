package releasegate

import (
	"context"
	"time"

	"github.com/bibhuyash/tradeedge/internal/adapters/notification/telegram"
	"github.com/bibhuyash/tradeedge/internal/notification"
	"github.com/bibhuyash/tradeedge/internal/operations/cas"
	"github.com/bibhuyash/tradeedge/internal/operations/reporting"
)

type blockingSender struct{ ch chan struct{} }

func (b *blockingSender) Send(ctx context.Context, _ notification.RenderedMessage) (notification.Receipt, error) {
	select {
	case <-b.ch:
		return notification.Receipt{}, nil
	case <-ctx.Done():
		return notification.Receipt{}, &notification.RetryableError{Class: "TRANSPORT", Cause: ctx.Err()}
	}
}
func (*blockingSender) Status() notification.ProviderStatus {
	return notification.ProviderStatus{Provider: "gate", State: "READY"}
}

type Report struct {
	SchemaVersion            int      `json:"schema_version"`
	Passed                   bool     `json:"passed"`
	DeterministicEvent       bool     `json:"deterministic_event"`
	ReplaySuppressed         bool     `json:"replay_suppressed"`
	CriticalFailureEvidenced bool     `json:"critical_failure_evidenced"`
	CASEvidenceDeterministic bool     `json:"cas_evidence_deterministic"`
	IncompletePnLExplicit    bool     `json:"incomplete_pnl_explicit"`
	TelegramDisabledSafe     bool     `json:"telegram_disabled_safe"`
	FailureReasons           []string `json:"failure_reasons"`
}

func Run() Report {
	report := Report{SchemaVersion: 1, TelegramDisabledSafe: telegram.Disabled{}.Status().State == "DISABLED"}
	at := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	spec := notification.EventSpec{SourceID: "gate", TradingDate: "2026-08-10", OccurredAt: at, Mode: "PAPER", Category: notification.CategoryExecution, Kind: notification.KindExecutionUnknown, Severity: notification.SeverityCritical}
	first, _ := notification.NewEvent(spec)
	second, _ := notification.NewEvent(spec)
	report.DeterministicEvent = first.ID == second.ID && first.Checksum == second.Checksum
	store, _ := notification.NewStore(10, 20)
	dispatcher, _ := notification.NewDispatcher(notification.DefaultConfig(), telegram.Disabled{}, store, nil, nil)
	dispatcher.Publish(first, true)
	deliveries := store.RecentDeliveries(1, false)
	report.ReplaySuppressed = len(deliveries) == 1 && deliveries[0].State == notification.DeliverySuppressed
	blocked := &blockingSender{ch: make(chan struct{})}
	queueConfig := notification.DefaultConfig()
	queueConfig.Capacity = 4
	queueConfig.Workers = 1
	queueDispatcher, _ := notification.NewDispatcher(queueConfig, blocked, store, nil, nil)
	for i := 0; i < 4; i++ {
		queuedSpec := spec
		queuedSpec.SourceID = string(rune('a' + i))
		queued, _ := notification.NewEvent(queuedSpec)
		queueDispatcher.Publish(queued, false)
	}
	deadline := time.Now().Add(time.Second)
	for queueDispatcher.Health().FailureCount == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	failures := store.RecentDeliveries(20, true)
	report.CriticalFailureEvidenced = len(failures) > 0 && failures[0].Reason == "QUEUE_FULL"
	close(blocked.ch)
	_ = queueDispatcher.Shutdown(context.Background())
	_ = dispatcher.Shutdown(context.Background())
	casSpec := cas.Spec{TradingDate: "2026-08-10", InstrumentID: "NIFTY", StrategyID: "fixture", RuntimeMode: "PAPER", Regime: "PRE_CAS", ConfigurationVersion: "runtime/v1", ConfigurationChecksum: "config", CalendarVersion: "calendar", PreCAS: cas.UnavailablePrice("NOT_OBSERVED"), LatestEligibleLTP: cas.UnavailablePrice("NOT_OBSERVED"), CASIndicative: cas.UnavailablePrice("SOURCE_UNAVAILABLE"), CASReference: cas.UnavailablePrice("SOURCE_UNAVAILABLE"), CASEquilibrium: cas.UnavailablePrice("SOURCE_UNAVAILABLE"), OfficialClose: cas.UnavailablePrice("NOT_PUBLISHED"), UpdatedAt: at}
	ca, _ := cas.NewRecord(casSpec)
	cb, _ := cas.NewRecord(casSpec)
	report.CASEvidenceDeterministic = ca.Checksum == cb.Checksum
	summary, _ := reporting.NewSummary(reporting.Summary{Mode: "PAPER", TradingDate: "2026-08-10", GeneratedAt: at, Final: true, Financial: reporting.Financial{Status: "PARTIAL", TotalPnL: reporting.Money{Availability: "UNAVAILABLE", Reason: "INCOMPLETE_VALUATION"}}})
	report.IncompletePnLExplicit = summary.Financial.TotalPnL.Availability == "UNAVAILABLE"
	checks := []struct {
		ok     bool
		reason string
	}{{report.DeterministicEvent, "event identity"}, {report.ReplaySuppressed, "replay suppression"}, {report.CriticalFailureEvidenced, "failure evidence"}, {report.CASEvidenceDeterministic, "CAS determinism"}, {report.IncompletePnLExplicit, "incomplete P&L"}, {report.TelegramDisabledSafe, "disabled Telegram"}}
	for _, check := range checks {
		if !check.ok {
			report.FailureReasons = append(report.FailureReasons, check.reason)
		}
	}
	report.Passed = len(report.FailureReasons) == 0
	return report
}
