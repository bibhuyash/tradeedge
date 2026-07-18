package telemetry

import (
	"sync"
	"testing"
	"time"

	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

func TestMemoryRecorderCopiesAndSupportsConcurrentRecording(t *testing.T) {
	t.Parallel()
	recorder := NewMemoryRecorder()
	definition, _ := strategymodel.NewDefinitionID("telemetry-fixture")
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			recorder.Record(Event{
				Definition: definition, Outcome: "COMMITTED_NO_ACTION",
				Duration: time.Millisecond, Publish: time.Microsecond,
				StateBytes: 12, InFlight: 2,
			})
		}()
	}
	wait.Wait()
	snapshot := recorder.Snapshot()
	if snapshot.Counts["COMMITTED_NO_ACTION"] != 20 ||
		snapshot.TotalDuration != 20*time.Millisecond ||
		snapshot.TotalPublish != 20*time.Microsecond ||
		snapshot.LastStateSize != 12 || snapshot.InFlight != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	snapshot.Counts["COMMITTED_NO_ACTION"] = 0
	if recorder.Snapshot().Counts["COMMITTED_NO_ACTION"] != 20 {
		t.Fatal("returned telemetry mutated recorder")
	}
}
