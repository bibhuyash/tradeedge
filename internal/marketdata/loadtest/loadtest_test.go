package loadtest

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestReducedProfileReconcilesWithoutDropsOrConcurrentConsumers(t *testing.T) {
	report, err := Run(context.Background(), Config{
		Profile: ProfileSlowConsumer, Instruments: 10, EventsPerSec: 10,
		SimulatedFor: time.Second, Buffer: 10000,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.UnexpectedEventLossCount != 0 ||
		report.ConcurrentConsumerInvocationDetected ||
		report.EndingGoroutineCount > report.StartingGoroutineCount+2 {
		t.Fatalf("report = %#v", report)
	}
}

func TestCancellationIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	_, err := Run(ctx, Config{
		Profile: ProfileNormal, Instruments: 250, EventsPerSec: 4,
		SimulatedFor: time.Minute, Buffer: 10000,
	})
	if err == nil {
		t.Fatal("Run() error = nil")
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("cancellation exceeded 500ms")
	}
}

func TestQualityProfilesHaveExactCounts(t *testing.T) {
	tests := []struct {
		profile               Profile
		generated, accepted   int
		duplicates, late, bad int
	}{
		{ProfileDuplicate, 240, 200, 40, 0, 0},
		{ProfileLate, 210, 200, 0, 10, 0},
		{ProfileMalformed, 202, 200, 0, 0, 2},
	}
	for _, test := range tests {
		t.Run(string(test.profile), func(t *testing.T) {
			report, err := Run(context.Background(), Config{
				Profile: test.profile, Instruments: 10, EventsPerSec: 2,
				SimulatedFor: 10 * time.Second, Buffer: 10000,
			})
			if err != nil {
				t.Fatal(err)
			}
			if report.GeneratedEventCount != test.generated ||
				report.AcceptedEventCount != test.accepted ||
				report.DuplicateCount != test.duplicates || report.LateCount != test.late ||
				report.MalformedCount != test.bad || report.UnexpectedEventLossCount != 0 {
				t.Fatalf("report = %#v", report)
			}
		})
	}
}

func TestReportContainsStableReadinessAndResourceEvidence(t *testing.T) {
	report, err := Run(context.Background(), Config{
		Profile: ProfileSoak, Instruments: 10, EventsPerSec: 4,
		SimulatedFor: 40 * time.Second, Buffer: 10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || report.ConfiguredDuration != "40s" ||
		report.ConfiguredInstrumentCount != 10 ||
		report.GeneratedEventCount != 1600 ||
		report.DownstreamDeliveryCount != report.AcceptedEventCount ||
		report.FinalReadinessState != "READY" ||
		report.ExpectedFinalReadinessState != "READY" ||
		report.ReadinessTransitionCount != 2 ||
		report.StartingHeapAllocationBytes == 0 ||
		report.PeakHeapAllocationBytes < report.StartingHeapAllocationBytes ||
		time.Duration(report.MaximumCancellationDuration) > maxCancellationDuration {
		t.Fatalf("report evidence = %#v", report)
	}
	if len(report.ReadinessStates) != 2 ||
		report.ReadinessStates[0] != "WARMING_UP" ||
		report.ReadinessStates[1] != "READY" {
		t.Fatalf("readiness states = %#v", report.ReadinessStates)
	}
}

func TestDeliveryAuditDetectsDuplicateWithoutPerEventMap(t *testing.T) {
	audit := newDeliveryAudit(128)
	if duplicate, outside := audit.Observe(64); duplicate || outside {
		t.Fatalf("first observation duplicate=%t outside=%t", duplicate, outside)
	}
	if duplicate, outside := audit.Observe(64); !duplicate || outside {
		t.Fatalf("second observation duplicate=%t outside=%t", duplicate, outside)
	}
	if duplicate, outside := audit.Observe(128); duplicate || !outside {
		t.Fatalf("outside observation duplicate=%t outside=%t", duplicate, outside)
	}
	if audit.Unique() != 1 {
		t.Fatalf("unique = %d", audit.Unique())
	}
}

func TestReportJSONContainsReleaseGateEvidence(t *testing.T) {
	encoded, err := json.Marshal(Report{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	required := []string{
		"configured_duration",
		"configured_instrument_count",
		"generated_event_count",
		"accepted_event_count",
		"duplicate_count",
		"rejected_count",
		"late_count",
		"quarantined_count",
		"downstream_delivery_count",
		"unexpected_event_loss_count",
		"unexpected_duplicate_delivery_count",
		"peak_reorder_depth",
		"starting_goroutine_count",
		"peak_goroutine_count",
		"ending_goroutine_count",
		"starting_heap_allocation_bytes",
		"peak_heap_allocation_bytes",
		"ending_heap_allocation_bytes",
		"garbage_collection_cycles",
		"readiness_transition_count",
		"maximum_cancellation_duration_nanoseconds",
		"total_processing_duration_nanoseconds",
		"final_readiness_state",
		"passed",
		"failure_reasons",
	}
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			t.Errorf("report JSON is missing %q", field)
		}
	}
}
