// Package telemetry defines bounded, provider-neutral broker connectivity observations.
package telemetry

import (
	"sync"
	"time"
)

type Operation string
type Outcome string

const (
	OperationAuthentication Operation = "authentication"
	OperationProfile        Operation = "profile"
	OperationInstruments    Operation = "instruments"
	OperationMapping        Operation = "mapping"
	OperationReadiness      Operation = "readiness"
	OperationShutdown       Operation = "shutdown"
)

const (
	OutcomeSuccess     Outcome = "success"
	OutcomeFailure     Outcome = "failure"
	OutcomeExpired     Outcome = "expired"
	OutcomeTimeout     Outcome = "timeout"
	OutcomeRateLimited Outcome = "rate_limited"
	OutcomeMalformed   Outcome = "malformed"
	OutcomeMissing     Outcome = "missing"
	OutcomeStale       Outcome = "stale"
	OutcomeAmbiguous   Outcome = "ambiguous"
	OutcomeStopped     Outcome = "stopped"
)

type Event struct {
	Operation Operation
	Outcome   Outcome
	Duration  time.Duration
	InFlight  int
}

type Recorder interface{ Record(Event) }

type NopRecorder struct{}

func (NopRecorder) Record(Event) {}

type safeRecorder struct{ next Recorder }

func (r safeRecorder) Record(event Event) {
	defer func() { _ = recover() }()
	r.next.Record(event)
}

func Safe(next Recorder) Recorder {
	if next == nil {
		return NopRecorder{}
	}
	return safeRecorder{next: next}
}

func Valid(event Event) bool {
	switch event.Operation {
	case OperationAuthentication, OperationProfile, OperationInstruments, OperationMapping, OperationReadiness, OperationShutdown:
	default:
		return false
	}
	switch event.Outcome {
	case OutcomeSuccess, OutcomeFailure, OutcomeExpired, OutcomeTimeout, OutcomeRateLimited,
		OutcomeMalformed, OutcomeMissing, OutcomeStale, OutcomeAmbiguous, OutcomeStopped:
		return true
	default:
		return false
	}
}

type MemoryRecorder struct {
	mu     sync.Mutex
	counts map[string]uint64
}

func NewMemoryRecorder() *MemoryRecorder { return &MemoryRecorder{counts: map[string]uint64{}} }

func (r *MemoryRecorder) Record(event Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !Valid(event) {
		return
	}
	r.counts[string(event.Operation)+"|"+string(event.Outcome)]++
}

func (r *MemoryRecorder) Counts() map[string]uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]uint64, len(r.counts))
	for key, value := range r.counts {
		result[key] = value
	}
	return result
}
