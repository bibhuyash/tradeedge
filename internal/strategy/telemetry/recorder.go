package telemetry

import (
	"sync"
	"time"

	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

type Event struct {
	Definition strategymodel.DefinitionID
	Outcome    string
	Duration   time.Duration
	Publish    time.Duration
	StateBytes int
	InFlight   int
}

type Recorder interface {
	Record(Event)
}

type NopRecorder struct{}

func (NopRecorder) Record(Event) {}

type Snapshot struct {
	Counts        map[string]uint64
	TotalDuration time.Duration
	TotalPublish  time.Duration
	LastStateSize int
	InFlight      int
}

type MemoryRecorder struct {
	mu       sync.Mutex
	snapshot Snapshot
}

func NewMemoryRecorder() *MemoryRecorder {
	return &MemoryRecorder{snapshot: Snapshot{Counts: make(map[string]uint64)}}
}

func (recorder *MemoryRecorder) Record(event Event) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.snapshot.Counts[event.Outcome]++
	recorder.snapshot.TotalDuration += event.Duration
	recorder.snapshot.TotalPublish += event.Publish
	if event.StateBytes > 0 {
		recorder.snapshot.LastStateSize = event.StateBytes
	}
	recorder.snapshot.InFlight = event.InFlight
}

func (recorder *MemoryRecorder) Snapshot() Snapshot {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	result := recorder.snapshot
	result.Counts = make(map[string]uint64, len(recorder.snapshot.Counts))
	for key, value := range recorder.snapshot.Counts {
		result.Counts[key] = value
	}
	return result
}
