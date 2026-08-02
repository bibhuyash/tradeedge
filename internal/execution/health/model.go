// Package health contains immutable provider-neutral execution health views.
package health

import "time"

type State string

const (
	StateHealthy     State = "HEALTHY"
	StateDegraded    State = "DEGRADED"
	StateBlocked     State = "BLOCKED"
	StateStopped     State = "STOPPED"
	StateUnavailable State = "UNAVAILABLE"
)

type Coordinator struct {
	Closed        bool          `json:"closed"`
	Available     bool          `json:"available"`
	InFlightPlans int           `json:"in_flight_plans"`
	KeyedOrders   int           `json:"keyed_orders"`
	MaximumPlans  int           `json:"maximum_plans"`
	BrokerTimeout time.Duration `json:"broker_timeout_nanoseconds"`
	Cursor        uint64        `json:"event_cursor"`
}

type OMS struct {
	Available        bool `json:"available"`
	Plans            int  `json:"plans"`
	Orders           int  `json:"orders"`
	Publications     int  `json:"publications"`
	Reports          int  `json:"reports"`
	Fills            int  `json:"fills"`
	UnknownOrders    int  `json:"unknown_orders"`
	PlanLimit        int  `json:"plan_limit"`
	OrderLimit       int  `json:"order_limit"`
	PublicationLimit int  `json:"publication_limit"`
	ReportLimit      int  `json:"report_limit"`
	FillLimit        int  `json:"fill_limit"`
}

type PaperBroker struct {
	Available           bool `json:"available"`
	ScenarioIndex       int  `json:"scenario_index"`
	ScenarioCount       int  `json:"scenario_count"`
	ActiveOrders        int  `json:"active_orders"`
	ScheduledEvents     int  `json:"scheduled_events"`
	DeliveredEvents     int  `json:"delivered_events"`
	UnavailableAttempts int  `json:"unavailable_attempts"`
}

type Reconciliation struct {
	Available   bool           `json:"available"`
	Running     bool           `json:"running"`
	Blocked     bool           `json:"blocked"`
	LastAttempt time.Time      `json:"last_attempt"`
	LastSuccess time.Time      `json:"last_success"`
	Repairs     int            `json:"repairs"`
	IssueCounts map[string]int `json:"issue_counts"`
	LastError   string         `json:"last_error,omitempty"`
}

type Snapshot struct {
	State          State          `json:"state"`
	ReasonCodes    []string       `json:"reason_codes"`
	UnknownOrders  int            `json:"unknown_orders"`
	Coordinator    Coordinator    `json:"coordinator"`
	OMS            OMS            `json:"oms"`
	PaperBroker    PaperBroker    `json:"paper_broker"`
	Reconciliation Reconciliation `json:"reconciliation"`
}

func Aggregate(coordinator Coordinator, oms OMS, paper PaperBroker, reconciliation Reconciliation, unknown int) Snapshot {
	result := Snapshot{State: StateHealthy, UnknownOrders: unknown, Coordinator: coordinator, OMS: oms, PaperBroker: paper, Reconciliation: reconciliation}
	if !coordinator.Available || !oms.Available || !paper.Available || !reconciliation.Available {
		result.State = StateUnavailable
		result.ReasonCodes = append(result.ReasonCodes, "EXECUTION_SOURCE_UNAVAILABLE")
	}
	if coordinator.Closed {
		result.State = StateStopped
		result.ReasonCodes = append(result.ReasonCodes, "COORDINATOR_STOPPED")
	}
	if reconciliation.LastAttempt.IsZero() && result.State == StateHealthy {
		result.State = StateDegraded
		result.ReasonCodes = append(result.ReasonCodes, "RECONCILIATION_NOT_RUN")
	}
	if unknown > 0 {
		result.State = StateBlocked
		result.ReasonCodes = append(result.ReasonCodes, "UNKNOWN_ORDERS")
	}
	if reconciliation.Blocked || len(reconciliation.IssueCounts) > 0 || reconciliation.LastError != "" {
		result.State = StateBlocked
		result.ReasonCodes = append(result.ReasonCodes, "RECONCILIATION_BLOCKED")
	}
	return result
}
