package paper

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	executionhealth "github.com/bibhuyash/tradeedge/internal/execution/health"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

const ObservedCheckpointVersion uint32 = 1

type ObservedOrderCheckpoint struct {
	Submission executionbroker.Submission
	Snapshot   executionbroker.OrderSnapshot
}

type ObservedCheckpoint struct {
	Version uint32
	Orders  []ObservedOrderCheckpoint
	Events  []executionbroker.Event
	Quotes  []string
}

// ObservedBroker simulates fills from canonical quote observations. It has no
// provider client and therefore cannot submit or cancel a real broker order.
type ObservedBroker struct {
	mu     sync.Mutex
	orders map[executionmodel.ClientOrderID]ObservedOrderCheckpoint
	events []executionbroker.Event
	quotes map[string]struct{}
	closed bool
}

func NewObserved() *ObservedBroker {
	return &ObservedBroker{orders: map[executionmodel.ClientOrderID]ObservedOrderCheckpoint{}, quotes: map[string]struct{}{}}
}

func RestoreObserved(checkpoint ObservedCheckpoint) (*ObservedBroker, error) {
	if checkpoint.Version != ObservedCheckpointVersion {
		return nil, errors.New("unsupported observed paper checkpoint")
	}
	broker := NewObserved()
	for _, item := range checkpoint.Orders {
		if item.Submission.ClientOrderID.IsZero() || item.Snapshot.ClientOrderID != item.Submission.ClientOrderID {
			return nil, errors.New("invalid observed paper checkpoint")
		}
		item.Snapshot.Fills = append([]executionbroker.FillSnapshot(nil), item.Snapshot.Fills...)
		broker.orders[item.Submission.ClientOrderID] = item
	}
	broker.events = append([]executionbroker.Event(nil), checkpoint.Events...)
	for _, id := range checkpoint.Quotes {
		if id == "" {
			return nil, errors.New("invalid observed quote checkpoint")
		}
		broker.quotes[id] = struct{}{}
	}
	return broker, nil
}

func (broker *ObservedBroker) Submit(ctx context.Context, request executionbroker.Submission) (executionbroker.SubmissionResult, error) {
	if err := ctx.Err(); err != nil {
		return executionbroker.SubmissionResult{}, err
	}
	if request.AttemptID.IsZero() || request.OrderID.IsZero() || request.ClientOrderID.IsZero() || request.InstrumentID.IsZero() || !request.Quantity.IsValid() || request.LimitPrice.IsZeroValue() || request.SubmittedAt.IsZero() || (request.Side != domain.SideBuy && request.Side != domain.SideSell) {
		return executionbroker.SubmissionResult{}, executionbroker.ErrInvalidRequest
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed {
		return executionbroker.SubmissionResult{}, executionbroker.ErrUnavailable
	}
	if existing, found := broker.orders[request.ClientOrderID]; found {
		if !observedSubmissionEqual(existing.Submission, request) {
			return executionbroker.SubmissionResult{}, executionbroker.ErrIdentityConflict
		}
		return executionbroker.SubmissionResult{Status: executionbroker.SubmissionAccepted, BrokerOrderID: existing.Snapshot.BrokerOrderID}, nil
	}
	brokerID := "paper-observed-" + request.ClientOrderID.String()[:16]
	snapshot := executionbroker.OrderSnapshot{ClientOrderID: request.ClientOrderID, BrokerOrderID: brokerID, InstrumentID: request.InstrumentID, Side: request.Side, Quantity: request.Quantity, LimitPrice: request.LimitPrice, State: executionmodel.OrderAcknowledged, UpdatedAt: request.SubmittedAt.UTC()}
	broker.orders[request.ClientOrderID] = ObservedOrderCheckpoint{Submission: request, Snapshot: snapshot}
	broker.events = append(broker.events, executionbroker.Event{EventID: brokerID + "-ack", ClientOrderID: request.ClientOrderID, BrokerOrderID: brokerID, Type: executionmodel.ReportAcknowledged, Reason: executionmodel.ReasonBrokerAcknowledged, OccurredAt: request.SubmittedAt.UTC()})
	return executionbroker.SubmissionResult{Status: executionbroker.SubmissionAccepted, BrokerOrderID: brokerID}, nil
}

func (broker *ObservedBroker) ObserveQuote(ctx context.Context, quote marketmodel.QuoteEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed {
		return executionbroker.ErrUnavailable
	}
	quoteID := quote.ID().String()
	if _, duplicate := broker.quotes[quoteID]; duplicate {
		return nil
	}
	broker.quotes[quoteID] = struct{}{}
	clientIDs := make([]executionmodel.ClientOrderID, 0, len(broker.orders))
	for clientID := range broker.orders {
		clientIDs = append(clientIDs, clientID)
	}
	sort.Slice(clientIDs, func(i, j int) bool { return clientIDs[i].String() < clientIDs[j].String() })
	for _, clientID := range clientIDs {
		item := broker.orders[clientID]
		if item.Snapshot.InstrumentID != quote.InstrumentID() || item.Snapshot.State.Terminal() || !quote.ExchangeTime().After(item.Submission.SubmittedAt) {
			continue
		}
		level := quote.BestAsk()
		eligible := item.Submission.Side == domain.SideBuy && level != nil && level.Quantity > 0 && level.Price.MinorUnits() <= item.Submission.LimitPrice.MinorUnits()
		if item.Submission.Side == domain.SideSell {
			level = quote.BestBid()
			eligible = level != nil && level.Quantity > 0 && level.Price.MinorUnits() >= item.Submission.LimitPrice.MinorUnits()
		}
		if !eligible || level.Price.Currency() != item.Submission.LimitPrice.Currency() {
			continue
		}
		remaining := item.Submission.Quantity.Int64() - item.Snapshot.CumulativeFilled
		fillQuantity := level.Quantity
		if fillQuantity > remaining {
			fillQuantity = remaining
		}
		cumulative := item.Snapshot.CumulativeFilled + fillQuantity
		kind, state := executionmodel.ReportPartialFill, executionmodel.OrderPartiallyFilled
		if cumulative == item.Submission.Quantity.Int64() {
			kind, state = executionmodel.ReportFill, executionmodel.OrderFilled
		}
		fillID := quoteID + "-" + clientID.String()
		event := executionbroker.Event{EventID: "paper-quote-" + fillID, FillExecutionID: fillID, ClientOrderID: clientID, BrokerOrderID: item.Snapshot.BrokerOrderID, Type: kind, Reason: executionmodel.ReasonBrokerFill, CumulativeFilled: cumulative, FillQuantity: fillQuantity, FillPrice: level.Price, OccurredAt: quote.ExchangeTime()}
		broker.events = append(broker.events, event)
		item.Snapshot.CumulativeFilled, item.Snapshot.State, item.Snapshot.UpdatedAt = cumulative, state, quote.ExchangeTime()
		item.Snapshot.Fills = append(item.Snapshot.Fills, executionbroker.FillSnapshot{ExecutionID: fillID, Quantity: fillQuantity, CumulativeFilled: cumulative, Price: level.Price, OccurredAt: quote.ExchangeTime()})
		broker.orders[clientID] = item
	}
	return nil
}

func (broker *ObservedBroker) Cancel(ctx context.Context, request executionbroker.CancellationRequest) (executionbroker.CancellationResult, error) {
	if err := ctx.Err(); err != nil {
		return executionbroker.CancellationResult{}, err
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if request.RequestedAt.IsZero() {
		return executionbroker.CancellationResult{}, executionbroker.ErrInvalidRequest
	}
	item, found := broker.orders[request.ClientOrderID]
	if !found || item.Submission.OrderID != request.OrderID || item.Snapshot.BrokerOrderID != request.BrokerOrderID {
		return executionbroker.CancellationResult{}, executionbroker.ErrIdentityConflict
	}
	if item.Snapshot.State.Terminal() {
		return executionbroker.CancellationResult{Accepted: item.Snapshot.State == executionmodel.OrderCancelled}, nil
	}
	item.Snapshot.State, item.Snapshot.UpdatedAt = executionmodel.OrderCancelled, request.RequestedAt.UTC()
	broker.orders[request.ClientOrderID] = item
	broker.events = append(broker.events, executionbroker.Event{EventID: item.Snapshot.BrokerOrderID + "-cancel", ClientOrderID: request.ClientOrderID, BrokerOrderID: item.Snapshot.BrokerOrderID, Type: executionmodel.ReportCancelled, Reason: executionmodel.ReasonBrokerCancelled, CumulativeFilled: item.Snapshot.CumulativeFilled, OccurredAt: request.RequestedAt.UTC()})
	return executionbroker.CancellationResult{Accepted: true}, nil
}

func (broker *ObservedBroker) LookupByClientOrderID(ctx context.Context, id executionmodel.ClientOrderID) (executionbroker.OrderSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return executionbroker.OrderSnapshot{}, err
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	item, found := broker.orders[id]
	if !found {
		return executionbroker.OrderSnapshot{}, executionbroker.ErrNotFound
	}
	return copySnapshot(item.Snapshot), nil
}

func (broker *ObservedBroker) EventsAfter(ctx context.Context, cursor executionbroker.EventCursor, limit int) (executionbroker.EventBatch, error) {
	if err := ctx.Err(); err != nil {
		return executionbroker.EventBatch{}, err
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed || limit <= 0 || limit > 1000 || uint64(cursor) > uint64(len(broker.events)) {
		return executionbroker.EventBatch{}, executionbroker.ErrUnavailable
	}
	end := int(cursor) + limit
	if end > len(broker.events) {
		end = len(broker.events)
	}
	return executionbroker.EventBatch{Events: append([]executionbroker.Event(nil), broker.events[int(cursor):end]...), NextCursor: executionbroker.EventCursor(end), Complete: end == len(broker.events)}, nil
}

func (broker *ObservedBroker) Snapshot(ctx context.Context, limit int) (executionbroker.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return executionbroker.Snapshot{}, err
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if limit <= 0 || limit > 1000 {
		return executionbroker.Snapshot{}, executionbroker.ErrInvalidRequest
	}
	values := make([]executionbroker.OrderSnapshot, 0, len(broker.orders))
	observedAt := time.Time{}
	for _, item := range broker.orders {
		values = append(values, copySnapshot(item.Snapshot))
		if item.Snapshot.UpdatedAt.After(observedAt) {
			observedAt = item.Snapshot.UpdatedAt
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ClientOrderID.String() < values[j].ClientOrderID.String() })
	complete := len(values) <= limit
	if !complete {
		values = values[:limit]
	}
	return executionbroker.Snapshot{Orders: values, Complete: complete, Cursor: executionbroker.EventCursor(len(broker.events)), ObservedAt: observedAt.UTC()}, nil
}

func (broker *ObservedBroker) Checkpoint() ObservedCheckpoint {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	result := ObservedCheckpoint{Version: ObservedCheckpointVersion, Events: append([]executionbroker.Event(nil), broker.events...)}
	for _, item := range broker.orders {
		item.Snapshot = copySnapshot(item.Snapshot)
		result.Orders = append(result.Orders, item)
	}
	sort.Slice(result.Orders, func(i, j int) bool {
		return result.Orders[i].Submission.ClientOrderID.String() < result.Orders[j].Submission.ClientOrderID.String()
	})
	for id := range broker.quotes {
		result.Quotes = append(result.Quotes, id)
	}
	sort.Strings(result.Quotes)
	return result
}

func (broker *ObservedBroker) Shutdown() { broker.mu.Lock(); broker.closed = true; broker.mu.Unlock() }
func (broker *ObservedBroker) Health() executionhealth.PaperBroker {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	return executionhealth.PaperBroker{Available: !broker.closed, ActiveOrders: len(broker.orders), DeliveredEvents: len(broker.events)}
}

var _ executionbroker.Port = (*ObservedBroker)(nil)

func observedSubmissionEqual(left, right executionbroker.Submission) bool {
	return left.AttemptID == right.AttemptID && left.OrderID == right.OrderID && left.ClientOrderID == right.ClientOrderID && left.InstrumentID == right.InstrumentID && left.Side == right.Side && left.Quantity == right.Quantity && left.LimitPrice.MinorUnits() == right.LimitPrice.MinorUnits() && left.LimitPrice.Currency() == right.LimitPrice.Currency() && left.SubmittedAt.Equal(right.SubmittedAt)
}
