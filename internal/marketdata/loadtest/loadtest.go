package loadtest

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
	"github.com/bibhuyash/tradeedge/internal/marketdata/ingest"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
)

const (
	reportSchemaVersion       = 1
	maxReorderDepth           = 10000
	maxPeakHeapGrowth         = 64 << 20
	maxSoakHeapGrowth         = 16 << 20
	goroutineTolerance        = 2
	maxCancellationDuration   = 500 * time.Millisecond
	readinessWarmup           = 30 * time.Second
	cancellationProbeAttempts = 5
)

type Profile string

const (
	ProfileNormal       Profile = "normal"
	ProfileBurst        Profile = "burst"
	ProfileDuplicate    Profile = "duplicate"
	ProfileLate         Profile = "late"
	ProfileMalformed    Profile = "malformed"
	ProfileSlowConsumer Profile = "slow-consumer"
	ProfileSoak         Profile = "soak"
)

type Config struct {
	Profile       Profile
	Instruments   int
	EventsPerSec  int
	SimulatedFor  time.Duration
	ConsumerDelay time.Duration
	Buffer        int
	Realtime      bool
}

type Report struct {
	SchemaVersion                        int      `json:"schema_version"`
	Profile                              Profile  `json:"profile"`
	ConfiguredDuration                   string   `json:"configured_duration"`
	ConfiguredInstrumentCount            int      `json:"configured_instrument_count"`
	ConfiguredBufferCapacity             int      `json:"configured_buffer_capacity"`
	GeneratedEventCount                  int      `json:"generated_event_count"`
	AcceptedEventCount                   int      `json:"accepted_event_count"`
	DuplicateCount                       int      `json:"duplicate_count"`
	RejectedCount                        int      `json:"rejected_count"`
	MalformedCount                       int      `json:"malformed_count"`
	LateCount                            int      `json:"late_count"`
	QuarantinedCount                     int      `json:"quarantined_count"`
	DownstreamDeliveryCount              int      `json:"downstream_delivery_count"`
	UniqueDownstreamDeliveryCount        int      `json:"unique_downstream_delivery_count"`
	UnexpectedEventLossCount             int      `json:"unexpected_event_loss_count"`
	UnexpectedDuplicateDeliveryCount     int      `json:"unexpected_duplicate_delivery_count"`
	PeakReorderDepth                     int      `json:"peak_reorder_depth"`
	StartingGoroutineCount               int      `json:"starting_goroutine_count"`
	PeakGoroutineCount                   int      `json:"peak_goroutine_count"`
	EndingGoroutineCount                 int      `json:"ending_goroutine_count"`
	StartingHeapAllocationBytes          uint64   `json:"starting_heap_allocation_bytes"`
	PeakHeapAllocationBytes              uint64   `json:"peak_heap_allocation_bytes"`
	EndingHeapAllocationBytes            uint64   `json:"ending_heap_allocation_bytes"`
	PeakHeapGrowthBytes                  int64    `json:"peak_heap_growth_bytes"`
	SoakHeapGrowthAfterWarmupBytes       int64    `json:"soak_heap_growth_after_warmup_bytes"`
	GarbageCollectionCycles              uint32   `json:"garbage_collection_cycles"`
	ReadinessTransitionCount             int      `json:"readiness_transition_count"`
	ReadinessStates                      []string `json:"readiness_states"`
	MaximumCancellationDuration          int64    `json:"maximum_cancellation_duration_nanoseconds"`
	TotalProcessingDuration              int64    `json:"total_processing_duration_nanoseconds"`
	ObservationsPerSecond                float64  `json:"observations_per_second"`
	P99ProcessingLatency                 int64    `json:"p99_processing_latency_nanoseconds"`
	BackpressureDuration                 int64    `json:"backpressure_duration_nanoseconds"`
	ConcurrentConsumerInvocationDetected bool     `json:"concurrent_consumer_invocation_detected"`
	FinalReadinessState                  string   `json:"final_readiness_state"`
	ExpectedFinalReadinessState          string   `json:"expected_final_readiness_state"`
	Passed                               bool     `json:"passed"`
	FailureReasons                       []string `json:"failure_reasons"`
}

func DefaultConfig(profile Profile) (Config, error) {
	config := Config{Profile: profile, Instruments: 250, SimulatedFor: 60 * time.Second, Buffer: maxReorderDepth}
	switch profile {
	case ProfileNormal, ProfileDuplicate, ProfileLate, ProfileMalformed:
		config.EventsPerSec = 4
	case ProfileBurst:
		config.EventsPerSec = 20
	case ProfileSlowConsumer:
		config.Instruments, config.EventsPerSec, config.SimulatedFor = 100, 100, time.Second
		config.ConsumerDelay = time.Millisecond
	case ProfileSoak:
		config.EventsPerSec = 4
		config.SimulatedFor = 30 * time.Minute
		config.Realtime = true
	default:
		return Config{}, fmt.Errorf("unknown load profile %q", profile)
	}
	return config, nil
}

func Run(ctx context.Context, config Config) (Report, error) {
	report := Report{
		SchemaVersion: reportSchemaVersion, Profile: config.Profile,
		ConfiguredDuration:        config.SimulatedFor.String(),
		ConfiguredInstrumentCount: config.Instruments,
		ConfiguredBufferCapacity:  config.Buffer,
		FailureReasons:            []string{},
	}
	if config.Instruments <= 0 || config.Instruments > 250 || config.EventsPerSec <= 0 ||
		config.SimulatedFor <= 0 || config.Buffer <= 0 {
		return report, errors.New("invalid load-test configuration")
	}

	runtime.GC()
	var startingMemory runtime.MemStats
	runtime.ReadMemStats(&startingMemory)
	report.StartingHeapAllocationBytes = startingMemory.HeapAlloc
	report.PeakHeapAllocationBytes = startingMemory.HeapAlloc
	report.StartingGoroutineCount = runtime.NumGoroutine()
	report.PeakGoroutineCount = report.StartingGoroutineCount

	base := time.Date(2026, time.July, 17, 3, 45, 0, 0, time.UTC)
	normalEventCount := profileEventCount(config)
	ids := make([]domain.InstrumentID, config.Instruments)
	for index := range ids {
		ids[index], _ = domain.InstrumentIDFromCanonicalKey(fmt.Sprintf("loadtest-%03d", index))
	}
	orderer, err := ingest.NewOrderer(2*time.Second, config.Buffer)
	if err != nil {
		return report, err
	}
	delivery := newDeliveryAudit(normalEventCount)
	latencies := newLatencyHistogram()
	clock := &harnessClock{now: base}
	evaluator, err := newReadinessEvaluator(clock, base, ids)
	if err != nil {
		return report, err
	}
	readinessAudit := newReadinessAudit()
	readinessAudit.Observe(evaluator.Snapshot(ctx).State)
	resources := resourceAudit{
		startHeap: startingMemory.HeapAlloc, startGC: startingMemory.NumGC,
		peakHeap: startingMemory.HeapAlloc, peakGoroutines: report.PeakGoroutineCount,
	}
	resources.Sample()

	var consumerActive bool
	var backpressure time.Duration
	started := time.Now()
	consume := func(events []model.Event) error {
		for _, event := range events {
			if err := ctx.Err(); err != nil {
				return err
			}
			if consumerActive {
				report.ConcurrentConsumerInvocationDetected = true
			}
			consumerActive = true
			consumerStarted := time.Now()
			if config.ConsumerDelay > 0 {
				if err := waitUntil(ctx, time.Now().Add(config.ConsumerDelay)); err != nil {
					consumerActive = false
					return err
				}
			}
			backpressure += time.Since(consumerStarted)
			duplicate, outOfRange := delivery.Observe(event.Provenance().SourceSequence)
			if duplicate {
				report.UnexpectedDuplicateDeliveryCount++
			}
			if outOfRange {
				report.FailureReasons = append(report.FailureReasons, "downstream event sequence is outside the generated range")
			}
			report.DownstreamDeliveryCount++
			evaluator.Accepted(event)
			consumerActive = false
		}
		return nil
	}

	second, withinSecond := 0, 0
	var warmupHeap uint64
	warmupCaptured := false
	for index := 0; index < normalEventCount; index++ {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		rate := rateForSecond(config, second)
		perSecond := config.Instruments * rate
		if withinSecond >= perSecond {
			second++
			withinSecond = 0
			rate = rateForSecond(config, second)
			perSecond = config.Instruments * rate
		}
		if config.Realtime && withinSecond == 0 {
			if err := waitUntil(ctx, started.Add(time.Duration(second)*time.Second)); err != nil {
				return report, err
			}
		}

		at := base.Add(time.Duration(second)*time.Second +
			time.Duration(withinSecond/config.Instruments)*time.Second/time.Duration(rate))
		price, priceErr := domain.NewPrice(int64(10000+index), "INR")
		if priceErr != nil {
			return report, priceErr
		}
		event, eventErr := model.NewQuoteEvent(model.QuoteSpec{
			InstrumentID: ids[index%len(ids)], LastPrice: price, Volume: int64(index),
			ExchangeTime: at, IngestedAt: at.Add(time.Millisecond),
			Provenance: model.Provenance{
				Provider: "loadtest", ProviderToken: fmt.Sprintf("%d", index%len(ids)),
				MasterVersion: "loadtest-v1", SourceSequence: uint64(index), HasSequence: true,
			},
		})
		if eventErr != nil {
			return report, eventErr
		}
		report.GeneratedEventCount++
		pushStarted := time.Now()
		ready, disposition, pushErr := orderer.Push(event)
		latencies.Observe(time.Since(pushStarted))
		if pushErr != nil {
			return report, pushErr
		}
		switch disposition {
		case ingest.PushAccepted:
			report.AcceptedEventCount++
		case ingest.PushDuplicate:
			report.DuplicateCount++
		case ingest.PushLate:
			report.LateCount++
		default:
			report.FailureReasons = append(report.FailureReasons, "normal event received an unknown classification")
		}
		if err := consume(ready); err != nil {
			return report, err
		}
		if depth := orderer.Depth(); depth > report.PeakReorderDepth {
			report.PeakReorderDepth = depth
		}
		// Sample often enough to observe intra-second burst allocation without
		// making runtime.ReadMemStats part of every event's hot path.
		if (index+1)%1000 == 0 {
			resources.Sample()
		}
		if config.Profile == ProfileDuplicate && index%5 == 0 {
			report.GeneratedEventCount++
			_, duplicateDisposition, duplicateErr := orderer.Push(event)
			if duplicateErr != nil {
				return report, duplicateErr
			}
			if duplicateDisposition != ingest.PushDuplicate {
				report.FailureReasons = append(report.FailureReasons, "exact duplicate was not classified as duplicate")
			} else {
				report.DuplicateCount++
			}
		}

		withinSecond++
		if withinSecond >= perSecond {
			clock.now = base.Add(time.Duration(second+1) * time.Second)
			readinessAudit.Observe(evaluator.Snapshot(ctx).State)
			resources.Sample()
			if config.Profile == ProfileSoak && second+1 >= 300 && !warmupCaptured {
				runtime.GC()
				resources.Sample()
				warmupHeap = resources.currentHeap
				warmupCaptured = true
			}
		}
	}

	if config.Profile == ProfileLate {
		if err := injectLateEvents(ctx, orderer, ids, base, normalEventCount, &report); err != nil {
			return report, err
		}
	}
	if config.Profile == ProfileMalformed {
		injectMalformedEvents(normalEventCount, &report)
	}
	if err := consume(orderer.Flush()); err != nil {
		return report, err
	}
	resources.Sample()

	clock.now = base.Add(config.SimulatedFor)
	finalSnapshot := evaluator.Snapshot(ctx)
	readinessAudit.Observe(finalSnapshot.State)
	report.FinalReadinessState = string(finalSnapshot.State)
	report.ExpectedFinalReadinessState = string(expectedReadiness(config))

	maximumCancellation, cancellationErr := measureCancellation(resources.Sample)
	if cancellationErr != nil {
		report.FailureReasons = append(report.FailureReasons, cancellationErr.Error())
	}
	report.MaximumCancellationDuration = maximumCancellation.Nanoseconds()

	runtime.GC()
	runtime.Gosched()
	resources.Sample()
	var endingMemory runtime.MemStats
	runtime.ReadMemStats(&endingMemory)
	report.EndingHeapAllocationBytes = endingMemory.HeapAlloc
	report.EndingGoroutineCount = runtime.NumGoroutine()
	if report.EndingGoroutineCount > resources.peakGoroutines {
		resources.peakGoroutines = report.EndingGoroutineCount
	}
	if endingMemory.HeapAlloc > resources.peakHeap {
		resources.peakHeap = endingMemory.HeapAlloc
	}
	report.PeakHeapAllocationBytes = resources.peakHeap
	report.PeakGoroutineCount = resources.peakGoroutines
	report.GarbageCollectionCycles = endingMemory.NumGC - startingMemory.NumGC
	report.PeakHeapGrowthBytes = positiveDifference(resources.peakHeap, startingMemory.HeapAlloc)
	if warmupCaptured {
		report.SoakHeapGrowthAfterWarmupBytes = signedDifference(endingMemory.HeapAlloc, warmupHeap)
	}

	report.UniqueDownstreamDeliveryCount = delivery.Unique()
	report.UnexpectedEventLossCount = report.AcceptedEventCount - report.UniqueDownstreamDeliveryCount
	if report.UnexpectedEventLossCount < 0 {
		report.UnexpectedEventLossCount = 0
	}
	report.QuarantinedCount = report.LateCount + report.MalformedCount
	report.ReadinessTransitionCount = readinessAudit.transitions
	report.ReadinessStates = append([]string(nil), readinessAudit.states...)
	report.TotalProcessingDuration = time.Since(started).Nanoseconds()
	report.BackpressureDuration = backpressure.Nanoseconds()
	report.P99ProcessingLatency = latencies.P99().Nanoseconds()
	if elapsed := time.Duration(report.TotalProcessingDuration); elapsed > 0 {
		report.ObservationsPerSecond = float64(report.GeneratedEventCount) / elapsed.Seconds()
	}

	report.evaluate(config, normalEventCount)
	report.Passed = len(report.FailureReasons) == 0
	return report, nil
}

func (r *Report) evaluate(config Config, normalEventCount int) {
	expectedDuplicates := 0
	expectedLate := 0
	expectedMalformed := 0
	switch config.Profile {
	case ProfileDuplicate:
		expectedDuplicates = (normalEventCount + 4) / 5
	case ProfileLate:
		expectedLate = normalEventCount / 20
	case ProfileMalformed:
		expectedMalformed = normalEventCount / 100
	}
	expectedGenerated := normalEventCount + expectedDuplicates + expectedLate + expectedMalformed
	if r.GeneratedEventCount != expectedGenerated {
		r.failf("generated event count %d does not match expected %d", r.GeneratedEventCount, expectedGenerated)
	}
	if r.AcceptedEventCount != normalEventCount {
		r.failf("accepted event count %d does not match expected %d", r.AcceptedEventCount, normalEventCount)
	}
	if r.DuplicateCount != expectedDuplicates {
		r.failf("duplicate count %d does not match expected %d", r.DuplicateCount, expectedDuplicates)
	}
	if r.LateCount != expectedLate {
		r.failf("late count %d does not match expected %d", r.LateCount, expectedLate)
	}
	if r.MalformedCount != expectedMalformed || r.RejectedCount != expectedMalformed {
		r.failf("malformed/rejected counts %d/%d do not match expected %d",
			r.MalformedCount, r.RejectedCount, expectedMalformed)
	}
	if r.QuarantinedCount != expectedLate+expectedMalformed {
		r.failf("quarantined count %d does not match expected %d",
			r.QuarantinedCount, expectedLate+expectedMalformed)
	}
	if r.UnexpectedEventLossCount != 0 {
		r.failf("unexpected event loss count is %d", r.UnexpectedEventLossCount)
	}
	if r.UnexpectedDuplicateDeliveryCount != 0 {
		r.failf("unexpected duplicate downstream delivery count is %d", r.UnexpectedDuplicateDeliveryCount)
	}
	if r.DownstreamDeliveryCount != r.AcceptedEventCount {
		r.failf("downstream delivery count %d does not match accepted count %d",
			r.DownstreamDeliveryCount, r.AcceptedEventCount)
	}
	if r.ConcurrentConsumerInvocationDetected {
		r.FailureReasons = append(r.FailureReasons, "consumer was invoked concurrently")
	}
	if r.PeakReorderDepth > maxReorderDepth || r.PeakReorderDepth > config.Buffer {
		r.failf("peak reorder depth %d exceeded its bounded capacity", r.PeakReorderDepth)
	}
	if r.PeakHeapGrowthBytes > maxPeakHeapGrowth {
		r.failf("peak heap growth %d exceeded %d bytes", r.PeakHeapGrowthBytes, maxPeakHeapGrowth)
	}
	if config.Profile == ProfileSoak && r.SoakHeapGrowthAfterWarmupBytes > maxSoakHeapGrowth {
		r.failf("post-warm-up soak heap growth %d exceeded %d bytes",
			r.SoakHeapGrowthAfterWarmupBytes, maxSoakHeapGrowth)
	}
	if r.EndingGoroutineCount > r.StartingGoroutineCount+goroutineTolerance {
		r.failf("ending goroutine count %d exceeded start %d plus tolerance %d",
			r.EndingGoroutineCount, r.StartingGoroutineCount, goroutineTolerance)
	}
	if time.Duration(r.MaximumCancellationDuration) > maxCancellationDuration {
		r.failf("maximum cancellation duration %s exceeded %s",
			time.Duration(r.MaximumCancellationDuration), maxCancellationDuration)
	}
	if r.FinalReadinessState != r.ExpectedFinalReadinessState {
		r.failf("final readiness state %s does not match expected %s",
			r.FinalReadinessState, r.ExpectedFinalReadinessState)
	}
	expectedTransitions := 1
	if config.SimulatedFor >= readinessWarmup {
		expectedTransitions = 2
	}
	if r.ReadinessTransitionCount != expectedTransitions {
		r.failf("readiness transition count %d does not match expected %d",
			r.ReadinessTransitionCount, expectedTransitions)
	}
	latencyLimit := 10 * time.Millisecond
	if config.Profile == ProfileBurst {
		latencyLimit = 50 * time.Millisecond
	}
	if time.Duration(r.P99ProcessingLatency) > latencyLimit {
		r.failf("p99 processing latency %s exceeded %s",
			time.Duration(r.P99ProcessingLatency), latencyLimit)
	}
	// A real-time soak intentionally runs at its configured feed cadence.
	// The normal and burst profiles provide the processing-capacity gate.
	if config.Profile != ProfileSlowConsumer && config.Profile != ProfileSoak && r.ObservationsPerSecond < 10000 {
		r.failf("processing rate %.2f observations/second is below 10000", r.ObservationsPerSecond)
	}
	if config.Profile == ProfileSoak && config.Realtime &&
		time.Duration(r.TotalProcessingDuration)+2*time.Second < config.SimulatedFor {
		r.failf("real-time soak completed in %s, shorter than configured %s",
			time.Duration(r.TotalProcessingDuration), config.SimulatedFor)
	}
}

func (r *Report) failf(format string, values ...any) {
	r.FailureReasons = append(r.FailureReasons, fmt.Sprintf(format, values...))
}

func profileEventCount(config Config) int {
	total := 0
	for second := 0; second < int(config.SimulatedFor/time.Second); second++ {
		total += config.Instruments * rateForSecond(config, second)
	}
	return total
}

func rateForSecond(config Config, second int) int {
	if config.Profile == ProfileSoak && second >= 300 && second%300 < 60 {
		return 20
	}
	return config.EventsPerSec
}

func injectLateEvents(
	ctx context.Context,
	orderer *ingest.Orderer,
	ids []domain.InstrumentID,
	base time.Time,
	normalEventCount int,
	report *Report,
) error {
	for index := 0; index < normalEventCount/20; index++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		price, _ := domain.NewPrice(int64(900000+index), "INR")
		event, err := model.NewQuoteEvent(model.QuoteSpec{
			InstrumentID: ids[index%len(ids)], LastPrice: price, Volume: int64(index),
			ExchangeTime: base, IngestedAt: base.Add(time.Millisecond),
			Provenance: model.Provenance{
				Provider: "loadtest", ProviderToken: fmt.Sprintf("%d", index%len(ids)),
				MasterVersion:  "loadtest-v1",
				SourceSequence: uint64(normalEventCount + index), HasSequence: true,
			},
		})
		if err != nil {
			return err
		}
		report.GeneratedEventCount++
		_, disposition, err := orderer.Push(event)
		if err != nil {
			return err
		}
		if disposition != ingest.PushLate {
			report.FailureReasons = append(report.FailureReasons, "late event was not classified as late")
		} else {
			report.LateCount++
		}
	}
	return nil
}

func injectMalformedEvents(normalEventCount int, report *Report) {
	for index := 0; index < normalEventCount/100; index++ {
		report.GeneratedEventCount++
		if _, err := domain.NewPrice(-1, "INR"); err == nil {
			report.FailureReasons = append(report.FailureReasons, "negative price was not rejected as malformed")
			continue
		}
		report.MalformedCount++
		report.RejectedCount++
	}
}

type deliveryAudit struct {
	seen   []uint64
	unique int
	limit  int
}

func newDeliveryAudit(limit int) *deliveryAudit {
	return &deliveryAudit{seen: make([]uint64, (limit+63)/64), limit: limit}
}

func (a *deliveryAudit) Observe(sequence uint64) (duplicate bool, outOfRange bool) {
	if sequence >= uint64(a.limit) {
		return false, true
	}
	index, bit := sequence/64, uint(sequence%64)
	mask := uint64(1) << bit
	if a.seen[index]&mask != 0 {
		return true, false
	}
	a.seen[index] |= mask
	a.unique++
	return false, false
}

func (a *deliveryAudit) Unique() int { return a.unique }

type latencyHistogram struct {
	bounds []time.Duration
	counts []uint64
	total  uint64
}

func newLatencyHistogram() *latencyHistogram {
	bounds := []time.Duration{
		500 * time.Microsecond, time.Millisecond, 2500 * time.Microsecond,
		5 * time.Millisecond, 10 * time.Millisecond, 25 * time.Millisecond,
		50 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond,
		500 * time.Millisecond, time.Second,
	}
	return &latencyHistogram{bounds: bounds, counts: make([]uint64, len(bounds)+1)}
}

func (h *latencyHistogram) Observe(value time.Duration) {
	for index, bound := range h.bounds {
		if value <= bound {
			h.counts[index]++
			h.total++
			return
		}
	}
	h.counts[len(h.counts)-1]++
	h.total++
}

func (h *latencyHistogram) P99() time.Duration {
	if h.total == 0 {
		return 0
	}
	target := (h.total*99 + 99) / 100
	var cumulative uint64
	for index, count := range h.counts {
		cumulative += count
		if cumulative >= target {
			if index < len(h.bounds) {
				return h.bounds[index]
			}
			return time.Duration(1<<63 - 1)
		}
	}
	return 0
}

type resourceAudit struct {
	startHeap      uint64
	startGC        uint32
	currentHeap    uint64
	peakHeap       uint64
	peakGoroutines int
}

func (a *resourceAudit) Sample() {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	a.currentHeap = memory.HeapAlloc
	if memory.HeapAlloc > a.peakHeap {
		a.peakHeap = memory.HeapAlloc
	}
	if goroutines := runtime.NumGoroutine(); goroutines > a.peakGoroutines {
		a.peakGoroutines = goroutines
	}
}

type harnessClock struct{ now time.Time }

func (c *harnessClock) Now() time.Time { return c.now }

func newReadinessEvaluator(
	clock *harnessClock,
	base time.Time,
	ids []domain.InstrumentID,
) (*readiness.Evaluator, error) {
	location, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		return nil, err
	}
	local := base.In(location)
	date, _ := domain.NewCivilDate(local.Year(), local.Month(), local.Day())
	schedule, err := calendar.New(calendar.Spec{
		Source:   calendar.Source{Name: "loadtest", PublishedAt: base.Add(-time.Hour)},
		Timezone: "Asia/Kolkata", EffectiveFrom: date, EffectiveTo: date,
		Days: []calendar.TradingDay{{
			Exchange: domain.ExchangeNSE, Date: date, Status: calendar.DayTrading,
			Sessions: []calendar.Session{{
				Open: base.In(location), Close: base.Add(6*time.Hour + 15*time.Minute).In(location),
				Kind: calendar.SessionRegular,
			}},
		}},
	})
	if err != nil {
		return nil, err
	}
	requirements := make([]readiness.Requirement, len(ids))
	for index, id := range ids {
		requirements[index] = readiness.Requirement{
			Provider: "loadtest", InstrumentID: id, Exchange: domain.ExchangeNSE,
			Segment: domain.SegmentIndex, EventKind: model.EventKindQuote, Required: true,
		}
	}
	watchlist, err := readiness.NewWatchlist("loadtest", requirements)
	if err != nil {
		return nil, err
	}
	return readiness.New(clock, schedule, readiness.DefaultPolicy(), []readiness.Watchlist{watchlist})
}

type readinessAudit struct {
	last        readiness.State
	transitions int
	states      []string
}

func newReadinessAudit() *readinessAudit { return &readinessAudit{} }

func (a *readinessAudit) Observe(state readiness.State) {
	if state == a.last {
		return
	}
	a.last = state
	a.transitions++
	a.states = append(a.states, string(state))
}

func expectedReadiness(config Config) readiness.State {
	if config.SimulatedFor < readinessWarmup {
		return readiness.StateWarmingUp
	}
	return readiness.StateReady
}

func measureCancellation(sample func()) (time.Duration, error) {
	var maximum time.Duration
	for attempt := 0; attempt < cancellationProbeAttempts; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- waitUntil(ctx, time.Now().Add(time.Hour))
		}()
		runtime.Gosched()
		if sample != nil {
			sample()
		}
		started := time.Now()
		cancel()
		select {
		case err := <-done:
			elapsed := time.Since(started)
			if elapsed > maximum {
				maximum = elapsed
			}
			if !errors.Is(err, context.Canceled) {
				return maximum, errors.New("cancellation probe returned an unexpected result")
			}
		case <-time.After(maxCancellationDuration):
			return maxCancellationDuration, errors.New("cancellation probe exceeded 500ms")
		}
	}
	return maximum, nil
}

func waitUntil(ctx context.Context, target time.Time) error {
	wait := time.Until(target)
	if wait <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func positiveDifference(left, right uint64) int64 {
	if left <= right {
		return 0
	}
	return int64(left - right)
}

func signedDifference(left, right uint64) int64 {
	if left >= right {
		return int64(left - right)
	}
	return -int64(right - left)
}
