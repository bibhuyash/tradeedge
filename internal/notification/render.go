package notification

import (
	"fmt"
	"strings"
)

const TemplateVersion = "telegram-plain/v1"

func Render(event Event) RenderedMessage {
	d := event.Details
	prefix := "[" + string(event.Severity) + "][" + event.Mode + "] "
	var body string
	switch event.Kind {
	case KindShadowTrade:
		body = fmt.Sprintf("TRADEEDGE SHADOW / PAPER VALIDATION — Shadow trade (hypothetical): %s qty=%d %s", d.InstrumentID, d.Quantity, d.Subject)
	case KindProposalGenerated:
		body = "TRADEEDGE SHADOW / PAPER VALIDATION — Reference candidate proposal: " + d.Subject
	case KindPaperPartialFill, KindPaperFill:
		body = fmt.Sprintf("PAPER ONLY fill: %s qty=%d price=%d %s state=%s %s", d.InstrumentID, d.Quantity, d.PriceMinor, d.Currency, d.State, d.Subject)
	case KindRiskRejected:
		body = "Risk rejected proposal: " + d.Reason
	case KindExecutionUnknown:
		body = "OMS execution is UNKNOWN; reconciliation required"
	case KindKillSwitch:
		body = "Kill switch activated: " + d.Reason
	case KindPreCAS, KindCASActive, KindPostCAS, KindCASRestricted:
		body = "CAS state: " + string(event.Kind) + reasonSuffix(d.Reason)
	case KindRuntimeDegraded, KindReadinessLost:
		body = "Readiness degraded: " + d.Reason
	case KindReadinessRestored, KindRuntimeReady:
		body = "Readiness restored"
	case KindEndOfDay:
		body = "End-of-day summary: " + d.State + reasonSuffix(d.Reason)
	default:
		body = strings.ReplaceAll(string(event.Kind), "_", " ") + reasonSuffix(d.Reason)
	}
	text := prefix + body
	if len(text) > 1000 {
		text = text[:997] + "..."
	}
	return RenderedMessage{NotificationID: NotificationID(event, "telegram", TemplateVersion), Text: text}
}
func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return ": " + reason
}
