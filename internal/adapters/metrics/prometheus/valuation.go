package prometheus

import (
	"github.com/bibhuyash/tradeedge/internal/accounting/valuation"
	prom "github.com/prometheus/client_golang/prometheus"
)

// ValuationRecorder exposes only the finite valuation vocabulary as labels.
type ValuationRecorder struct {
	attempts *prom.CounterVec
	duration *prom.HistogramVec
	inFlight prom.Gauge
}

func NewValuationRecorder(registerer prom.Registerer) (*ValuationRecorder, error) {
	value := &ValuationRecorder{attempts: prom.NewCounterVec(prom.CounterOpts{Name: "tradeedge_financial_operations_total", Help: "Bounded valuation and financial publication outcomes."}, []string{"operation", "outcome", "status", "reason"}), duration: prom.NewHistogramVec(prom.HistogramOpts{Name: "tradeedge_financial_operation_duration_seconds", Help: "Valuation operation latency."}, []string{"operation", "outcome"}), inFlight: prom.NewGauge(prom.GaugeOpts{Name: "tradeedge_financial_in_flight", Help: "Current bounded portfolio valuations."})}
	if registerer == nil {
		registerer = prom.DefaultRegisterer
	}
	if err := registerer.Register(value.attempts); err != nil {
		return nil, err
	}
	if err := registerer.Register(value.duration); err != nil {
		return nil, err
	}
	if err := registerer.Register(value.inFlight); err != nil {
		return nil, err
	}
	return value, nil
}
func (r *ValuationRecorder) Record(event valuation.Event) {
	operation := valuation.BoundedLabel(string(event.Operation))
	outcome := valuation.BoundedLabel(string(event.Outcome))
	status := valuation.BoundedLabel(string(event.Status))
	reason := valuation.BoundedLabel(string(event.Reason))
	r.attempts.WithLabelValues(operation, outcome, status, reason).Inc()
	if event.Duration > 0 {
		r.duration.WithLabelValues(operation, outcome).Observe(event.Duration.Seconds())
	}
	r.inFlight.Set(float64(event.InFlight))
}
