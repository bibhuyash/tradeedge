package prometheus

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/telemetry"
)

var (
	processingBuckets = []float64{.0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1}
	lagBuckets        = []float64{.01, .025, .05, .1, .25, .5, 1, 2, 5, 10, 30, 60}
	operationBuckets  = []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 300}
)

type Recorder struct {
	registry *prometheus.Registry

	observations  *prometheus.CounterVec
	quality       *prometheus.CounterVec
	normalization *prometheus.HistogramVec
	transportLag  *prometheus.HistogramVec
	eventAge      *prometheus.GaugeVec
	reorderDepth  *prometheus.GaugeVec
	ready         *prometheus.GaugeVec
	transitions   *prometheus.CounterVec
	coverage      *prometheus.GaugeVec
	missing       *prometheus.CounterVec
	commits       *prometheus.CounterVec
	commitTime    prometheus.Histogram
	datasetBytes  prometheus.Gauge
	checksums     prometheus.Counter
	replayEvents  *prometheus.CounterVec
	replayTime    *prometheus.HistogramVec
	consumerTime  prometheus.Histogram
	backpressure  prometheus.Counter
	pause         prometheus.Counter

	mu            sync.Mutex
	readinessSeen map[string]string
}

func New() *Recorder {
	r := &Recorder{
		registry:      prometheus.NewRegistry(),
		observations:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_marketdata_observations_total", Help: "Market-data observations received."}, []string{"provider", "exchange", "segment", "event_kind", "candle_interval", "outcome"}),
		quality:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_marketdata_quality_total", Help: "Market-data quality dispositions."}, []string{"provider", "exchange", "segment", "event_kind", "candle_interval", "quality_code", "disposition"}),
		normalization: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tradeedge_marketdata_normalization_duration_seconds", Help: "Observation normalization duration.", Buckets: processingBuckets}, streamLabels()),
		transportLag:  prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tradeedge_marketdata_transport_lag_seconds", Help: "Exchange-to-ingestion lag.", Buckets: lagBuckets}, streamLabels()),
		eventAge:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tradeedge_marketdata_event_age_seconds", Help: "Current accepted event age."}, streamLabels()),
		reorderDepth:  prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tradeedge_marketdata_reorder_buffer_depth", Help: "Current reorder-buffer depth."}, streamLabels()),
		ready:         prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tradeedge_marketdata_ready", Help: "Market-data readiness by bounded scope."}, []string{"scope_type", "provider", "watchlist", "state", "reason"}),
		transitions:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_marketdata_readiness_transitions_total", Help: "Market-data readiness state transitions."}, []string{"scope_type", "provider", "watchlist", "state", "reason"}),
		coverage:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tradeedge_marketdata_coverage_ratio", Help: "Required stream coverage."}, []string{"scope_type", "provider", "watchlist"}),
		missing:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_marketdata_missing_intervals_total", Help: "Missing expected candle intervals."}, streamLabels()),
		commits:       prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_marketdata_dataset_commits_total", Help: "Dataset commit attempts."}, []string{"outcome"}),
		commitTime:    prometheus.NewHistogram(prometheus.HistogramOpts{Name: "tradeedge_marketdata_dataset_commit_duration_seconds", Help: "Dataset commit duration.", Buckets: operationBuckets}),
		datasetBytes:  prometheus.NewGauge(prometheus.GaugeOpts{Name: "tradeedge_marketdata_dataset_bytes", Help: "Bytes in the most recently committed dataset."}),
		checksums:     prometheus.NewCounter(prometheus.CounterOpts{Name: "tradeedge_marketdata_checksum_failures_total", Help: "Dataset checksum failures."}),
		replayEvents:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "tradeedge_marketdata_replay_events_total", Help: "Events replayed."}, []string{"terminal_state"}),
		replayTime:    prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "tradeedge_marketdata_replay_duration_seconds", Help: "Replay duration.", Buckets: operationBuckets}, []string{"terminal_state"}),
		consumerTime:  prometheus.NewHistogram(prometheus.HistogramOpts{Name: "tradeedge_marketdata_replay_consumer_duration_seconds", Help: "Replay consumer duration.", Buckets: processingBuckets}),
		backpressure:  prometheus.NewCounter(prometheus.CounterOpts{Name: "tradeedge_marketdata_replay_backpressure_seconds_total", Help: "Time spent synchronously invoking replay consumers."}),
		pause:         prometheus.NewCounter(prometheus.CounterOpts{Name: "tradeedge_marketdata_replay_pause_seconds_total", Help: "Time replay remained paused."}),
		readinessSeen: make(map[string]string),
	}
	r.registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	r.registry.MustRegister(r.observations, r.quality, r.normalization, r.transportLag, r.eventAge,
		r.reorderDepth, r.ready, r.transitions, r.coverage, r.missing, r.commits, r.commitTime,
		r.datasetBytes, r.checksums, r.replayEvents, r.replayTime, r.consumerTime, r.backpressure, r.pause)
	return r
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
