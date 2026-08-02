package zerodha

import (
	"context"
	"errors"
	"testing"
	"time"

	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
)

func acceptedExecution(t *testing.T) (*ExecutionAdapter, executionbroker.Submission, *FakeOrderTransport, time.Time) {
	t.Helper()
	transport := &FakeOrderTransport{Places: []FakePlace{{Response: PlaceResponse{BrokerOrderID: "broker-1", Delivery: DeliveryResponse}}}}
	adapter, submission, clock := executionFixture(t, transport, PermitMutationGate{Submission: true, Cancellation: true})
	if _, err := adapter.Submit(context.Background(), submission); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	return adapter, submission, transport, clock.Now()
}

func TestOrderUpdatePartialFullDuplicateOutOfOrderAndLateFill(t *testing.T) {
	adapter, submission, _, now := acceptedExecution(t)
	open := brokerOrderFor(adapter, submission, "broker-1", "OPEN", 0, now.Add(time.Second))
	if err := adapter.IngestOrderUpdate(context.Background(), open, nil); err != nil {
		t.Fatalf("open update error = %v", err)
	}
	partial := brokerOrderFor(adapter, submission, "broker-1", "UPDATE", 20, now.Add(2*time.Second))
	tradeOne := BrokerTrade{TradeID: "trade-1", BrokerOrderID: "broker-1", Quantity: 20, Price: "100.00", OccurredAt: now.Add(2 * time.Second)}
	if err := adapter.IngestOrderUpdate(context.Background(), partial, []BrokerTrade{tradeOne}); err != nil {
		t.Fatalf("partial update error = %v", err)
	}
	cancelled := brokerOrderFor(adapter, submission, "broker-1", "CANCELLED", 20, now.Add(3*time.Second))
	if err := adapter.IngestOrderUpdate(context.Background(), cancelled, []BrokerTrade{tradeOne}); err != nil {
		t.Fatalf("cancel update error = %v", err)
	}
	complete := brokerOrderFor(adapter, submission, "broker-1", "COMPLETE", 65, now.Add(4*time.Second))
	tradeTwo := BrokerTrade{TradeID: "trade-2", BrokerOrderID: "broker-1", Quantity: 45, Price: "101.00", OccurredAt: now.Add(4 * time.Second)}
	if err := adapter.IngestOrderUpdate(context.Background(), complete, []BrokerTrade{tradeTwo, tradeOne}); err != nil {
		t.Fatalf("late full-fill update error = %v", err)
	}
	if err := adapter.IngestOrderUpdate(context.Background(), complete, []BrokerTrade{tradeOne, tradeTwo}); err != nil {
		t.Fatalf("duplicate update error = %v", err)
	}
	if err := adapter.IngestOrderUpdate(context.Background(), open, nil); err != nil {
		t.Fatalf("out-of-order update error = %v", err)
	}
	batch, err := adapter.EventsAfter(context.Background(), 0, 100)
	if err != nil || len(batch.Events) != 5 {
		t.Fatalf("EventsAfter() count = %d, error = %v", len(batch.Events), err)
	}
	if batch.Events[1].Type != executionmodel.ReportPartialFill || batch.Events[4].Type != executionmodel.ReportFill || batch.Events[4].CumulativeFilled != 65 {
		t.Fatalf("unexpected normalized events: %#v", batch.Events)
	}
}

func TestOrderUpdateChangedDuplicateAndOverfillFailClosed(t *testing.T) {
	adapter, submission, _, now := acceptedExecution(t)
	partial := brokerOrderFor(adapter, submission, "broker-1", "UPDATE", 20, now.Add(time.Second))
	trade := BrokerTrade{TradeID: "trade-1", BrokerOrderID: "broker-1", Quantity: 20, Price: "100.00", OccurredAt: now.Add(time.Second)}
	if err := adapter.IngestOrderUpdate(context.Background(), partial, []BrokerTrade{trade}); err != nil {
		t.Fatal(err)
	}
	trade.Price = "102.00"
	if err := adapter.IngestOrderUpdate(context.Background(), partial, []BrokerTrade{trade}); !errors.Is(err, executionbroker.ErrIdentityConflict) {
		t.Fatalf("changed duplicate error = %v", err)
	}
	if _, err := adapter.EventsAfter(context.Background(), 0, 10); !errors.Is(err, executionbroker.ErrUnavailable) {
		t.Fatalf("gapped EventsAfter() error = %v", err)
	}
}

func TestUnknownLookupSnapshotDisconnectAndRestartRecovery(t *testing.T) {
	transport := &FakeOrderTransport{Places: []FakePlace{{Response: PlaceResponse{Delivery: DeliveryPossiblySent}, Err: ErrDeliveryUnknown}}}
	adapter, submission, clock := executionFixture(t, transport, PermitMutationGate{Submission: true})
	if _, err := adapter.Submit(context.Background(), submission); !errors.Is(err, executionbroker.ErrOutcomeUnknown) {
		t.Fatalf("Submit() error = %v", err)
	}
	if _, err := adapter.LookupByClientOrderID(context.Background(), submission.ClientOrderID); !errors.Is(err, executionbroker.ErrNotFound) {
		t.Fatalf("unresolved lookup error = %v", err)
	}
	order := brokerOrderFor(adapter, submission, "broker-existing", "OPEN", 0, clock.Now().Add(time.Second))
	transport.OrderBook = []BrokerOrder{order}
	snapshot, err := adapter.LookupByClientOrderID(context.Background(), submission.ClientOrderID)
	if err != nil || snapshot.BrokerOrderID != "broker-existing" || snapshot.ClientOrderID != submission.ClientOrderID {
		t.Fatalf("resolved lookup = %#v, %v", snapshot, err)
	}
	adapter.MarkUpdateGap()
	reconciled, err := adapter.Snapshot(context.Background(), 100)
	if err != nil || reconciled.Complete {
		t.Fatalf("gapped Snapshot() = %#v, %v", reconciled, err)
	}
	adapter.ClearUpdateGapAfterReconciliation()
	checkpoint := adapter.Checkpoint()
	restored, err := NewExecutionAdapterFromCheckpoint(transport, PermitMutationGate{Submission: true}, adapter.session, adapter.mapper, clock, checkpoint)
	if err != nil {
		t.Fatalf("restore error = %v", err)
	}
	result, err := restored.Submit(context.Background(), submission)
	if err != nil || result.BrokerOrderID != "broker-existing" {
		t.Fatalf("restored exact submit = %#v, %v", result, err)
	}
}

func TestSnapshotReconciliationMismatchIsIncomplete(t *testing.T) {
	adapter, submission, transport, now := acceptedExecution(t)
	order := brokerOrderFor(adapter, submission, "broker-1", "COMPLETE", 65, now.Add(time.Second))
	transport.OrderBook = []BrokerOrder{order}
	transport.TradeBook = []BrokerTrade{{TradeID: "trade-1", BrokerOrderID: "broker-1", Quantity: 64, Price: "100.00", OccurredAt: now.Add(time.Second)}}
	snapshot, err := adapter.Snapshot(context.Background(), 100)
	if err != nil || snapshot.Complete {
		t.Fatalf("mismatched Snapshot() = %#v, %v", snapshot, err)
	}
	if _, err := adapter.LookupByClientOrderID(context.Background(), submission.ClientOrderID); !errors.Is(err, executionbroker.ErrUnavailable) {
		t.Fatalf("mismatched lookup error = %v", err)
	}
}

func TestExecutionCheckpointRejectsUnknownVersion(t *testing.T) {
	adapter, _, transport, _ := acceptedExecution(t)
	checkpoint := adapter.Checkpoint()
	checkpoint.Version++
	if _, err := NewExecutionAdapterFromCheckpoint(transport, PermitMutationGate{Submission: true}, adapter.session, adapter.mapper, adapter.clock, checkpoint); !errors.Is(err, executionbroker.ErrIdentityConflict) {
		t.Fatalf("unknown checkpoint version error = %v", err)
	}
}
