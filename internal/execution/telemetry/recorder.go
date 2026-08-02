// Package telemetry defines provider-neutral, non-authoritative execution observations.
package telemetry

import (
	"sync"
	"time"
)

type Operation string
type Outcome string

const (
	OperationPlan           Operation = "plan"
	OperationSubmission     Operation = "submission"
	OperationOrderEvent     Operation = "order_event"
	OperationPublication    Operation = "publication"
	OperationCancellation   Operation = "cancellation"
	OperationReconciliation Operation = "reconciliation"
	OperationMismatch       Operation = "mismatch"
	OperationRepair         Operation = "repair"
	OperationPaperScenario  Operation = "paper_scenario"
	OperationShutdown       Operation = "shutdown"
	OperationHealth         Operation = "health"
)

const (
	OutcomeCreated      Outcome = "created"
	OutcomeCompleted    Outcome = "completed"
	OutcomeFailed       Outcome = "failed"
	OutcomePending      Outcome = "pending"
	OutcomeAccepted     Outcome = "accepted"
	OutcomeAcknowledged Outcome = "acknowledged"
	OutcomePartialFill  Outcome = "partial_fill"
	OutcomeFilled       Outcome = "filled"
	OutcomeCancelled    Outcome = "cancelled"
	OutcomeRejected     Outcome = "rejected"
	OutcomeUnknown      Outcome = "unknown"
	OutcomeDuplicate    Outcome = "duplicate"
	OutcomeInvalid      Outcome = "invalid"
	OutcomeUnavailable  Outcome = "unavailable"
	OutcomeBlocked      Outcome = "blocked"
	OutcomeRepaired     Outcome = "repaired"
	OutcomeClean        Outcome = "clean"
	OutcomeShutdown     Outcome = "shutdown"
)

type Event struct {
	Operation        Operation     `json:"operation"`
	Outcome          Outcome       `json:"outcome"`
	Detail           string        `json:"detail,omitempty"`
	PlanID           string        `json:"plan_id,omitempty"`
	OrderID          string        `json:"order_id,omitempty"`
	Occurred         time.Time     `json:"occurred_at"`
	Duration         time.Duration `json:"duration_nanoseconds,omitempty"`
	InFlight         int           `json:"in_flight"`
	UnknownOrders    int           `json:"unknown_orders,omitempty"`
	HasUnknownOrders bool          `json:"-"`
}

type Recorder interface{ Record(Event) }

type NopRecorder struct{}

func (NopRecorder) Record(Event) {}

type safeRecorder struct{ recorder Recorder }

func (recorder safeRecorder) Record(event Event) {
	defer func() { _ = recover() }()
	recorder.recorder.Record(event)
}

// Safe prevents an observability failure from changing execution truth.
func Safe(recorder Recorder) Recorder {
	if recorder == nil {
		return NopRecorder{}
	}
	return safeRecorder{recorder}
}

func ValidOperation(value Operation) bool {
	switch value {
	case OperationPlan, OperationSubmission, OperationOrderEvent, OperationPublication,
		OperationCancellation, OperationReconciliation, OperationMismatch, OperationRepair,
		OperationPaperScenario, OperationShutdown, OperationHealth:
		return true
	default:
		return false
	}
}

func ValidOutcome(value Outcome) bool {
	switch value {
	case OutcomeCreated, OutcomeCompleted, OutcomeFailed, OutcomePending, OutcomeAccepted,
		OutcomeAcknowledged, OutcomePartialFill, OutcomeFilled, OutcomeCancelled,
		OutcomeRejected, OutcomeUnknown, OutcomeDuplicate, OutcomeInvalid,
		OutcomeUnavailable, OutcomeBlocked, OutcomeRepaired, OutcomeClean, OutcomeShutdown:
		return true
	default:
		return false
	}
}

// BoundedDetail maps only the finite execution vocabulary into metric labels.
func BoundedDetail(value string) string {
	for _, allowed := range []string{
		"", "CREATED", "PLANNED", "SUBMISSION_PENDING", "SUBMITTED", "ACKNOWLEDGED",
		"PARTIAL_FILL", "FILL", "PARTIALLY_FILLED", "FILLED", "CANCEL_PENDING", "CANCELLED", "REJECTED", "EXPIRED", "FAILED", "UNKNOWN",
		"MISSING_BROKER_ORDER", "UNKNOWN_BROKER_ORDER", "TERMS_MISMATCH", "STATUS_MISMATCH", "FILL_MISMATCH",
		"BROKER_FILL_BEHIND_LOCAL", "INCOMPLETE_BROKER_SNAPSHOT",
		"IMMEDIATE_FILL", "PARTIAL_FILL", "DELAYED_FILL", "REJECT", "HOLD", "TIMEOUT", "LOST_RESPONSE",
		"DUPLICATE_EVENTS", "OUT_OF_ORDER", "LATE_FILL",
	} {
		if value == allowed {
			return value
		}
	}
	return "invalid"
}

type Snapshot struct {
	Counts      map[string]uint64 `json:"counts"`
	Recent      []Event           `json:"recent"`
	InFlight    int               `json:"in_flight"`
	MaxInFlight int               `json:"maximum_in_flight"`
}

type MemoryRecorder struct {
	mu       sync.Mutex
	capacity int
	value    Snapshot
}

func NewMemoryRecorder(capacity int) *MemoryRecorder {
	if capacity <= 0 || capacity > 10000 {
		capacity = 1000
	}
	return &MemoryRecorder{capacity: capacity, value: Snapshot{Counts: map[string]uint64{}}}
}

func (recorder *MemoryRecorder) Record(event Event) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	operation, outcome := string(event.Operation), string(event.Outcome)
	if !ValidOperation(event.Operation) {
		operation = "invalid"
	}
	if !ValidOutcome(event.Outcome) {
		outcome = "invalid"
	}
	recorder.value.Counts[operation+"|"+outcome+"|"+BoundedDetail(event.Detail)]++
	recorder.value.InFlight = event.InFlight
	if event.InFlight > recorder.value.MaxInFlight {
		recorder.value.MaxInFlight = event.InFlight
	}
	recorder.value.Recent = append(recorder.value.Recent, event)
	if len(recorder.value.Recent) > recorder.capacity {
		copy(recorder.value.Recent, recorder.value.Recent[len(recorder.value.Recent)-recorder.capacity:])
		recorder.value.Recent = recorder.value.Recent[:recorder.capacity]
	}
}

func (recorder *MemoryRecorder) Snapshot(limit int) Snapshot {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	result := recorder.value
	result.Counts = make(map[string]uint64, len(recorder.value.Counts))
	for key, value := range recorder.value.Counts {
		result.Counts[key] = value
	}
	start := len(recorder.value.Recent) - limit
	if start < 0 {
		start = 0
	}
	values := recorder.value.Recent[start:]
	result.Recent = make([]Event, len(values))
	for index := range values {
		result.Recent[index] = values[len(values)-1-index]
	}
	return result
}

type MultiRecorder []Recorder

func (recorders MultiRecorder) Record(event Event) {
	for _, recorder := range recorders {
		if recorder != nil {
			Safe(recorder).Record(event)
		}
	}
}
