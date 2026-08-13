package shadowruntime

import (
	"fmt"
	"time"

	"github.com/bibhuyash/tradeedge/internal/notification"
	"github.com/bibhuyash/tradeedge/internal/qualification"
)

type notificationSpec struct {
	source, kind, subject string
	at                    time.Time
	underlying            qualification.Underlying
	details               notification.Details
}

func (r *Runtime) emit(spec notificationSpec) {
	if r.observer == nil || spec.at.IsZero() || spec.source == "" {
		return
	}
	kind := notification.KindShadowSignal
	category := notification.CategoryStrategy
	switch spec.kind {
	case "ready":
		kind, category = notification.KindShadowSessionReady, notification.CategoryRuntime
	case "closed":
		kind, category = notification.KindShadowSessionClosed, notification.CategoryReporting
	}
	details := spec.details
	if details.Subject == "" {
		details.Subject = spec.subject
	}
	if details.Underlying == "" && spec.underlying != "" {
		details.Underlying = string(spec.underlying)
	}
	event, err := notification.NewEvent(notification.EventSpec{
		SourceID: spec.source, TradingDate: r.tradingDate, Mode: "SHADOW", OccurredAt: spec.at,
		Category: category, Kind: kind, Severity: notification.SeverityInfo, Details: details,
	})
	if err == nil {
		r.observer.Observe(event)
	}
}

func (r *Runtime) SessionReady(at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if at.IsZero() {
		return ErrInvalid
	}
	nifty, bank := r.status[qualification.NIFTY], r.status[qualification.BANKNIFTY]
	r.emit(notificationSpec{
		source: identity(r.tradingDate, "shadow-session-ready"), kind: "ready", at: at,
		subject: fmt.Sprintf("NIFTY: %s; BANKNIFTY: %s", nifty.Strategy, bank.Strategy),
	})
	return nil
}
