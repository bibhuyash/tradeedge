package paper

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
)

type ScriptClock interface{ Now() time.Time }

type Behavior string

const (
	BehaviorImmediateFill   Behavior = "IMMEDIATE_FILL"
	BehaviorPartialFill     Behavior = "PARTIAL_FILL"
	BehaviorDelayedFill     Behavior = "DELAYED_FILL"
	BehaviorReject          Behavior = "REJECT"
	BehaviorHold            Behavior = "HOLD"
	BehaviorTimeout         Behavior = "TIMEOUT"
	BehaviorLostResponse    Behavior = "LOST_RESPONSE"
	BehaviorDuplicateEvents Behavior = "DUPLICATE_EVENTS"
	BehaviorOutOfOrder      Behavior = "OUT_OF_ORDER"
	BehaviorLateFill        Behavior = "LATE_FILL"
)

type Scenario struct {
	Behavior            Behavior
	PartialQuantity     int64
	Delay               time.Duration
	UnavailableAttempts int
}

type scheduledEvent struct {
	due   time.Time
	event executionbroker.Event
}
type scriptedOrder struct {
	request   executionbroker.Submission
	canonical string
	snapshot  executionbroker.OrderSnapshot
	lateFill  bool
}

type ScriptedCheckpoint struct {
	ScenarioIndex       int
	UnavailableAttempts int
	Orders              map[executionmodel.ClientOrderID]scriptedOrder
	Scheduled           []scheduledEvent
	Delivered           []executionbroker.Event
}

type ScriptedBroker struct {
	mu                  sync.Mutex
	clock               ScriptClock
	scenarios           []Scenario
	scenarioIndex       int
	unavailableAttempts int
	orders              map[executionmodel.ClientOrderID]scriptedOrder
	scheduled           []scheduledEvent
	delivered           []executionbroker.Event
}

func NewScripted(clock ScriptClock, scenarios []Scenario) (*ScriptedBroker, error) {
	if clock == nil || len(scenarios) == 0 {
		return nil, errors.New("paper script clock and scenarios are required")
	}
	values := append([]Scenario(nil), scenarios...)
	for _, scenario := range values {
		if !validBehavior(scenario.Behavior) || scenario.PartialQuantity < 0 || scenario.Delay < 0 || scenario.UnavailableAttempts < 0 {
			return nil, errors.New("invalid paper scenario")
		}
	}
	return &ScriptedBroker{clock: clock, scenarios: values, orders: map[executionmodel.ClientOrderID]scriptedOrder{}}, nil
}

func validBehavior(value Behavior) bool {
	switch value {
	case BehaviorImmediateFill, BehaviorPartialFill, BehaviorDelayedFill, BehaviorReject, BehaviorHold, BehaviorTimeout, BehaviorLostResponse, BehaviorDuplicateEvents, BehaviorOutOfOrder, BehaviorLateFill:
		return true
	default:
		return false
	}
}

func submissionCanonical(value executionbroker.Submission) string {
	return fmt.Sprintf("%s|%s|%s|%s|%d|%d|%s", value.OrderID.String(), value.ClientOrderID.String(), value.InstrumentID.String(), value.Side, value.Quantity.Int64(), value.LimitPrice.MinorUnits(), value.LimitPrice.Currency())
}

func (broker *ScriptedBroker) Submit(ctx context.Context, request executionbroker.Submission) (executionbroker.SubmissionResult, error) {
	if err := ctx.Err(); err != nil {
		return executionbroker.SubmissionResult{}, err
	}
	if request.AttemptID.IsZero() || request.OrderID.IsZero() || request.ClientOrderID.IsZero() || request.InstrumentID.IsZero() || !request.Quantity.IsValid() || request.LimitPrice.IsZeroValue() || request.SubmittedAt.IsZero() {
		return executionbroker.SubmissionResult{}, executionbroker.ErrInvalidRequest
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	canonical := submissionCanonical(request)
	if existing, ok := broker.orders[request.ClientOrderID]; ok {
		if existing.canonical != canonical {
			return executionbroker.SubmissionResult{}, executionbroker.ErrIdentityConflict
		}
		status := executionbroker.SubmissionAccepted
		if existing.snapshot.State == executionmodel.OrderRejected {
			status = executionbroker.SubmissionRejected
		}
		return executionbroker.SubmissionResult{Status: status, BrokerOrderID: existing.snapshot.BrokerOrderID}, nil
	}
	if broker.scenarioIndex >= len(broker.scenarios) {
		return executionbroker.SubmissionResult{}, executionbroker.ErrUnavailable
	}
	scenario := broker.scenarios[broker.scenarioIndex]
	if broker.unavailableAttempts < scenario.UnavailableAttempts {
		broker.unavailableAttempts++
		return executionbroker.SubmissionResult{}, executionbroker.ErrUnavailable
	}
	if scenario.Behavior == BehaviorPartialFill && request.Quantity.Int64() < 2 {
		return executionbroker.SubmissionResult{}, executionbroker.ErrInvalidRequest
	}
	broker.scenarioIndex++
	broker.unavailableAttempts = 0
	now := broker.clock.Now().UTC()
	brokerID := "paper-" + request.ClientOrderID.String()[:16]
	state := executionmodel.OrderSubmitted
	if scenario.Behavior == BehaviorReject {
		state = executionmodel.OrderRejected
	}
	order := scriptedOrder{request: request, canonical: canonical, snapshot: executionbroker.OrderSnapshot{ClientOrderID: request.ClientOrderID, BrokerOrderID: brokerID, InstrumentID: request.InstrumentID, Side: request.Side, Quantity: request.Quantity, LimitPrice: request.LimitPrice, State: state, UpdatedAt: now}, lateFill: scenario.Behavior == BehaviorLateFill}
	broker.orders[request.ClientOrderID] = order
	if scenario.Behavior == BehaviorReject {
		return executionbroker.SubmissionResult{Status: executionbroker.SubmissionRejected, BrokerOrderID: brokerID}, nil
	}
	broker.schedule(request.ClientOrderID, brokerID, executionmodel.ReportAcknowledged, executionmodel.ReasonBrokerAcknowledged, 0, 0, request.LimitPrice, now, "ack")
	delay := scenario.Delay
	if delay == 0 {
		delay = time.Second
	}
	switch scenario.Behavior {
	case BehaviorImmediateFill:
		broker.schedule(request.ClientOrderID, brokerID, executionmodel.ReportFill, executionmodel.ReasonBrokerFill, request.Quantity.Int64(), request.Quantity.Int64(), request.LimitPrice, now, "fill")
	case BehaviorPartialFill:
		partial := scenario.PartialQuantity
		if partial <= 0 || partial >= request.Quantity.Int64() {
			partial = request.Quantity.Int64() / 2
		}
		broker.schedule(request.ClientOrderID, brokerID, executionmodel.ReportPartialFill, executionmodel.ReasonBrokerFill, partial, partial, request.LimitPrice, now, "partial")
		broker.schedule(request.ClientOrderID, brokerID, executionmodel.ReportFill, executionmodel.ReasonBrokerFill, request.Quantity.Int64(), request.Quantity.Int64()-partial, request.LimitPrice, now.Add(delay), "fill")
	case BehaviorDelayedFill:
		broker.schedule(request.ClientOrderID, brokerID, executionmodel.ReportFill, executionmodel.ReasonBrokerFill, request.Quantity.Int64(), request.Quantity.Int64(), request.LimitPrice, now.Add(delay), "fill")
	case BehaviorDuplicateEvents:
		broker.schedule(request.ClientOrderID, brokerID, executionmodel.ReportFill, executionmodel.ReasonBrokerFill, request.Quantity.Int64(), request.Quantity.Int64(), request.LimitPrice, now, "fill")
		broker.schedule(request.ClientOrderID, brokerID, executionmodel.ReportFill, executionmodel.ReasonBrokerFill, request.Quantity.Int64(), request.Quantity.Int64(), request.LimitPrice, now, "fill")
	case BehaviorOutOfOrder:
		broker.schedule(request.ClientOrderID, brokerID, executionmodel.ReportFill, executionmodel.ReasonBrokerFill, request.Quantity.Int64(), request.Quantity.Int64(), request.LimitPrice, now, "fill-early")
		broker.schedule(request.ClientOrderID, brokerID, executionmodel.ReportAcknowledged, executionmodel.ReasonBrokerAcknowledged, request.Quantity.Int64(), 0, request.LimitPrice, now, "ack-late")
	}
	result := executionbroker.SubmissionResult{Status: executionbroker.SubmissionAccepted, BrokerOrderID: brokerID}
	if scenario.Behavior == BehaviorTimeout || scenario.Behavior == BehaviorLostResponse {
		return executionbroker.SubmissionResult{}, executionbroker.ErrOutcomeUnknown
	}
	return result, nil
}

func (broker *ScriptedBroker) schedule(client executionmodel.ClientOrderID, brokerID string, kind executionmodel.ReportType, reason executionmodel.TransitionReason, cumulative, fillQuantity int64, price domain.Price, due time.Time, suffix string) {
	eventID := client.String() + "-" + suffix
	fillID := ""
	if fillQuantity > 0 {
		fillID = client.String() + "-fill-" + suffix
	}
	broker.scheduled = append(broker.scheduled, scheduledEvent{due.UTC(), executionbroker.Event{EventID: eventID, FillExecutionID: fillID, ClientOrderID: client, BrokerOrderID: brokerID, Type: kind, Reason: reason, CumulativeFilled: cumulative, FillQuantity: fillQuantity, FillPrice: price, OccurredAt: due.UTC()}})
}

func (broker *ScriptedBroker) LookupByClientOrderID(ctx context.Context, id executionmodel.ClientOrderID) (executionbroker.OrderSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return executionbroker.OrderSnapshot{}, err
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	value, ok := broker.orders[id]
	if !ok {
		return executionbroker.OrderSnapshot{}, executionbroker.ErrNotFound
	}
	return copySnapshot(value.snapshot), nil
}

func (broker *ScriptedBroker) Cancel(ctx context.Context, request executionbroker.CancellationRequest) (executionbroker.CancellationResult, error) {
	if err := ctx.Err(); err != nil {
		return executionbroker.CancellationResult{}, err
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	value, ok := broker.orders[request.ClientOrderID]
	if !ok {
		return executionbroker.CancellationResult{}, executionbroker.ErrNotFound
	}
	if value.snapshot.BrokerOrderID != request.BrokerOrderID {
		return executionbroker.CancellationResult{}, executionbroker.ErrIdentityConflict
	}
	now := broker.clock.Now().UTC()
	broker.schedule(request.ClientOrderID, request.BrokerOrderID, executionmodel.ReportCancelled, executionmodel.ReasonBrokerCancelled, value.snapshot.CumulativeFilled, 0, value.request.LimitPrice, now, "cancelled")
	if value.lateFill {
		broker.schedule(request.ClientOrderID, request.BrokerOrderID, executionmodel.ReportFill, executionmodel.ReasonBrokerFill, value.request.Quantity.Int64(), value.request.Quantity.Int64()-value.snapshot.CumulativeFilled, value.request.LimitPrice, now.Add(time.Second), "late-fill")
	}
	return executionbroker.CancellationResult{Accepted: true}, nil
}

func (broker *ScriptedBroker) EventsAfter(ctx context.Context, cursor executionbroker.EventCursor, limit int) (executionbroker.EventBatch, error) {
	if err := ctx.Err(); err != nil {
		return executionbroker.EventBatch{}, err
	}
	if limit <= 0 || limit > 1000 {
		return executionbroker.EventBatch{}, executionbroker.ErrInvalidRequest
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	now := broker.clock.Now().UTC()
	remaining := broker.scheduled[:0]
	for _, item := range broker.scheduled {
		if !item.due.After(now) {
			broker.delivered = append(broker.delivered, item.event)
			broker.apply(item.event)
		} else {
			remaining = append(remaining, item)
		}
	}
	broker.scheduled = remaining
	if uint64(cursor) > uint64(len(broker.delivered)) {
		return executionbroker.EventBatch{}, executionbroker.ErrIdentityConflict
	}
	end := int(cursor) + limit
	if end > len(broker.delivered) {
		end = len(broker.delivered)
	}
	events := append([]executionbroker.Event(nil), broker.delivered[int(cursor):end]...)
	return executionbroker.EventBatch{Events: events, NextCursor: executionbroker.EventCursor(end), Complete: end == len(broker.delivered)}, nil
}

func (broker *ScriptedBroker) apply(event executionbroker.Event) {
	value := broker.orders[event.ClientOrderID]
	if event.CumulativeFilled >= value.snapshot.CumulativeFilled {
		value.snapshot.CumulativeFilled = event.CumulativeFilled
	}
	if event.FillQuantity > 0 {
		value.snapshot.Fills = append(value.snapshot.Fills, executionbroker.FillSnapshot{ExecutionID: event.FillExecutionID, Quantity: event.FillQuantity, CumulativeFilled: event.CumulativeFilled, Price: event.FillPrice, OccurredAt: event.OccurredAt})
	}
	switch event.Type {
	case executionmodel.ReportAcknowledged:
		if !value.snapshot.State.Terminal() {
			value.snapshot.State = executionmodel.OrderAcknowledged
		}
	case executionmodel.ReportPartialFill:
		value.snapshot.State = executionmodel.OrderPartiallyFilled
	case executionmodel.ReportFill:
		value.snapshot.State = executionmodel.OrderFilled
	case executionmodel.ReportCancelled:
		value.snapshot.State = executionmodel.OrderCancelled
	}
	value.snapshot.UpdatedAt = event.OccurredAt
	broker.orders[event.ClientOrderID] = value
}

func (broker *ScriptedBroker) Snapshot(ctx context.Context, limit int) (executionbroker.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return executionbroker.Snapshot{}, err
	}
	if limit <= 0 || limit > 1000 {
		return executionbroker.Snapshot{}, executionbroker.ErrInvalidRequest
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	values := make([]executionbroker.OrderSnapshot, 0, len(broker.orders))
	for _, value := range broker.orders {
		values = append(values, copySnapshot(value.snapshot))
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ClientOrderID.String() < values[j].ClientOrderID.String() })
	complete := len(values) <= limit
	if len(values) > limit {
		values = values[:limit]
	}
	return executionbroker.Snapshot{Orders: values, Complete: complete, Cursor: executionbroker.EventCursor(len(broker.delivered)), ObservedAt: broker.clock.Now().UTC()}, nil
}

func copySnapshot(value executionbroker.OrderSnapshot) executionbroker.OrderSnapshot {
	value.Fills = append([]executionbroker.FillSnapshot(nil), value.Fills...)
	return value
}

func (broker *ScriptedBroker) Checkpoint() ScriptedCheckpoint {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	orders := make(map[executionmodel.ClientOrderID]scriptedOrder, len(broker.orders))
	for id, value := range broker.orders {
		value.snapshot = copySnapshot(value.snapshot)
		orders[id] = value
	}
	return ScriptedCheckpoint{broker.scenarioIndex, broker.unavailableAttempts, orders, append([]scheduledEvent(nil), broker.scheduled...), append([]executionbroker.Event(nil), broker.delivered...)}
}
func RestoreScripted(clock ScriptClock, scenarios []Scenario, checkpoint ScriptedCheckpoint) (*ScriptedBroker, error) {
	value, err := NewScripted(clock, scenarios)
	if err != nil {
		return nil, err
	}
	if checkpoint.ScenarioIndex < 0 || checkpoint.ScenarioIndex > len(scenarios) {
		return nil, errors.New("invalid paper checkpoint")
	}
	value.scenarioIndex = checkpoint.ScenarioIndex
	value.unavailableAttempts = checkpoint.UnavailableAttempts
	for id, order := range checkpoint.Orders {
		order.snapshot = copySnapshot(order.snapshot)
		value.orders[id] = order
	}
	value.scheduled = append([]scheduledEvent(nil), checkpoint.Scheduled...)
	value.delivered = append([]executionbroker.Event(nil), checkpoint.Delivered...)
	return value, nil
}

var _ executionbroker.Port = (*ScriptedBroker)(nil)
