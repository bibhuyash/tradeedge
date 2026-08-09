// Package operations composes non-authoritative operational observers.
package operations

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/bibhuyash/tradeedge/internal/notification"
	"github.com/bibhuyash/tradeedge/internal/operations/cas"
	"github.com/bibhuyash/tradeedge/internal/operations/reporting"
)

type Publisher interface {
	Publish(notification.Event, bool)
}
type Observer struct {
	publisher Publisher
	cas       *cas.Recorder
	reports   *reporting.Accumulator
	mu        sync.Mutex
	evidence  map[string]cas.Spec
	telemetry notification.Telemetry
	seen      map[string]struct{}
	seenOrder []string
}

func NewObserver(p Publisher, c *cas.Recorder, r *reporting.Accumulator, telemetry notification.Telemetry) (*Observer, error) {
	if p == nil || c == nil || r == nil {
		return nil, notification.ErrInvalid
	}
	if telemetry == nil {
		telemetry = notification.NoopTelemetry{}
	}
	return &Observer{publisher: p, cas: c, reports: r, evidence: map[string]cas.Spec{}, telemetry: telemetry, seen: map[string]struct{}{}}, nil
}
func (o *Observer) Observe(event notification.Event) {
	o.mu.Lock()
	_, duplicate := o.seen[event.ID]
	if !duplicate {
		o.seen[event.ID] = struct{}{}
		o.seenOrder = append(o.seenOrder, event.ID)
		if len(o.seenOrder) > 4096 {
			delete(o.seen, o.seenOrder[0])
			o.seenOrder = o.seenOrder[1:]
		}
	}
	o.mu.Unlock()
	if duplicate {
		o.publisher.Publish(event, false)
		return
	}
	o.reports.Observe(event)
	if event.Category == notification.CategoryValuation {
		d := event.Details
		reason := d.Reason
		if reason == "" && d.ValuationStatus != "COMPLETE" {
			reason = "INCOMPLETE_VALUATION"
		}
		o.reports.UpdateFinancial(event.Mode, event.TradingDate, reporting.Financial{Status: d.ValuationStatus, SnapshotChecksum: d.FinancialChecksum, RealizedPnL: reportMoney(d.RealizedAvailability, d.RealizedMinor, d.Currency, reason), UnrealizedPnL: reportMoney(d.UnrealizedAvailability, d.UnrealizedMinor, d.Currency, reason), TotalPnL: reportMoney(d.TotalAvailability, d.TotalMinor, d.Currency, reason), MaxDrawdown: reporting.Money{Availability: "UNAVAILABLE", Reason: "INCOMPLETE_SERIES"}})
	}
	if event.Kind == notification.KindEndOfDay {
		if summary, err := o.reports.Close(event.Mode, event.TradingDate, event.OccurredAt); err == nil {
			details := event.Details
			details.State = summary.Financial.Status
			details.Reason = fmt.Sprintf("proposals=%d approved=%d modified=%d rejected=%d fills=%d pnl=%s", summary.Counts.Proposals, summary.Counts.RiskApproved, summary.Counts.RiskModified, summary.Counts.RiskRejected, summary.Counts.Fills, summary.Financial.TotalPnL.Availability)
			if rebuilt, rebuildErr := notification.NewEvent(notification.EventSpec{SourceID: event.SourceID, TradingDate: event.TradingDate, OccurredAt: event.OccurredAt, Mode: event.Mode, Category: event.Category, Kind: event.Kind, Severity: event.Severity, Details: details}); rebuildErr == nil {
				event = rebuilt
			}
			o.telemetry.RecordNotification(notification.MetricEvent{Operation: "eod_summary", Outcome: "created", Severity: string(event.Severity), Category: string(event.Category), Kind: string(event.Kind)})
		}
	}
	if event.Category == notification.CategoryValuation {
		o.captureFinancial(event)
	}
	if event.Category == notification.CategoryCAS || o.hasCASEvidence(event) {
		o.capture(event)
	}
	o.publisher.Publish(event, false)
}
func (o *Observer) hasCASEvidence(event notification.Event) bool {
	d := event.Details
	if d.InstrumentID == "" || d.StrategyID == "" {
		return false
	}
	key := event.TradingDate + "|" + event.Mode + "|" + d.InstrumentID + "|" + d.StrategyID
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.evidence[key]
	return ok
}
func (o *Observer) captureFinancial(event notification.Event) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for key, spec := range o.evidence {
		if spec.TradingDate != event.TradingDate || spec.RuntimeMode != event.Mode {
			continue
		}
		value := cas.KnownValue(event.Details.FinancialChecksum, event.SourceID, event.OccurredAt)
		if event.Details.FinancialChecksum == "" {
			value = cas.KnownValue(event.SourceID, event.SourceID, event.OccurredAt)
		}
		if spec.Regime == "POST_CAS" {
			spec.PositionAfter = value
		} else {
			spec.PositionBefore = value
		}
		spec.SourceChecksums = append(spec.SourceChecksums, event.Checksum)
		spec.UpdatedAt = event.OccurredAt
		if stored, err := o.cas.Capture(spec); err == nil {
			o.evidence[key] = stored
		}
	}
}
func reportMoney(availability string, minor int64, currency, reason string) reporting.Money {
	if availability == "KNOWN" {
		return reporting.Money{Availability: "KNOWN", Minor: minor, Currency: currency}
	}
	if reason == "" {
		reason = "NOT_OBSERVED"
	}
	return reporting.Money{Availability: "UNAVAILABLE", Reason: reason}
}
func (o *Observer) capture(event notification.Event) {
	d := event.Details
	if d.InstrumentID == "" || d.StrategyID == "" {
		return
	}
	key := event.TradingDate + "|" + event.Mode + "|" + d.InstrumentID + "|" + d.StrategyID
	o.mu.Lock()
	defer o.mu.Unlock()
	spec, ok := o.evidence[key]
	created := !ok
	if !ok {
		unavailablePrice := cas.UnavailablePrice("SOURCE_UNAVAILABLE")
		unavailableValue := cas.UnavailableValue("NOT_OBSERVED")
		checksum := event.Details.ConfigurationChecksum
		if checksum == "" {
			sum := sha256.Sum256([]byte("runtime/v1|" + event.Mode))
			checksum = hex.EncodeToString(sum[:])
		}
		calendarVersion := event.Details.CalendarVersion
		if calendarVersion == "" {
			calendarVersion = "OBSERVED_RUNTIME"
		}
		configVersion := event.Details.ConfigurationVersion
		if configVersion == "" {
			configVersion = "runtime/v1"
		}
		spec = cas.Spec{TradingDate: event.TradingDate, InstrumentID: d.InstrumentID, StrategyID: d.StrategyID, RuntimeMode: event.Mode, PreCAS: unavailablePrice, LatestEligibleLTP: unavailablePrice, CASIndicative: unavailablePrice, CASReference: unavailablePrice, CASEquilibrium: unavailablePrice, OfficialClose: cas.UnavailablePrice("NOT_PUBLISHED"), FuturesReference: unavailablePrice, IndexReference: unavailablePrice, StrategyEligibility: unavailableValue, Proposal: unavailableValue, RiskOutcome: unavailableValue, ShadowDecision: unavailableValue, PaperExecution: unavailableValue, PositionBefore: cas.UnavailableValue("NOT_CAPTURED"), PositionAfter: cas.UnavailableValue("SESSION_OPEN"), BlockedReason: unavailableValue, Readiness: unavailableValue, ConfigurationVersion: configVersion, ConfigurationChecksum: checksum, CalendarVersion: calendarVersion}
	}
	if d.PriceMinor > 0 && d.Currency != "" {
		price := cas.KnownPrice(d.PriceMinor, d.Currency, "LAST_TRADED_PRICE", event.SourceID, event.OccurredAt)
		spec.LatestEligibleLTP = price
		if event.Kind == notification.KindPreCAS {
			spec.PreCAS = price
		}
	}
	switch event.Kind {
	case notification.KindPreCAS:
		spec.Regime = "PRE_CAS"
		spec.StrategyEligibility = cas.KnownValue(d.State, event.SourceID, event.OccurredAt)
	case notification.KindCASActive:
		spec.Regime = "CAS_ACTIVE"
		spec.StrategyEligibility = cas.KnownValue(d.State, event.SourceID, event.OccurredAt)
	case notification.KindPostCAS:
		spec.Regime = "POST_CAS"
	case notification.KindCASRestricted, notification.KindStrategyRestricted:
		spec.BlockedReason = cas.KnownValue(d.Reason, event.SourceID, event.OccurredAt)
	case notification.KindProposalGenerated:
		spec.Proposal = cas.KnownValue(d.ReferenceID, event.SourceID, event.OccurredAt)
	case notification.KindRiskApproved, notification.KindRiskModified, notification.KindRiskRejected:
		spec.RiskOutcome = cas.KnownValue(string(event.Kind), event.SourceID, event.OccurredAt)
	case notification.KindShadowTrade:
		spec.ShadowDecision = cas.KnownValue(d.ReferenceID, event.SourceID, event.OccurredAt)
	case notification.KindPaperSubmitted, notification.KindPaperFill, notification.KindPaperPartialFill:
		spec.PaperExecution = cas.KnownValue(d.ReferenceID, event.SourceID, event.OccurredAt)
	case notification.KindReadinessLost, notification.KindReadinessRestored:
		spec.Readiness = cas.KnownValue(d.State, event.SourceID, event.OccurredAt)
	}
	spec.SourceChecksums = append(spec.SourceChecksums, event.Checksum)
	spec.UpdatedAt = event.OccurredAt
	if value, err := o.cas.Capture(spec); err == nil {
		o.evidence[key] = value
		if created {
			o.reports.RecordCASEvidence(event.Mode, event.TradingDate)
		}
		o.telemetry.RecordNotification(notification.MetricEvent{Operation: "cas_evidence", Outcome: "recorded", Severity: string(event.Severity), Category: string(event.Category), Kind: string(event.Kind)})
	}
}
