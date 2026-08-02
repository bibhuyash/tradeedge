package telemetry_test

import (
	"sync"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/execution/telemetry"
)

func TestMemoryRecorderIsBoundedAndDefensive(t *testing.T) {
	recorder := telemetry.NewMemoryRecorder(10)
	for index := 0; index < 100; index++ {
		recorder.Record(telemetry.Event{Operation: telemetry.OperationPlan, Outcome: telemetry.OutcomeCompleted, PlanID: "identity", Occurred: time.Unix(int64(index), 0)})
	}
	snapshot := recorder.Snapshot(100)
	if len(snapshot.Recent) != 10 || snapshot.Counts["plan|completed|"] != 100 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	snapshot.Recent[0].PlanID = "mutated"
	if recorder.Snapshot(100).Recent[0].PlanID == "mutated" {
		t.Fatal("snapshot was not defensively copied")
	}
}

func TestMemoryRecorderSupportsConcurrentWritersAndBoundsVocabulary(t *testing.T) {
	recorder := telemetry.NewMemoryRecorder(100)
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				recorder.Record(telemetry.Event{Operation: "unbounded-user-value", Outcome: "unbounded-error", Detail: "order-id"})
			}
		}()
	}
	wait.Wait()
	if recorder.Snapshot(100).Counts["invalid|invalid|invalid"] != 2000 {
		t.Fatal("invalid dimensions were not collapsed")
	}
}

type panicRecorder struct{}

func (panicRecorder) Record(telemetry.Event) { panic("telemetry unavailable") }

func TestSafeRecorderContainsTelemetryPanic(t *testing.T) {
	telemetry.Safe(panicRecorder{}).Record(telemetry.Event{Operation: telemetry.OperationPlan, Outcome: telemetry.OutcomeCompleted})
}
