package valuation

import "time"

type Operation string
type Outcome string

const (
	OperationValuation   Operation = "valuation"
	OperationPublication Operation = "publication"
	OperationReplay      Operation = "replay"
	OperationShutdown    Operation = "shutdown"
)
const (
	OutcomeCompleted Outcome = "completed"
	OutcomeFailed    Outcome = "failed"
	OutcomeConflict  Outcome = "conflict"
	OutcomeDuplicate Outcome = "duplicate"
	OutcomeCancelled Outcome = "cancelled"
)

type Event struct {
	Operation Operation
	Outcome   Outcome
	Status    Status
	Reason    Reason
	Duration  time.Duration
	InFlight  int
}
type Recorder interface{ Record(Event) }
type nopRecorder struct{}

func (nopRecorder) Record(Event) {}

type safeRecorder struct{ value Recorder }

func (s safeRecorder) Record(e Event) { defer func() { _ = recover() }(); s.value.Record(e) }
func SafeRecorder(value Recorder) Recorder {
	if value == nil {
		return nopRecorder{}
	}
	return safeRecorder{value}
}
func BoundedLabel(value string) string {
	for _, allowed := range []string{"valuation", "publication", "replay", "shutdown", "completed", "failed", "conflict", "duplicate", "cancelled", "COMPLETE", "PARTIAL", "STALE", "UNAVAILABLE", "NONE", "MISSING_MARK", "STALE_MARK", "INVALID_PRICE", "CLOCK_SKEW", "MARKET_UNAVAILABLE", "MARKET_REVISION_CONFLICT", "CURRENCY_MISMATCH", "ARITHMETIC_OVERFLOW", "AUTHORITATIVE_CAPITAL_UNAVAILABLE"} {
		if value == allowed {
			return value
		}
	}
	return "invalid"
}
