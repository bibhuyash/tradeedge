package notification

type MetricEvent struct {
	Operation, Outcome, Severity, Category, Kind, Reason string
	QueueDepth                                           int
}
type Telemetry interface{ RecordNotification(MetricEvent) }
type NoopTelemetry struct{}

func (NoopTelemetry) RecordNotification(MetricEvent) {}
