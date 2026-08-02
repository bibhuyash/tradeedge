package zerodha

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	brokertelemetry "github.com/bibhuyash/tradeedge/internal/broker/telemetry"
	"github.com/bibhuyash/tradeedge/internal/domain"
	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
)

func (adapter *ExecutionAdapter) IngestOrderUpdate(ctx context.Context, update BrokerOrder, trades []BrokerTrade) (err error) {
	started := adapter.clock.Now()
	defer func() {
		adapter.recorder.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationOrderUpdate, Outcome: genericOutcome(err), Duration: adapter.clock.Now().Sub(started)})
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	adapter.state.mu.Lock()
	defer adapter.state.mu.Unlock()
	if adapter.state.closed {
		return executionbroker.ErrUnavailable
	}
	clientID, found := adapter.state.tags[update.Tag]
	if !found {
		adapter.state.gap = true
		return executionbroker.ErrIdentityConflict
	}
	record := adapter.state.records[clientID]
	if record.BrokerOrderID != "" && record.BrokerOrderID != update.BrokerOrderID {
		adapter.state.gap = true
		return executionbroker.ErrIdentityConflict
	}
	if err := verifyBrokerOrder(record, update); err != nil {
		adapter.state.gap = true
		return err
	}
	if record.BrokerOrderID == "" {
		record.BrokerOrderID, record.State, record.UpdatedAt = update.BrokerOrderID, CorrelationAccepted, adapter.clock.Now()
		adapter.state.records[clientID] = record
	}

	filtered := make([]BrokerTrade, 0, len(trades))
	for _, trade := range trades {
		if trade.BrokerOrderID == update.BrokerOrderID {
			filtered = append(filtered, trade)
		}
	}
	cumulative := adapter.currentCumulative(update.BrokerOrderID)
	if update.FilledQuantity < cumulative {
		return nil
	}
	for _, trade := range sortedTrades(filtered) {
		eventID := "kite-trade-" + update.BrokerOrderID + "-" + trade.TradeID
		price, err := parseINR(trade.Price)
		if err != nil || trade.TradeID == "" || trade.Quantity <= 0 || trade.OccurredAt.IsZero() {
			adapter.state.gap = true
			return executionbroker.ErrIdentityConflict
		}
		if existing, duplicate := adapter.state.seen[eventID]; duplicate {
			if existing.FillExecutionID != trade.TradeID || existing.FillQuantity != trade.Quantity || existing.FillPrice.MinorUnits() != price.MinorUnits() || existing.FillPrice.Currency() != price.Currency() || !existing.OccurredAt.Equal(trade.OccurredAt.UTC()) {
				adapter.state.gap = true
				return executionbroker.ErrIdentityConflict
			}
			continue
		}
		if cumulative > record.Submission.Quantity.Int64()-trade.Quantity {
			adapter.state.gap = true
			return executionbroker.ErrIdentityConflict
		}
		cumulative += trade.Quantity
		kind := executionmodel.ReportPartialFill
		if cumulative == record.Submission.Quantity.Int64() {
			kind = executionmodel.ReportFill
		}
		event := executionbroker.Event{EventID: eventID, FillExecutionID: trade.TradeID, ClientOrderID: clientID, BrokerOrderID: update.BrokerOrderID, Type: kind, Reason: executionmodel.ReasonBrokerFill, CumulativeFilled: cumulative, FillQuantity: trade.Quantity, FillPrice: price, OccurredAt: trade.OccurredAt.UTC()}
		if err := adapter.state.appendEvent(event); err != nil {
			adapter.state.gap = true
			return err
		}
	}
	if update.FilledQuantity != cumulative {
		adapter.state.gap = true
		return executionbroker.ErrUnavailable
	}

	kind, reason, emit := statusEvent(update.Status)
	if emit && kind != executionmodel.ReportFill && kind != executionmodel.ReportPartialFill {
		occurred := update.UpdatedAt.UTC()
		if occurred.IsZero() {
			adapter.state.gap = true
			return executionbroker.ErrInvalidRequest
		}
		event := executionbroker.Event{EventID: fmt.Sprintf("kite-order-%s-%s-%d-%s", update.BrokerOrderID, kind, cumulative, occurred.Format(time.RFC3339Nano)), ClientOrderID: clientID, BrokerOrderID: update.BrokerOrderID, Type: kind, Reason: reason, CumulativeFilled: cumulative, OccurredAt: occurred}
		if err := adapter.state.appendEvent(event); err != nil {
			adapter.state.gap = true
			return err
		}
	}
	if len(adapter.state.events) > maximumExecutionEvents {
		adapter.state.gap = true
		return executionbroker.ErrUnavailable
	}
	return nil
}

func (adapter *ExecutionAdapter) currentCumulative(brokerOrderID string) int64 {
	var cumulative int64
	for _, event := range adapter.state.events {
		if event.BrokerOrderID == brokerOrderID && event.CumulativeFilled > cumulative {
			cumulative = event.CumulativeFilled
		}
	}
	return cumulative
}

func verifyBrokerOrder(record CorrelationRecord, update BrokerOrder) error {
	if strings.TrimSpace(update.BrokerOrderID) == "" || update.Tag != record.Request.Tag || update.TradingSymbol != record.Request.TradingSymbol || update.Exchange != record.Request.Exchange || update.TransactionType != record.Request.TransactionType || update.OrderType != record.Request.OrderType || update.Product != record.Request.Product || update.Validity != record.Request.Validity || update.Variety != record.Request.Variety || update.Quantity != record.Request.Quantity || update.Price != record.Request.Price || update.FilledQuantity < 0 || update.FilledQuantity > record.Request.Quantity {
		return executionbroker.ErrIdentityConflict
	}
	return nil
}

func statusEvent(status string) (executionmodel.ReportType, executionmodel.TransitionReason, bool) {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "PUT ORDER REQ RECEIVED", "VALIDATION PENDING", "OPEN PENDING", "OPEN", "TRIGGER PENDING", "UPDATE":
		return executionmodel.ReportAcknowledged, executionmodel.ReasonBrokerAcknowledged, true
	case "REJECTED":
		return executionmodel.ReportRejected, executionmodel.ReasonBrokerRejected, true
	case "CANCELLED":
		return executionmodel.ReportCancelled, executionmodel.ReasonBrokerCancelled, true
	case "COMPLETE":
		return executionmodel.ReportFill, executionmodel.ReasonBrokerFill, true
	default:
		return "", "", false
	}
}

func (adapter *ExecutionAdapter) buildSnapshot(orders []BrokerOrder, trades []BrokerTrade, limit int) (executionbroker.Snapshot, error) {
	observed := adapter.clock.Now().UTC()
	result := executionbroker.Snapshot{Complete: true, ObservedAt: observed}
	if len(orders) > limit {
		orders, result.Complete = orders[:limit], false
	}
	tradesByOrder := make(map[string][]BrokerTrade)
	for _, trade := range trades {
		tradesByOrder[trade.BrokerOrderID] = append(tradesByOrder[trade.BrokerOrderID], trade)
	}
	adapter.state.mu.Lock()
	defer adapter.state.mu.Unlock()
	for _, order := range orders {
		resolved, err := adapter.mapper.ResolveToken(order.InstrumentToken, observed)
		if err != nil {
			result.Complete = false
			continue
		}
		price, err := parseINR(order.Price)
		quantity, quantityErr := domain.NewQuantity(order.Quantity)
		state, stateOK := snapshotState(order.Status, order.FilledQuantity, order.Quantity)
		if err != nil || quantityErr != nil || !stateOK || order.BrokerOrderID == "" || order.UpdatedAt.IsZero() {
			result.Complete = false
			continue
		}
		side := domain.SideBuy
		if order.TransactionType == "SELL" {
			side = domain.SideSell
		} else if order.TransactionType != "BUY" {
			result.Complete = false
			continue
		}
		clientID := executionmodel.ClientOrderID{}
		if mapped, found := adapter.state.tags[order.Tag]; found {
			clientID = mapped
			record := adapter.state.records[mapped]
			if verifyBrokerOrder(record, order) != nil || (record.BrokerOrderID != "" && record.BrokerOrderID != order.BrokerOrderID) {
				result.Complete = false
			}
		}
		remote := executionbroker.OrderSnapshot{ClientOrderID: clientID, BrokerOrderID: order.BrokerOrderID, InstrumentID: resolved.InstrumentID, Side: side, Quantity: quantity, LimitPrice: price, State: state, UpdatedAt: order.UpdatedAt.UTC()}
		cumulative := int64(0)
		for _, trade := range sortedTrades(tradesByOrder[order.BrokerOrderID]) {
			tradePrice, tradeErr := parseINR(trade.Price)
			if tradeErr != nil || trade.TradeID == "" || trade.Quantity <= 0 || trade.OccurredAt.IsZero() || cumulative > order.Quantity-trade.Quantity {
				result.Complete = false
				continue
			}
			cumulative += trade.Quantity
			remote.Fills = append(remote.Fills, executionbroker.FillSnapshot{ExecutionID: trade.TradeID, Quantity: trade.Quantity, CumulativeFilled: cumulative, Price: tradePrice, OccurredAt: trade.OccurredAt.UTC()})
		}
		remote.CumulativeFilled = cumulative
		if cumulative != order.FilledQuantity {
			result.Complete = false
		}
		result.Orders = append(result.Orders, remote)
	}
	result.Cursor = executionbroker.EventCursor(len(adapter.state.events))
	if adapter.state.gap {
		result.Complete = false
	}
	sort.Slice(result.Orders, func(i, j int) bool { return result.Orders[i].BrokerOrderID < result.Orders[j].BrokerOrderID })
	return result, nil
}

func snapshotState(status string, filled, quantity int64) (executionmodel.OrderState, bool) {
	if filled < 0 || quantity <= 0 || filled > quantity {
		return "", false
	}
	if filled == quantity {
		return executionmodel.OrderFilled, true
	}
	if filled > 0 {
		return executionmodel.OrderPartiallyFilled, true
	}
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "PUT ORDER REQ RECEIVED", "VALIDATION PENDING", "OPEN PENDING":
		return executionmodel.OrderSubmitted, true
	case "OPEN", "TRIGGER PENDING", "UPDATE":
		return executionmodel.OrderAcknowledged, true
	case "REJECTED":
		return executionmodel.OrderRejected, true
	case "CANCEL PENDING":
		return executionmodel.OrderCancelPending, true
	case "CANCELLED":
		return executionmodel.OrderCancelled, true
	default:
		return "", false
	}
}
