package telemetry

import (
	"sync"
	"time"

	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
)

// Event contains only bounded dimensions. Authoritative identities stay out of metrics.
type Event struct {
	Outcome  string
	RuleID   riskmodel.RiskRuleID
	Status   riskmodel.RuleResultStatus
	Effect   riskmodel.RuleEffect
	Severity riskmodel.RuleSeverity
	Duration time.Duration
	Publish  time.Duration
	InFlight int
}

type Recorder interface{ Record(Event) }

type NopRecorder struct{}

func (NopRecorder) Record(Event) {}

type Snapshot struct {
	Counts      map[string]uint64
	RuleCounts  map[string]uint64
	Duration    time.Duration
	Publication time.Duration
	InFlight    int
	Maximum     int
}

type MemoryRecorder struct {
	mu       sync.Mutex
	snapshot Snapshot
}

func NewMemoryRecorder() *MemoryRecorder {
	return &MemoryRecorder{snapshot: Snapshot{Counts: map[string]uint64{}, RuleCounts: map[string]uint64{}}}
}

func (recorder *MemoryRecorder) Record(event Event) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if event.Outcome != "" {
		recorder.snapshot.Counts[event.Outcome]++
	}
	if event.RuleID != "" {
		key := string(event.RuleID) + "|" + string(event.Status) + "|" + string(event.Effect) + "|" + string(event.Severity)
		recorder.snapshot.RuleCounts[key]++
	}
	recorder.snapshot.Duration += event.Duration
	recorder.snapshot.Publication += event.Publish
	recorder.snapshot.InFlight = event.InFlight
	if event.InFlight > recorder.snapshot.Maximum {
		recorder.snapshot.Maximum = event.InFlight
	}
}

func (recorder *MemoryRecorder) Snapshot() Snapshot {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	result := recorder.snapshot
	result.Counts = clone(result.Counts)
	result.RuleCounts = clone(result.RuleCounts)
	return result
}

func clone(source map[string]uint64) map[string]uint64 {
	result := make(map[string]uint64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
