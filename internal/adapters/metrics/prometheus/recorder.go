package prometheus

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	brokertelemetry "github.com/bibhuyash/tradeedge/internal/broker/telemetry"
	executiontelemetry "github.com/bibhuyash/tradeedge/internal/execution/telemetry"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/telemetry"
	"github.com/bibhuyash/tradeedge/internal/notification"
	risktelemetry "github.com/bibhuyash/tradeedge/internal/risk/telemetry"
	strategytelemetry "github.com/bibhuyash/tradeedge/internal/strategy/telemetry"
)

var (
	processingBuckets = []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1}
	lagBuckets        = []float64{.01, .025, .05, .1, .25, .5, 1, 2, 5, 10, 30, 60}
	operationBuckets  = []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 300}
)

type Recorder struct {
	registry *prometheus.Registry

	observations        *prometheus.CounterVec
	quality             *prometheus.CounterVec
	normalization       *prometheus.HistogramVec
	transportLag        *prometheus.HistogramVec
	eventAge            *prometheus.GaugeVec
	reorderDepth        *prometheus.GaugeVec
	ready               *prometheus.GaugeVec
	transitions         *prometheus.CounterVec
	coverage            *prometheus.GaugeVec
	missing             *prometheus.CounterVec
	commits             *prometheus.CounterVec
	commitTime          prometheus.Histogram
	datasetBytes        prometheus.Gauge
	checksums           prometheus.Counter
	replayEvents        *prometheus.CounterVec
	replayTime          *prometheus.HistogramVec
	consumerTime        prometheus.Histogram
	backpressure        prometheus.Counter
	pause               prometheus.Counter
	strategyEvaluations *prometheus.CounterVec
	strategyDuration    *prometheus.HistogramVec
	strategyPublish     *prometheus.HistogramVec
	strategyStateBytes  prometheus.Gauge
	strategyInFlight    prometheus.Gauge
	executionPlans      *prometheus.CounterVec
	executionSubmits    *prometheus.CounterVec
	executionEvents     *prometheus.CounterVec
	executionReconciles *prometheus.CounterVec
	executionIssues     *prometheus.CounterVec
	executionRepairs    *prometheus.CounterVec
	executionScenarios  *prometheus.CounterVec
	executionDuration   *prometheus.HistogramVec
	executionInFlight   *prometheus.GaugeVec
	executionUnknown    prometheus.Gauge
	brokerReads         *prometheus.CounterVec
	brokerReadDuration  *prometheus.HistogramVec
	brokerReadInFlight  prometheus.Gauge
	riskDecisions       *prometheus.CounterVec
	riskRules           *prometheus.CounterVec
	riskDuration        *prometheus.HistogramVec
	riskPublish         prometheus.Histogram
	riskInFlight        prometheus.Gauge
	notificationEvents  *prometheus.CounterVec
	notificationQueue   prometheus.Gauge

	mu            sync.Mutex
	readinessSeen map[string]string
}

func New() *Recorder {
	r := &Recorder{
		registry:            prometheus.NewRegistry(),
		observations:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_marketdata_observations_total", Help: "Market-data observations received."}, []string{"provider", "exchange", "segment", "event_kind", "candle_interval", "outcome"}),
		quality:             prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_marketdata_quality_total", Help: "Market-data quality dispositions."}, []string{"provider", "exchange", "segment", "event_kind", "candle_interval", "quality_code", "disposition"}),
		normalization:       prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tradeedge_marketdata_normalization_duration_seconds", Help: "Observation normalization duration.", Buckets: processingBuckets}, streamLabels()),
		transportLag:        prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tradeedge_marketdata_transport_lag_seconds", Help: "Exchange-to-ingestion lag.", Buckets: lagBuckets}, streamLabels()),
		eventAge:            prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tradeedge_marketdata_event_age_seconds", Help: "Current accepted event age."}, streamLabels()),
		reorderDepth:        prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tradeedge_marketdata_reorder_buffer_depth", Help: "Current reorder-buffer depth."}, streamLabels()),
		ready:               prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tradeedge_marketdata_ready", Help: "Market-data readiness by bounded scope."}, []string{"scope_type", "provider", "watchlist", "state", "reason"}),
		transitions:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_marketdata_readiness_transitions_total", Help: "Market-data readiness state transitions."}, []string{"scope_type", "provider", "watchlist", "state", "reason"}),
		coverage:            prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tradeedge_marketdata_coverage_ratio", Help: "Required stream coverage."}, []string{"scope_type", "provider", "watchlist"}),
		missing:             prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_marketdata_missing_intervals_total", Help: "Missing expected candle intervals."}, streamLabels()),
		commits:             prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_marketdata_dataset_commits_total", Help: "Dataset commit attempts."}, []string{"outcome"}),
		commitTime:          prometheus.NewHistogram(prometheus.HistogramOpts{Name: "tradeedge_marketdata_dataset_commit_duration_seconds", Help: "Dataset commit duration.", Buckets: operationBuckets}),
		datasetBytes:        prometheus.NewGauge(prometheus.GaugeOpts{Name: "tradeedge_marketdata_dataset_bytes", Help: "Bytes in the most recently committed dataset."}),
		checksums:           prometheus.NewCounter(prometheus.CounterOpts{Name: "tradeedge_marketdata_checksum_failures_total", Help: "Dataset checksum failures."}),
		replayEvents:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_marketdata_replay_events_total", Help: "Events replayed."}, []string{"terminal_state"}),
		replayTime:          prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tradeedge_marketdata_replay_duration_seconds", Help: "Replay duration.", Buckets: operationBuckets}, []string{"terminal_state"}),
		consumerTime:        prometheus.NewHistogram(prometheus.HistogramOpts{Name: "tradeedge_marketdata_replay_consumer_duration_seconds", Help: "Replay consumer duration.", Buckets: processingBuckets}),
		backpressure:        prometheus.NewCounter(prometheus.CounterOpts{Name: "tradeedge_marketdata_replay_backpressure_seconds_total", Help: "Time spent synchronously invoking replay consumers."}),
		pause:               prometheus.NewCounter(prometheus.CounterOpts{Name: "tradeedge_marketdata_replay_pause_seconds_total", Help: "Time replay remained paused."}),
		strategyEvaluations: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_strategy_evaluations_total", Help: "Strategy runner outcomes."}, []string{"definition", "outcome"}),
		strategyDuration:    prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tradeedge_strategy_evaluation_duration_seconds", Help: "Strategy evaluation duration.", Buckets: processingBuckets}, []string{"definition", "outcome"}),
		strategyPublish:     prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tradeedge_strategy_publication_duration_seconds", Help: "Atomic publication duration.", Buckets: processingBuckets}, []string{"definition", "outcome"}),
		strategyStateBytes:  prometheus.NewGauge(prometheus.GaugeOpts{Name: "tradeedge_strategy_state_bytes", Help: "Most recent bounded strategy state size."}),
		strategyInFlight:    prometheus.NewGauge(prometheus.GaugeOpts{Name: "tradeedge_strategy_in_flight", Help: "Current strategy evaluations in flight."}),
		executionPlans:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_execution_plans_total", Help: "Execution plan outcomes."}, []string{"outcome"}),
		executionSubmits:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_execution_submissions_total", Help: "Execution submission outcomes."}, []string{"outcome"}),
		executionEvents:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_execution_order_events_total", Help: "OMS order-event outcomes."}, []string{"outcome", "detail"}),
		executionReconciles: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_execution_reconciliation_total", Help: "Execution reconciliation outcomes."}, []string{"outcome"}),
		executionIssues:     prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_execution_reconciliation_issues_total", Help: "Bounded reconciliation issue kinds."}, []string{"kind", "outcome"}),
		executionRepairs:    prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_execution_reconciliation_repairs_total", Help: "Bounded reconciliation repairs."}, []string{"kind"}),
		executionScenarios:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_paper_broker_scenarios_total", Help: "Deterministic paper-broker scenario outcomes."}, []string{"scenario", "outcome"}),
		executionDuration:   prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tradeedge_execution_duration_seconds", Help: "Execution operation duration.", Buckets: processingBuckets}, []string{"operation", "outcome"}),
		executionInFlight:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tradeedge_execution_in_flight", Help: "Bounded execution work in flight."}, []string{"scope"}),
		executionUnknown:    prometheus.NewGauge(prometheus.GaugeOpts{Name: "tradeedge_execution_unknown_orders", Help: "Current OMS orders in UNKNOWN state."}),
		brokerReads:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_broker_connectivity_reads_total", Help: "Read-only broker connectivity outcomes."}, []string{"operation", "outcome"}),
		brokerReadDuration:  prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tradeedge_broker_connectivity_read_duration_seconds", Help: "Read-only broker connectivity duration.", Buckets: processingBuckets}, []string{"operation", "outcome"}),
		brokerReadInFlight:  prometheus.NewGauge(prometheus.GaugeOpts{Name: "tradeedge_broker_connectivity_reads_in_flight", Help: "Current read-only broker operations."}),
		riskDecisions:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_risk_decisions_total", Help: "Portfolio-risk runner outcomes."}, []string{"outcome"}),
		riskRules:           prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_risk_rule_results_total", Help: "Bounded production risk-rule results."}, []string{"rule_id", "status", "effect", "severity"}),
		riskDuration:        prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tradeedge_risk_evaluation_duration_seconds", Help: "Portfolio-risk evaluation duration.", Buckets: processingBuckets}, []string{"outcome"}),
		riskPublish:         prometheus.NewHistogram(prometheus.HistogramOpts{Name: "tradeedge_risk_publication_duration_seconds", Help: "Atomic portfolio-risk publication duration.", Buckets: processingBuckets}),
		riskInFlight:        prometheus.NewGauge(prometheus.GaugeOpts{Name: "tradeedge_risk_in_flight", Help: "Current portfolio-risk evaluations in flight."}),
		notificationEvents:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_operational_notifications_total", Help: "Bounded provider-neutral notification outcomes."}, []string{"operation", "outcome", "severity", "category", "kind", "reason"}),
		notificationQueue:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "tradeedge_operational_notification_queue_depth", Help: "Current bounded notification queue depth."}),
		readinessSeen:       make(map[string]string),
	}
	r.registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	r.registry.MustRegister(r.observations, r.quality, r.normalization, r.transportLag, r.eventAge,
		r.reorderDepth, r.ready, r.transitions, r.coverage, r.missing, r.commits, r.commitTime,
		r.datasetBytes, r.checksums, r.replayEvents, r.replayTime, r.consumerTime, r.backpressure, r.pause)
	r.registry.MustRegister(r.strategyEvaluations, r.strategyDuration, r.strategyPublish,
		r.strategyStateBytes, r.strategyInFlight)
	r.registry.MustRegister(r.executionPlans, r.executionSubmits, r.executionEvents,
		r.executionReconciles, r.executionIssues, r.executionRepairs, r.executionScenarios,
		r.executionDuration, r.executionInFlight, r.executionUnknown)
	r.registry.MustRegister(r.brokerReads, r.brokerReadDuration, r.brokerReadInFlight)
	r.registry.MustRegister(r.riskDecisions, r.riskRules, r.riskDuration, r.riskPublish, r.riskInFlight)
	r.registry.MustRegister(r.notificationEvents, r.notificationQueue)
	return r
}

// RecordNotification uses only fixed event vocabularies. Free-form identities
// and provider errors are collapsed before becoming metric labels.
func (r *Recorder) RecordNotification(event notification.MetricEvent) {
	reason := boundedNotificationReason(event.Reason)
	r.notificationEvents.WithLabelValues(event.Operation, event.Outcome, event.Severity, event.Category, event.Kind, reason).Inc()
	r.notificationQueue.Set(float64(event.QueueDepth))
}
func boundedNotificationReason(value string) string {
	switch value {
	case "", "COALESCE_WINDOW", "QUEUE_FULL", "SHUTDOWN", "DUPLICATE", "IDENTITY_COLLISION", "REPLAY_POLICY", "PROVIDER_DISABLED", "PRESENTATION_POLICY", "EVICTED_BY_CRITICAL", "EVICTED_BY_WARNING", "RATE_LIMITED", "TRANSPORT", "SERVER_ERROR", "PERMANENT_REQUEST", "PERMANENT_FAILURE", "DISPATCHER_PANIC", "SHUTDOWN_TIMEOUT":
		return value
	default:
		return "OTHER"
	}
}

func streamLabels() []string {
	return []string{"provider", "exchange", "segment", "event_kind", "candle_interval"}
}

func labels(d telemetry.Dimensions) []string {
	return []string{string(d.Provider), string(d.Exchange), string(d.Segment), string(d.Kind), string(d.Interval)}
}

func (r *Recorder) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}

func (r *Recorder) Registry() *prometheus.Registry { return r.registry }

func (r *Recorder) Observation(d telemetry.Dimensions, outcome string) {
	r.observations.WithLabelValues(append(labels(d), outcome)...).Inc()
}
func (r *Recorder) Quality(d telemetry.Dimensions, code model.QualityCode, disposition model.Disposition) {
	r.quality.WithLabelValues(append(labels(d), string(code), string(disposition))...).Inc()
}
func (r *Recorder) Normalization(d telemetry.Dimensions, elapsed time.Duration) {
	r.normalization.WithLabelValues(labels(d)...).Observe(elapsed.Seconds())
}
func (r *Recorder) TransportLag(d telemetry.Dimensions, lag time.Duration) {
	r.transportLag.WithLabelValues(labels(d)...).Observe(lag.Seconds())
}
func (r *Recorder) EventAge(d telemetry.Dimensions, age time.Duration) {
	r.eventAge.WithLabelValues(labels(d)...).Set(age.Seconds())
}
func (r *Recorder) ReorderDepth(d telemetry.Dimensions, depth int) {
	r.reorderDepth.WithLabelValues(labels(d)...).Set(float64(depth))
}
func (r *Recorder) Readiness(scopeType, provider, watchlist, state, reason string, ready bool, coverage float64) {
	value := 0.0
	if ready {
		value = 1
	}
	r.ready.WithLabelValues(scopeType, provider, watchlist, state, reason).Set(value)
	r.coverage.WithLabelValues(scopeType, provider, watchlist).Set(coverage)
	key := scopeType + "\x00" + provider + "\x00" + watchlist
	current := state + "\x00" + reason
	r.mu.Lock()
	if r.readinessSeen[key] != current {
		r.transitions.WithLabelValues(scopeType, provider, watchlist, state, reason).Inc()
		r.readinessSeen[key] = current
	}
	r.mu.Unlock()
}
func (r *Recorder) MissingIntervals(d telemetry.Dimensions, count int) {
	r.missing.WithLabelValues(labels(d)...).Add(float64(count))
}
func (r *Recorder) DatasetCommit(outcome string, elapsed time.Duration, bytes int64) {
	r.commits.WithLabelValues(outcome).Inc()
	r.commitTime.Observe(elapsed.Seconds())
	r.datasetBytes.Set(float64(bytes))
}
func (r *Recorder) ChecksumFailure() { r.checksums.Inc() }
func (r *Recorder) Replay(state string, events uint64, elapsed, consumer, backpressure, pause time.Duration) {
	r.replayEvents.WithLabelValues(state).Add(float64(events))
	r.replayTime.WithLabelValues(state).Observe(elapsed.Seconds())
	r.consumerTime.Observe(consumer.Seconds())
	r.backpressure.Add(backpressure.Seconds())
	r.pause.Add(pause.Seconds())
}

func BoolLabel(value bool) string { return strconv.FormatBool(value) }

func (r *Recorder) Record(event strategytelemetry.Event) {
	definition := event.Definition.String()
	if definition == "" {
		definition = "unknown"
	}
	r.strategyEvaluations.WithLabelValues(definition, event.Outcome).Inc()
	r.strategyDuration.WithLabelValues(definition, event.Outcome).Observe(event.Duration.Seconds())
	r.strategyPublish.WithLabelValues(definition, event.Outcome).Observe(event.Publish.Seconds())
	if event.StateBytes > 0 {
		r.strategyStateBytes.Set(float64(event.StateBytes))
	}
	r.strategyInFlight.Set(float64(event.InFlight))
}

type RiskRecorder struct{ recorder *Recorder }

// Risk exposes the same private registry through the provider-neutral risk recorder contract.
func (r *Recorder) Risk() risktelemetry.Recorder { return RiskRecorder{recorder: r} }

// Record implements the provider-neutral risk telemetry contract without identity labels.
func (adapter RiskRecorder) Record(event risktelemetry.Event) {
	r := adapter.recorder
	if event.RuleID != "" {
		r.riskRules.WithLabelValues(string(event.RuleID), string(event.Status),
			string(event.Effect), string(event.Severity)).Inc()
	}
	if event.Outcome != "" {
		r.riskDecisions.WithLabelValues(event.Outcome).Inc()
		if event.Duration > 0 {
			r.riskDuration.WithLabelValues(event.Outcome).Observe(event.Duration.Seconds())
		}
	}
	if event.Publish > 0 {
		r.riskPublish.Observe(event.Publish.Seconds())
	}
	r.riskInFlight.Set(float64(event.InFlight))
}

type ExecutionRecorder struct{ recorder *Recorder }

func (r *Recorder) Execution() executiontelemetry.Recorder { return ExecutionRecorder{recorder: r} }

func (adapter ExecutionRecorder) Record(event executiontelemetry.Event) {
	r := adapter.recorder
	operation, outcome := string(event.Operation), string(event.Outcome)
	if !executiontelemetry.ValidOperation(event.Operation) {
		operation = "invalid"
	}
	if !executiontelemetry.ValidOutcome(event.Outcome) {
		outcome = "invalid"
	}
	detail := executiontelemetry.BoundedDetail(event.Detail)
	switch event.Operation {
	case executiontelemetry.OperationPlan:
		r.executionPlans.WithLabelValues(outcome).Inc()
	case executiontelemetry.OperationSubmission:
		r.executionSubmits.WithLabelValues(outcome).Inc()
	case executiontelemetry.OperationOrderEvent, executiontelemetry.OperationPublication, executiontelemetry.OperationCancellation:
		r.executionEvents.WithLabelValues(outcome, detail).Inc()
	case executiontelemetry.OperationReconciliation:
		r.executionReconciles.WithLabelValues(outcome).Inc()
	case executiontelemetry.OperationMismatch:
		r.executionIssues.WithLabelValues(detail, outcome).Inc()
	case executiontelemetry.OperationRepair:
		r.executionRepairs.WithLabelValues(detail).Inc()
	case executiontelemetry.OperationPaperScenario:
		r.executionScenarios.WithLabelValues(detail, outcome).Inc()
	}
	if event.Duration > 0 {
		r.executionDuration.WithLabelValues(operation, outcome).Observe(event.Duration.Seconds())
	}
	if event.Operation == executiontelemetry.OperationPlan {
		r.executionInFlight.WithLabelValues("plans").Set(float64(event.InFlight))
	}
	if event.Operation == executiontelemetry.OperationOrderEvent {
		r.executionInFlight.WithLabelValues("orders").Set(float64(event.InFlight))
	}
	if event.HasUnknownOrders {
		r.executionUnknown.Set(float64(event.UnknownOrders))
	}
}

type BrokerRecorder struct{ recorder *Recorder }

// Broker exposes bounded provider-neutral connectivity metrics. Provider,
// account, instrument, token, request, path, and error values are never labels.
func (r *Recorder) Broker() brokertelemetry.Recorder { return BrokerRecorder{recorder: r} }

func (adapter BrokerRecorder) Record(event brokertelemetry.Event) {
	if !brokertelemetry.Valid(event) {
		return
	}
	operation, outcome := string(event.Operation), string(event.Outcome)
	adapter.recorder.brokerReads.WithLabelValues(operation, outcome).Inc()
	if event.Duration > 0 {
		adapter.recorder.brokerReadDuration.WithLabelValues(operation, outcome).Observe(event.Duration.Seconds())
	}
	adapter.recorder.brokerReadInFlight.Set(float64(event.InFlight))
}
