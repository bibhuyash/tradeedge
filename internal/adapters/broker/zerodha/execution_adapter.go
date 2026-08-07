package zerodha

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	brokertelemetry "github.com/bibhuyash/tradeedge/internal/broker/telemetry"
	"github.com/bibhuyash/tradeedge/internal/domain"
	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
)

const maximumExecutionEvents = 10000

type ExecutionAdapter struct {
	transport OrderTransport
	gate      MutationGate
	session   *SessionManager
	mapper    *Mapper
	clock     Clock
	state     *executionState
	recorder  brokertelemetry.Recorder
}

func NewExecutionAdapter(transport OrderTransport, gate MutationGate, session *SessionManager, mapper *Mapper, clock Clock) (*ExecutionAdapter, error) {
	return newExecutionAdapter(transport, gate, session, mapper, clock, ExecutionCheckpoint{}, brokertelemetry.NopRecorder{})
}

func NewExecutionAdapterFromCheckpoint(transport OrderTransport, gate MutationGate, session *SessionManager, mapper *Mapper, clock Clock, checkpoint ExecutionCheckpoint) (*ExecutionAdapter, error) {
	return newExecutionAdapter(transport, gate, session, mapper, clock, checkpoint, brokertelemetry.NopRecorder{})
}

func NewExecutionAdapterInstrumented(transport OrderTransport, gate MutationGate, session *SessionManager, mapper *Mapper, clock Clock, recorder brokertelemetry.Recorder) (*ExecutionAdapter, error) {
	return newExecutionAdapter(transport, gate, session, mapper, clock, ExecutionCheckpoint{}, recorder)
}

func newExecutionAdapter(transport OrderTransport, gate MutationGate, session *SessionManager, mapper *Mapper, clock Clock, checkpoint ExecutionCheckpoint, recorder brokertelemetry.Recorder) (*ExecutionAdapter, error) {
	if transport == nil || session == nil || mapper == nil {
		return nil, ErrInvalidConfiguration
	}
	if gate == nil {
		gate = DisabledMutationGate{}
	}
	if clock == nil {
		clock = RealClock{}
	}
	state, err := restoreExecutionState(checkpoint)
	if err != nil {
		return nil, err
	}
	return &ExecutionAdapter{transport: transport, gate: gate, session: session, mapper: mapper, clock: clock, state: state, recorder: brokertelemetry.Safe(recorder)}, nil
}

func (adapter *ExecutionAdapter) Submit(ctx context.Context, submission executionbroker.Submission) (result executionbroker.SubmissionResult, err error) {
	started := adapter.clock.Now()
	defer func() {
		adapter.recorder.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationSubmission, Outcome: submissionOutcome(result, err), Duration: adapter.clock.Now().Sub(started)})
	}()
	if err := adapter.gate.AllowSubmission(ctx); err != nil {
		return executionbroker.SubmissionResult{}, executionbroker.ErrInvalidRequest
	}
	adapter.state.mu.Lock()
	existing, found := adapter.state.records[submission.ClientOrderID]
	if found {
		if submissionsDiffer(existing.Submission, submission) {
			adapter.state.mu.Unlock()
			return executionbroker.SubmissionResult{}, executionbroker.ErrIdentityConflict
		}
		switch existing.State {
		case CorrelationAccepted:
			adapter.state.mu.Unlock()
			return executionbroker.SubmissionResult{Status: executionbroker.SubmissionAccepted, BrokerOrderID: existing.BrokerOrderID}, nil
		case CorrelationRejected:
			adapter.state.mu.Unlock()
			return executionbroker.SubmissionResult{Status: executionbroker.SubmissionRejected, BrokerOrderID: existing.BrokerOrderID}, nil
		case CorrelationUncertain:
			adapter.state.mu.Unlock()
			return executionbroker.SubmissionResult{}, executionbroker.ErrOutcomeUnknown
		}
	}
	adapter.state.mu.Unlock()
	if _, err := adapter.session.Authorization(); err != nil {
		return executionbroker.SubmissionResult{Status: executionbroker.SubmissionRejected}, nil
	}
	var request OrderRequest
	var version string
	if found {
		request, version = existing.Request, existing.MappingVersion
	} else {
		request, version, err = adapter.translateSubmission(submission)
		if err != nil {
			return executionbroker.SubmissionResult{}, err
		}
	}
	hash := hashOrderRequest(request)
	adapter.state.mu.Lock()
	defer adapter.state.mu.Unlock()
	if adapter.state.closed {
		return executionbroker.SubmissionResult{}, executionbroker.ErrUnavailable
	}
	if existing, found := adapter.state.records[submission.ClientOrderID]; found {
		if existing.RequestHash != hash || submissionsDiffer(existing.Submission, submission) {
			return executionbroker.SubmissionResult{}, executionbroker.ErrIdentityConflict
		}
		switch existing.State {
		case CorrelationAccepted:
			return executionbroker.SubmissionResult{Status: executionbroker.SubmissionAccepted, BrokerOrderID: existing.BrokerOrderID}, nil
		case CorrelationRejected:
			return executionbroker.SubmissionResult{Status: executionbroker.SubmissionRejected, BrokerOrderID: existing.BrokerOrderID}, nil
		case CorrelationUncertain:
			return executionbroker.SubmissionResult{}, executionbroker.ErrOutcomeUnknown
		}
	} else {
		if collision, found := adapter.state.tags[request.Tag]; found && collision != submission.ClientOrderID {
			return executionbroker.SubmissionResult{}, executionbroker.ErrIdentityConflict
		}
		adapter.state.records[submission.ClientOrderID] = CorrelationRecord{Submission: submission, Request: request, RequestHash: hash, MappingVersion: version, State: CorrelationPrepared, UpdatedAt: adapter.clock.Now()}
		adapter.state.tags[request.Tag] = submission.ClientOrderID
	}

	response, transportErr := adapter.transport.Place(ctx, request)
	record := adapter.state.records[submission.ClientOrderID]
	record.UpdatedAt = adapter.clock.Now()
	if errors.Is(transportErr, ErrSessionExpired) {
		adapter.session.Expire()
		if response.Delivery == DeliveryPossiblySent {
			record.State = CorrelationUncertain
			adapter.state.records[submission.ClientOrderID] = record
			return executionbroker.SubmissionResult{}, executionbroker.ErrOutcomeUnknown
		}
		record.State = CorrelationRejected
		adapter.state.records[submission.ClientOrderID] = record
		return executionbroker.SubmissionResult{Status: executionbroker.SubmissionRejected}, nil
	}
	if response.Rejected || errors.Is(transportErr, ErrTransportRejected) {
		record.State, record.BrokerOrderID = CorrelationRejected, strings.TrimSpace(response.BrokerOrderID)
		adapter.state.records[submission.ClientOrderID] = record
		return executionbroker.SubmissionResult{Status: executionbroker.SubmissionRejected, BrokerOrderID: record.BrokerOrderID}, nil
	}
	if transportErr != nil {
		mapped := deliveryError(response.Delivery, transportErr)
		if errors.Is(mapped, executionbroker.ErrUnavailable) || (errors.Is(transportErr, ErrRateLimited) && response.Delivery != DeliveryPossiblySent) {
			record.State = CorrelationNotSent
			adapter.state.records[submission.ClientOrderID] = record
			return executionbroker.SubmissionResult{}, executionbroker.ErrUnavailable
		}
		record.State = CorrelationUncertain
		adapter.state.records[submission.ClientOrderID] = record
		return executionbroker.SubmissionResult{}, executionbroker.ErrOutcomeUnknown
	}
	if response.Delivery != DeliveryResponse || strings.TrimSpace(response.BrokerOrderID) == "" {
		record.State = CorrelationUncertain
		adapter.state.records[submission.ClientOrderID] = record
		return executionbroker.SubmissionResult{}, executionbroker.ErrOutcomeUnknown
	}
	record.State, record.BrokerOrderID = CorrelationAccepted, strings.TrimSpace(response.BrokerOrderID)
	adapter.state.records[submission.ClientOrderID] = record
	return executionbroker.SubmissionResult{Status: executionbroker.SubmissionAccepted, BrokerOrderID: record.BrokerOrderID}, nil
}

func (adapter *ExecutionAdapter) Cancel(ctx context.Context, request executionbroker.CancellationRequest) (result executionbroker.CancellationResult, err error) {
	started := adapter.clock.Now()
	defer func() {
		adapter.recorder.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationCancellation, Outcome: genericOutcome(err), Duration: adapter.clock.Now().Sub(started)})
	}()
	if err := adapter.gate.AllowCancellation(ctx); err != nil {
		return executionbroker.CancellationResult{}, executionbroker.ErrInvalidRequest
	}
	if _, err := adapter.session.Authorization(); err != nil {
		return executionbroker.CancellationResult{}, executionbroker.ErrOutcomeUnknown
	}
	adapter.state.mu.Lock()
	defer adapter.state.mu.Unlock()
	if adapter.state.closed {
		return executionbroker.CancellationResult{}, executionbroker.ErrUnavailable
	}
	record, found := adapter.state.records[request.ClientOrderID]
	if !found || record.Submission.OrderID != request.OrderID || record.BrokerOrderID == "" || record.BrokerOrderID != request.BrokerOrderID {
		return executionbroker.CancellationResult{}, executionbroker.ErrIdentityConflict
	}
	response, err := adapter.transport.Cancel(ctx, "regular", request.BrokerOrderID)
	if errors.Is(err, ErrSessionExpired) {
		adapter.session.Expire()
	}
	if err != nil || response.Delivery != DeliveryResponse {
		return executionbroker.CancellationResult{}, executionbroker.ErrOutcomeUnknown
	}
	return executionbroker.CancellationResult{Accepted: response.Accepted}, nil
}

func (adapter *ExecutionAdapter) LookupByClientOrderID(ctx context.Context, id executionmodel.ClientOrderID) (result executionbroker.OrderSnapshot, err error) {
	started := adapter.clock.Now()
	defer func() {
		adapter.recorder.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationLookup, Outcome: genericOutcome(err), Duration: adapter.clock.Now().Sub(started)})
	}()
	adapter.state.mu.Lock()
	record, found := adapter.state.records[id]
	adapter.state.mu.Unlock()
	if !found {
		return executionbroker.OrderSnapshot{}, executionbroker.ErrNotFound
	}
	snapshot, err := adapter.Snapshot(ctx, 1000)
	if err != nil {
		return executionbroker.OrderSnapshot{}, err
	}
	if !snapshot.Complete {
		return executionbroker.OrderSnapshot{}, executionbroker.ErrUnavailable
	}
	var matches []executionbroker.OrderSnapshot
	for _, order := range snapshot.Orders {
		if order.ClientOrderID == id {
			matches = append(matches, order)
		}
	}
	if len(matches) == 0 {
		return executionbroker.OrderSnapshot{}, executionbroker.ErrNotFound
	}
	if len(matches) != 1 || termsDiffer(record.Submission, matches[0]) {
		return executionbroker.OrderSnapshot{}, executionbroker.ErrIdentityConflict
	}
	adapter.state.mu.Lock()
	current := adapter.state.records[id]
	if current.BrokerOrderID != "" && current.BrokerOrderID != matches[0].BrokerOrderID {
		adapter.state.mu.Unlock()
		return executionbroker.OrderSnapshot{}, executionbroker.ErrIdentityConflict
	}
	current.BrokerOrderID, current.State, current.UpdatedAt = matches[0].BrokerOrderID, CorrelationAccepted, adapter.clock.Now()
	adapter.state.records[id] = current
	adapter.state.mu.Unlock()
	return matches[0], nil
}

func (adapter *ExecutionAdapter) EventsAfter(ctx context.Context, cursor executionbroker.EventCursor, limit int) (executionbroker.EventBatch, error) {
	if err := ctx.Err(); err != nil {
		return executionbroker.EventBatch{}, err
	}
	adapter.state.mu.Lock()
	defer adapter.state.mu.Unlock()
	if adapter.state.closed || adapter.state.gap || limit <= 0 || limit > 1000 || uint64(cursor) > uint64(len(adapter.state.events)) {
		return executionbroker.EventBatch{}, executionbroker.ErrUnavailable
	}
	end := int(cursor) + limit
	if end > len(adapter.state.events) {
		end = len(adapter.state.events)
	}
	return executionbroker.EventBatch{Events: append([]executionbroker.Event(nil), adapter.state.events[int(cursor):end]...), NextCursor: executionbroker.EventCursor(end), Complete: end == len(adapter.state.events)}, nil
}

func (adapter *ExecutionAdapter) Snapshot(ctx context.Context, limit int) (result executionbroker.Snapshot, err error) {
	started := adapter.clock.Now()
	defer func() {
		adapter.recorder.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationSnapshot, Outcome: genericOutcome(err), Duration: adapter.clock.Now().Sub(started)})
	}()
	if limit <= 0 || limit > 1000 {
		return executionbroker.Snapshot{}, executionbroker.ErrInvalidRequest
	}
	if _, err := adapter.session.Authorization(); err != nil {
		return executionbroker.Snapshot{}, executionbroker.ErrUnavailable
	}
	orders, err := adapter.transport.Orders(ctx)
	if err != nil {
		if errors.Is(err, ErrSessionExpired) {
			adapter.session.Expire()
		}
		return executionbroker.Snapshot{}, executionbroker.ErrUnavailable
	}
	trades, err := adapter.transport.Trades(ctx)
	if err != nil {
		return executionbroker.Snapshot{}, executionbroker.ErrUnavailable
	}
	return adapter.buildSnapshot(orders, trades, limit)
}

func (adapter *ExecutionAdapter) translateSubmission(value executionbroker.Submission) (OrderRequest, string, error) {
	return translateSubmission(adapter.mapper, value)
}

func translateSubmission(mapper *Mapper, value executionbroker.Submission) (OrderRequest, string, error) {
	if value.AttemptID.IsZero() || value.OrderID.IsZero() || value.ClientOrderID.IsZero() || value.InstrumentID.IsZero() || !value.Quantity.IsValid() || value.LimitPrice.IsZeroValue() || value.SubmittedAt.IsZero() {
		return OrderRequest{}, "", executionbroker.ErrInvalidRequest
	}
	resolved, err := mapper.ResolveCanonical(value.InstrumentID, value.SubmittedAt)
	if err != nil {
		return OrderRequest{}, "", executionbroker.ErrInvalidRequest
	}
	instrument, found := mapper.master.Instrument(value.InstrumentID)
	if !found || (instrument.Type() != domain.InstrumentOption && instrument.Type() != domain.InstrumentFuture) || value.Quantity.Int64()%instrument.LotSize().Int64() != 0 || value.LimitPrice.Currency() != instrument.Currency() || value.LimitPrice.MinorUnits() <= 0 || value.LimitPrice.MinorUnits()%instrument.TickSize().MinorUnits() != 0 {
		return OrderRequest{}, "", executionbroker.ErrInvalidRequest
	}
	transaction := "BUY"
	if value.Side == domain.SideSell {
		transaction = "SELL"
	} else if value.Side != domain.SideBuy {
		return OrderRequest{}, "", executionbroker.ErrInvalidRequest
	}
	return OrderRequest{Tag: compactTag(value.ClientOrderID), TradingSymbol: resolved.TradingSymbol, Exchange: "NFO", TransactionType: transaction, OrderType: "LIMIT", Product: "NRML", Validity: "DAY", Variety: "regular", Quantity: value.Quantity.Int64(), Price: formatINR(value.LimitPrice.MinorUnits())}, resolved.MasterVersion, nil
}

func (adapter *ExecutionAdapter) Checkpoint() ExecutionCheckpoint { return adapter.state.checkpoint() }

func (adapter *ExecutionAdapter) MarkUpdateGap() {
	adapter.state.mu.Lock()
	adapter.state.gap = true
	adapter.state.mu.Unlock()
}

func (adapter *ExecutionAdapter) ClearUpdateGapAfterReconciliation() {
	adapter.state.mu.Lock()
	adapter.state.gap = false
	adapter.state.mu.Unlock()
}

func (adapter *ExecutionAdapter) Shutdown() {
	adapter.state.mu.Lock()
	if adapter.state.closed {
		adapter.state.mu.Unlock()
		return
	}
	adapter.state.closed = true
	adapter.state.mu.Unlock()
	adapter.transport.Close()
}

func formatINR(minor int64) string {
	whole, fraction := minor/100, minor%100
	if fraction < 0 {
		fraction = -fraction
	}
	return fmt.Sprintf("%d.%02d", whole, fraction)
}

func parseINR(value string) (domain.Price, error) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) > 2 || parts[0] == "" {
		return domain.Price{}, executionbroker.ErrInvalidRequest
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return domain.Price{}, executionbroker.ErrInvalidRequest
	}
	fraction := int64(0)
	if len(parts) == 2 {
		if len(parts[1]) == 1 {
			parts[1] += "0"
		}
		if len(parts[1]) != 2 {
			return domain.Price{}, executionbroker.ErrInvalidRequest
		}
		fraction, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return domain.Price{}, executionbroker.ErrInvalidRequest
		}
	}
	if whole > (1<<63-1-fraction)/100 {
		return domain.Price{}, executionbroker.ErrInvalidRequest
	}
	return domain.NewPrice(whole*100+fraction, "INR")
}

func termsDiffer(local executionbroker.Submission, remote executionbroker.OrderSnapshot) bool {
	return local.InstrumentID != remote.InstrumentID || local.Side != remote.Side || local.Quantity != remote.Quantity || local.LimitPrice.MinorUnits() != remote.LimitPrice.MinorUnits() || local.LimitPrice.Currency() != remote.LimitPrice.Currency()
}

func submissionsDiffer(left, right executionbroker.Submission) bool {
	return left.AttemptID != right.AttemptID || left.OrderID != right.OrderID || left.ClientOrderID != right.ClientOrderID || left.InstrumentID != right.InstrumentID || left.Side != right.Side || left.Quantity != right.Quantity || left.LimitPrice.MinorUnits() != right.LimitPrice.MinorUnits() || left.LimitPrice.Currency() != right.LimitPrice.Currency() || !left.SubmittedAt.Equal(right.SubmittedAt)
}

var _ executionbroker.Port = (*ExecutionAdapter)(nil)

func submissionOutcome(result executionbroker.SubmissionResult, err error) brokertelemetry.Outcome {
	if errors.Is(err, executionbroker.ErrOutcomeUnknown) {
		return brokertelemetry.OutcomeUnknown
	}
	if errors.Is(err, executionbroker.ErrIdentityConflict) {
		return brokertelemetry.OutcomeConflict
	}
	if errors.Is(err, executionbroker.ErrInvalidRequest) {
		return brokertelemetry.OutcomeDisabled
	}
	if err != nil {
		return brokertelemetry.OutcomeFailure
	}
	if result.Status == executionbroker.SubmissionRejected {
		return brokertelemetry.OutcomeRejected
	}
	return brokertelemetry.OutcomeSuccess
}

func genericOutcome(err error) brokertelemetry.Outcome {
	if errors.Is(err, executionbroker.ErrOutcomeUnknown) {
		return brokertelemetry.OutcomeUnknown
	}
	if errors.Is(err, executionbroker.ErrIdentityConflict) {
		return brokertelemetry.OutcomeConflict
	}
	if errors.Is(err, executionbroker.ErrInvalidRequest) {
		return brokertelemetry.OutcomeDisabled
	}
	if err != nil {
		return brokertelemetry.OutcomeFailure
	}
	return brokertelemetry.OutcomeSuccess
}

func sortedTrades(values []BrokerTrade) []BrokerTrade {
	result := append([]BrokerTrade(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if !result[i].OccurredAt.Equal(result[j].OccurredAt) {
			return result[i].OccurredAt.Before(result[j].OccurredAt)
		}
		return result[i].TradeID < result[j].TradeID
	})
	return result
}
