package model

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

var (
	ErrInvalidOrder        = errors.New("invalid order")
	ErrInvalidTransition   = errors.New("invalid order state transition")
	ErrOverfill            = errors.New("fill exceeds order quantity")
	ErrNonMonotonicFill    = errors.New("cumulative fill is not monotonic")
	ErrReportOrderMismatch = errors.New("execution report does not belong to order")
)

type OrderState string

const (
	OrderCreated           OrderState = "CREATED"
	OrderPlanned           OrderState = "PLANNED"
	OrderSubmissionPending OrderState = "SUBMISSION_PENDING"
	OrderSubmitted         OrderState = "SUBMITTED"
	OrderAcknowledged      OrderState = "ACKNOWLEDGED"
	OrderPartiallyFilled   OrderState = "PARTIALLY_FILLED"
	OrderFilled            OrderState = "FILLED"
	OrderCancelPending     OrderState = "CANCEL_PENDING"
	OrderCancelled         OrderState = "CANCELLED"
	OrderRejected          OrderState = "REJECTED"
	OrderExpired           OrderState = "EXPIRED"
	OrderFailed            OrderState = "FAILED"
	OrderUnknown           OrderState = "UNKNOWN"
)

func (value OrderState) Validate() error {
	switch value {
	case OrderCreated, OrderPlanned, OrderSubmissionPending, OrderSubmitted, OrderAcknowledged,
		OrderPartiallyFilled, OrderFilled, OrderCancelPending, OrderCancelled, OrderRejected,
		OrderExpired, OrderFailed, OrderUnknown:
		return nil
	default:
		return ErrInvalidOrder
	}
}

func (value OrderState) Terminal() bool {
	switch value {
	case OrderFilled, OrderCancelled, OrderRejected, OrderExpired, OrderFailed:
		return true
	default:
		return false
	}
}

type OrderSpec struct {
	SchemaVersion  string
	PlanID         OrderPlanID
	Leg            OrderLeg
	State          OrderState
	Revision       OrderRevision
	FilledQuantity int64
	BrokerOrderID  string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Order struct {
	id       OrderID
	clientID ClientOrderID
	spec     OrderSpec
	raw      []byte
}

func NewOrder(plan OrderPlan, legID OrderLegID, createdAt time.Time) (Order, error) {
	if plan.IsZero() || legID.IsZero() || createdAt.IsZero() {
		return Order{}, ErrInvalidOrder
	}
	var selected OrderLeg
	for _, leg := range plan.Legs() {
		if leg.ID == legID {
			selected = leg
			break
		}
	}
	if selected.ID.IsZero() {
		return Order{}, ErrInvalidOrder
	}
	id := OrderID(derive("order-id/v1", plan.ID().String(), legID.String()))
	clientID := ClientOrderID(derive("client-order-id/v1", id.String(), "submission-contract/v1"))
	return newOrderValue(id, clientID, OrderSpec{"order/v1", plan.ID(), selected, OrderCreated, 1, 0, "", createdAt.UTC(), createdAt.UTC()})
}

func newOrderValue(id OrderID, clientID ClientOrderID, spec OrderSpec) (Order, error) {
	if id.IsZero() || clientID.IsZero() || strings.TrimSpace(spec.SchemaVersion) == "" || spec.PlanID.IsZero() ||
		spec.Leg.ID.IsZero() || spec.State.Validate() != nil || spec.Revision.Validate() != nil ||
		spec.FilledQuantity < 0 || spec.FilledQuantity > spec.Leg.Quantity.Int64() || spec.CreatedAt.IsZero() ||
		spec.UpdatedAt.Before(spec.CreatedAt) || (spec.State == OrderFilled && spec.FilledQuantity != spec.Leg.Quantity.Int64()) ||
		(spec.State == OrderPartiallyFilled && (spec.FilledQuantity == 0 || spec.FilledQuantity == spec.Leg.Quantity.Int64())) {
		return Order{}, ErrInvalidOrder
	}
	spec.BrokerOrderID = strings.TrimSpace(spec.BrokerOrderID)
	spec.CreatedAt, spec.UpdatedAt = spec.CreatedAt.UTC(), spec.UpdatedAt.UTC()
	raw, err := canonicalOrder(id, clientID, spec)
	if err != nil {
		return Order{}, ErrInvalidOrder
	}
	return Order{id: id, clientID: clientID, spec: spec, raw: raw}, nil
}

func canonicalOrder(id OrderID, clientID ClientOrderID, spec OrderSpec) ([]byte, error) {
	dependencies := make([]string, len(spec.Leg.DependsOn))
	for i, dep := range spec.Leg.DependsOn {
		dependencies[i] = dep.String()
	}
	return json.Marshal(struct {
		SchemaVersion, ID, ClientOrderID, PlanID, LegID, InstrumentID, Side string
		Quantity, PriceMinor                                                int64
		Currency                                                            string
		Protective                                                          bool
		DependsOn                                                           []string
		State                                                               string
		Revision                                                            uint64
		FilledQuantity                                                      int64
		BrokerOrderID, CreatedAt, UpdatedAt                                 string
	}{spec.SchemaVersion, id.String(), clientID.String(), spec.PlanID.String(), spec.Leg.ID.String(),
		spec.Leg.InstrumentID.String(), string(spec.Leg.Side), spec.Leg.Quantity.Int64(), spec.Leg.LimitPrice.MinorUnits(),
		spec.Leg.LimitPrice.Currency().String(), spec.Leg.Protective, dependencies, string(spec.State), uint64(spec.Revision),
		spec.FilledQuantity, spec.BrokerOrderID, spec.CreatedAt.Format(time.RFC3339Nano), spec.UpdatedAt.Format(time.RFC3339Nano)})
}

func (value Order) ID() OrderID                  { return value.id }
func (value Order) ClientOrderID() ClientOrderID { return value.clientID }
func (value Order) Spec() OrderSpec {
	result := value.spec
	result.Leg.DependsOn = append([]OrderLegID(nil), result.Leg.DependsOn...)
	return result
}
func (value Order) CanonicalJSON() []byte { return append([]byte(nil), value.raw...) }
func (value Order) IsZero() bool          { return value.id.IsZero() }

type ReportType string

const (
	ReportPlanned           ReportType = "PLANNED"
	ReportSubmissionPending ReportType = "SUBMISSION_PENDING"
	ReportSubmitted         ReportType = "SUBMITTED"
	ReportAcknowledged      ReportType = "ACKNOWLEDGED"
	ReportPartialFill       ReportType = "PARTIAL_FILL"
	ReportFill              ReportType = "FILL"
	ReportCancelPending     ReportType = "CANCEL_PENDING"
	ReportCancelled         ReportType = "CANCELLED"
	ReportRejected          ReportType = "REJECTED"
	ReportExpired           ReportType = "EXPIRED"
	ReportFailed            ReportType = "FAILED"
	ReportUnknown           ReportType = "UNKNOWN"
)

type TransitionReason string

const (
	ReasonPlanned                  TransitionReason = "PLAN_ACCEPTED"
	ReasonSubmissionStarted        TransitionReason = "SUBMISSION_STARTED"
	ReasonBrokerAccepted           TransitionReason = "BROKER_ACCEPTED"
	ReasonBrokerAcknowledged       TransitionReason = "BROKER_ACKNOWLEDGED"
	ReasonBrokerFill               TransitionReason = "BROKER_FILL"
	ReasonCancellationRequested    TransitionReason = "CANCELLATION_REQUESTED"
	ReasonBrokerCancelled          TransitionReason = "BROKER_CANCELLED"
	ReasonBrokerRejected           TransitionReason = "BROKER_REJECTED"
	ReasonAuthorityExpired         TransitionReason = "AUTHORITY_EXPIRED"
	ReasonInternalFailure          TransitionReason = "INTERNAL_FAILURE"
	ReasonSubmissionOutcomeUnknown TransitionReason = "SUBMISSION_OUTCOME_UNKNOWN"
)

type ExecutionReportSpec struct {
	SchemaVersion, Source, SourceEventID string
	OrderID                              OrderID
	ClientOrderID                        ClientOrderID
	BrokerOrderID                        string
	Type                                 ReportType
	Reason                               TransitionReason
	CumulativeFilled                     int64
	OccurredAt, ReceivedAt               time.Time
}
type ExecutionReport struct {
	id   ExecutionReportID
	spec ExecutionReportSpec
	raw  []byte
}

func NewExecutionReport(spec ExecutionReportSpec) (ExecutionReport, error) {
	spec.SchemaVersion, spec.Source, spec.SourceEventID = strings.TrimSpace(spec.SchemaVersion), strings.TrimSpace(spec.Source), strings.TrimSpace(spec.SourceEventID)
	if spec.SchemaVersion == "" || spec.Source == "" || spec.SourceEventID == "" || spec.OrderID.IsZero() ||
		spec.ClientOrderID.IsZero() || spec.CumulativeFilled < 0 || spec.OccurredAt.IsZero() || spec.ReceivedAt.IsZero() {
		return ExecutionReport{}, ErrInvalidOrder
	}
	if _, ok := reportTarget(spec.Type); !ok || !validTransitionReason(spec.Reason) || expectedReason(spec.Type) != spec.Reason {
		return ExecutionReport{}, ErrInvalidOrder
	}
	id, err := NewExecutionReportID(spec.Source, spec.SourceEventID)
	if err != nil {
		return ExecutionReport{}, err
	}
	spec.BrokerOrderID = strings.TrimSpace(spec.BrokerOrderID)
	spec.OccurredAt, spec.ReceivedAt = spec.OccurredAt.UTC(), spec.ReceivedAt.UTC()
	raw, err := json.Marshal(struct {
		SchemaVersion, ID, Source, SourceEventID, OrderID, ClientOrderID, BrokerOrderID, Type, Reason string
		CumulativeFilled                                                                              int64
		OccurredAt, ReceivedAt                                                                        string
	}{
		spec.SchemaVersion, id.String(), spec.Source, spec.SourceEventID, spec.OrderID.String(), spec.ClientOrderID.String(), spec.BrokerOrderID,
		string(spec.Type), string(spec.Reason), spec.CumulativeFilled, spec.OccurredAt.Format(time.RFC3339Nano), spec.ReceivedAt.Format(time.RFC3339Nano)})
	if err != nil {
		return ExecutionReport{}, ErrInvalidOrder
	}
	return ExecutionReport{id, spec, raw}, nil
}

func validTransitionReason(value TransitionReason) bool {
	switch value {
	case ReasonPlanned, ReasonSubmissionStarted, ReasonBrokerAccepted, ReasonBrokerAcknowledged,
		ReasonBrokerFill, ReasonCancellationRequested, ReasonBrokerCancelled, ReasonBrokerRejected,
		ReasonAuthorityExpired, ReasonInternalFailure, ReasonSubmissionOutcomeUnknown:
		return true
	default:
		return false
	}
}

func expectedReason(value ReportType) TransitionReason {
	switch value {
	case ReportPlanned:
		return ReasonPlanned
	case ReportSubmissionPending:
		return ReasonSubmissionStarted
	case ReportSubmitted:
		return ReasonBrokerAccepted
	case ReportAcknowledged:
		return ReasonBrokerAcknowledged
	case ReportPartialFill, ReportFill:
		return ReasonBrokerFill
	case ReportCancelPending:
		return ReasonCancellationRequested
	case ReportCancelled:
		return ReasonBrokerCancelled
	case ReportRejected:
		return ReasonBrokerRejected
	case ReportExpired:
		return ReasonAuthorityExpired
	case ReportFailed:
		return ReasonInternalFailure
	case ReportUnknown:
		return ReasonSubmissionOutcomeUnknown
	default:
		return ""
	}
}
func (value ExecutionReport) ID() ExecutionReportID     { return value.id }
func (value ExecutionReport) Spec() ExecutionReportSpec { return value.spec }
func (value ExecutionReport) CanonicalJSON() []byte     { return append([]byte(nil), value.raw...) }

type FillSpec struct {
	SchemaVersion, Source, SourceExecutionID string
	OrderID                                  OrderID
	ReportID                                 ExecutionReportID
	Quantity                                 domain.Quantity
	Price                                    domain.Price
	OccurredAt                               time.Time
}
type Fill struct {
	id   FillID
	spec FillSpec
	raw  []byte
}

func NewFill(spec FillSpec) (Fill, error) {
	spec.SchemaVersion, spec.Source, spec.SourceExecutionID = strings.TrimSpace(spec.SchemaVersion), strings.TrimSpace(spec.Source), strings.TrimSpace(spec.SourceExecutionID)
	if spec.SchemaVersion == "" || spec.Source == "" || spec.SourceExecutionID == "" || spec.OrderID.IsZero() || spec.ReportID.IsZero() ||
		!spec.Quantity.IsValid() || spec.Price.IsZeroValue() || spec.OccurredAt.IsZero() {
		return Fill{}, ErrInvalidOrder
	}
	id, err := NewFillID(spec.Source, spec.SourceExecutionID)
	if err != nil {
		return Fill{}, err
	}
	spec.OccurredAt = spec.OccurredAt.UTC()
	raw, err := json.Marshal(struct {
		SchemaVersion, ID, Source, SourceExecutionID, OrderID, ReportID string
		Quantity, PriceMinor                                            int64
		Currency, OccurredAt                                            string
	}{
		spec.SchemaVersion, id.String(), spec.Source, spec.SourceExecutionID, spec.OrderID.String(), spec.ReportID.String(), spec.Quantity.Int64(), spec.Price.MinorUnits(), spec.Price.Currency().String(), spec.OccurredAt.Format(time.RFC3339Nano)})
	if err != nil {
		return Fill{}, ErrInvalidOrder
	}
	return Fill{id, spec, raw}, nil
}
func (value Fill) ID() FillID            { return value.id }
func (value Fill) Spec() FillSpec        { return value.spec }
func (value Fill) CanonicalJSON() []byte { return append([]byte(nil), value.raw...) }

func ApplyExecutionReport(current Order, report ExecutionReport, fill *Fill) (Order, error) {
	if current.IsZero() || report.ID().IsZero() || report.spec.OrderID != current.id || report.spec.ClientOrderID != current.clientID {
		return Order{}, ErrReportOrderMismatch
	}
	if report.spec.CumulativeFilled < current.spec.FilledQuantity {
		return Order{}, ErrNonMonotonicFill
	}
	if report.spec.CumulativeFilled > current.spec.Leg.Quantity.Int64() {
		return Order{}, ErrOverfill
	}
	target, ok := reportTarget(report.spec.Type)
	if !ok {
		return Order{}, ErrInvalidTransition
	}
	fillQuantity := int64(0)
	if fill != nil {
		if fill.spec.OrderID != current.id || fill.spec.ReportID != report.id || fill.spec.Price.Currency() != current.spec.Leg.LimitPrice.Currency() {
			return Order{}, ErrReportOrderMismatch
		}
		fillQuantity = fill.spec.Quantity.Int64()
	}
	if target == OrderPartiallyFilled || target == OrderFilled {
		if fill == nil || fillQuantity > math.MaxInt64-current.spec.FilledQuantity ||
			current.spec.FilledQuantity+fillQuantity != report.spec.CumulativeFilled {
			return Order{}, ErrNonMonotonicFill
		}
		if target == OrderPartiallyFilled && report.spec.CumulativeFilled == current.spec.Leg.Quantity.Int64() {
			return Order{}, ErrInvalidTransition
		}
		if target == OrderFilled && report.spec.CumulativeFilled != current.spec.Leg.Quantity.Int64() {
			return Order{}, ErrInvalidTransition
		}
	} else if fill != nil || report.spec.CumulativeFilled != current.spec.FilledQuantity {
		return Order{}, ErrNonMonotonicFill
	}
	if !legalTransition(current.spec.State, target) {
		if staleTransition(current.spec.State, target) {
			target = current.spec.State
		} else {
			return Order{}, ErrInvalidTransition
		}
	}
	next := current.spec
	next.State = target
	next.Revision++
	next.FilledQuantity = report.spec.CumulativeFilled
	if report.spec.ReceivedAt.After(next.UpdatedAt) {
		next.UpdatedAt = report.spec.ReceivedAt
	}
	if report.spec.BrokerOrderID != "" {
		if next.BrokerOrderID != "" && next.BrokerOrderID != report.spec.BrokerOrderID {
			return Order{}, ErrReportOrderMismatch
		}
		next.BrokerOrderID = report.spec.BrokerOrderID
	}
	return newOrderValue(current.id, current.clientID, next)
}

func reportTarget(value ReportType) (OrderState, bool) {
	switch value {
	case ReportPlanned:
		return OrderPlanned, true
	case ReportSubmissionPending:
		return OrderSubmissionPending, true
	case ReportSubmitted:
		return OrderSubmitted, true
	case ReportAcknowledged:
		return OrderAcknowledged, true
	case ReportPartialFill:
		return OrderPartiallyFilled, true
	case ReportFill:
		return OrderFilled, true
	case ReportCancelPending:
		return OrderCancelPending, true
	case ReportCancelled:
		return OrderCancelled, true
	case ReportRejected:
		return OrderRejected, true
	case ReportExpired:
		return OrderExpired, true
	case ReportFailed:
		return OrderFailed, true
	case ReportUnknown:
		return OrderUnknown, true
	default:
		return "", false
	}
}

func legalTransition(from, to OrderState) bool {
	allowed := map[OrderState]map[OrderState]bool{
		OrderCreated:           {OrderPlanned: true, OrderExpired: true, OrderFailed: true},
		OrderPlanned:           {OrderSubmissionPending: true, OrderExpired: true, OrderCancelled: true, OrderFailed: true},
		OrderSubmissionPending: {OrderSubmitted: true, OrderAcknowledged: true, OrderPartiallyFilled: true, OrderFilled: true, OrderRejected: true, OrderUnknown: true},
		OrderSubmitted:         {OrderAcknowledged: true, OrderPartiallyFilled: true, OrderFilled: true, OrderCancelPending: true, OrderRejected: true, OrderUnknown: true},
		OrderAcknowledged:      {OrderPartiallyFilled: true, OrderFilled: true, OrderCancelPending: true, OrderRejected: true, OrderUnknown: true},
		OrderPartiallyFilled:   {OrderPartiallyFilled: true, OrderFilled: true, OrderCancelPending: true, OrderRejected: true, OrderUnknown: true},
		OrderCancelPending:     {OrderCancelled: true, OrderPartiallyFilled: true, OrderFilled: true, OrderRejected: true, OrderUnknown: true},
		OrderCancelled:         {OrderPartiallyFilled: true, OrderFilled: true},
		OrderUnknown:           {OrderSubmitted: true, OrderAcknowledged: true, OrderPartiallyFilled: true, OrderFilled: true, OrderCancelPending: true, OrderCancelled: true, OrderRejected: true},
	}
	return allowed[from][to]
}

func staleTransition(from, to OrderState) bool {
	if to == OrderPartiallyFilled || to == OrderFilled {
		return false
	}
	rank := map[OrderState]int{OrderCreated: 1, OrderPlanned: 2, OrderSubmissionPending: 3, OrderSubmitted: 4, OrderAcknowledged: 5, OrderPartiallyFilled: 6, OrderCancelPending: 7, OrderCancelled: 8, OrderRejected: 8, OrderExpired: 8, OrderFailed: 8, OrderFilled: 9}
	return rank[to] > 0 && rank[to] <= rank[from]
}
