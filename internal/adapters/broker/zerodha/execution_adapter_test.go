package zerodha

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
)

func executionFixture(t *testing.T, transport *FakeOrderTransport, gate MutationGate) (*ExecutionAdapter, executionbroker.Submission, *fixedClock) {
	t.Helper()
	now := time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	clock := &fixedClock{now: now}
	instrument := optionInstrument(t, 2026, time.August, 27)
	master, err := instrumentmaster.New(now, []domain.Instrument{instrument}, []domain.ProviderInstrumentRef{{Provider: Provider, Token: "123", TradingSymbol: "NIFTY26AUG25000CE", InstrumentID: instrument.ID(), ValidFrom: now, ValidUntil: now.Add(24 * time.Hour)}})
	if err != nil {
		t.Fatalf("instrumentmaster.New() error = %v", err)
	}
	mapper, _ := NewMapper(master, 12*time.Hour, clock, nil)
	session, _ := authenticatedSession(now)
	adapter, err := NewExecutionAdapter(transport, gate, session, mapper, clock)
	if err != nil {
		t.Fatalf("NewExecutionAdapter() error = %v", err)
	}
	attempt, _ := executionmodel.NewSubmissionAttemptID("fixture-attempt")
	orderSeed, _ := executionmodel.NewSubmissionAttemptID("fixture-order")
	clientSeed, _ := executionmodel.NewSubmissionAttemptID("fixture-client")
	quantity, _ := domain.NewQuantity(65)
	price, _ := domain.NewPrice(10000, "INR")
	submission := executionbroker.Submission{AttemptID: attempt, OrderID: executionmodel.OrderID(orderSeed), ClientOrderID: executionmodel.ClientOrderID(clientSeed), InstrumentID: instrument.ID(), Side: domain.SideBuy, Quantity: quantity, LimitPrice: price, SubmittedAt: now.Add(time.Hour)}
	return adapter, submission, clock
}

func brokerOrderFor(adapter *ExecutionAdapter, submission executionbroker.Submission, brokerID, status string, filled int64, at time.Time) BrokerOrder {
	request, _, _ := adapter.translateSubmission(submission)
	return BrokerOrder{BrokerOrderID: brokerID, Tag: request.Tag, InstrumentToken: "123", TradingSymbol: request.TradingSymbol, Exchange: request.Exchange, TransactionType: request.TransactionType, OrderType: request.OrderType, Product: request.Product, Validity: request.Validity, Variety: request.Variety, Quantity: request.Quantity, Price: request.Price, Status: status, FilledQuantity: filled, UpdatedAt: at}
}

func TestExecutionAdapterMutationDisabledByDefault(t *testing.T) {
	adapter, submission, _ := executionFixture(t, &FakeOrderTransport{}, nil)
	if _, err := adapter.Submit(context.Background(), submission); !errors.Is(err, executionbroker.ErrInvalidRequest) {
		t.Fatalf("Submit() error = %v", err)
	}
	places, _ := adapter.transport.(*FakeOrderTransport).Counts()
	if places != 0 {
		t.Fatalf("mutation-disabled transport calls = %d", places)
	}
	if _, err := adapter.Cancel(context.Background(), executionbroker.CancellationRequest{}); !errors.Is(err, executionbroker.ErrInvalidRequest) {
		t.Fatalf("disabled Cancel() error = %v", err)
	}
	_, cancellations := adapter.transport.(*FakeOrderTransport).Counts()
	if cancellations != 0 {
		t.Fatalf("mutation-disabled cancellation calls = %d", cancellations)
	}
}

func TestExecutionAdapterSubmissionSuccessIdempotencyAndAuthorityEscalation(t *testing.T) {
	transport := &FakeOrderTransport{Places: []FakePlace{{Response: PlaceResponse{BrokerOrderID: "broker-1", Delivery: DeliveryResponse}}}}
	adapter, submission, _ := executionFixture(t, transport, PermitMutationGate{Submission: true})
	first, err := adapter.Submit(context.Background(), submission)
	if err != nil || first.Status != executionbroker.SubmissionAccepted || first.BrokerOrderID != "broker-1" {
		t.Fatalf("Submit() = %#v, %v", first, err)
	}
	second, err := adapter.Submit(context.Background(), submission)
	if err != nil || second != first {
		t.Fatalf("idempotent Submit() = %#v, %v", second, err)
	}
	places, _ := transport.Counts()
	if places != 1 {
		t.Fatalf("place calls = %d, want 1", places)
	}
	escalated := submission
	escalated.Quantity, _ = domain.NewQuantity(130)
	if _, err := adapter.Submit(context.Background(), escalated); !errors.Is(err, executionbroker.ErrIdentityConflict) {
		t.Fatalf("authority escalation error = %v", err)
	}
}

func TestExecutionAdapterKnownNotSentRetryAndUnknownNeverRetry(t *testing.T) {
	transport := &FakeOrderTransport{Places: []FakePlace{{Response: PlaceResponse{Delivery: DeliveryNotSent}, Err: ErrUnavailable}, {Response: PlaceResponse{BrokerOrderID: "broker-1", Delivery: DeliveryResponse}}}}
	adapter, submission, _ := executionFixture(t, transport, PermitMutationGate{Submission: true})
	if _, err := adapter.Submit(context.Background(), submission); !errors.Is(err, executionbroker.ErrUnavailable) {
		t.Fatalf("known-not-sent error = %v", err)
	}
	if _, err := adapter.Submit(context.Background(), submission); err != nil {
		t.Fatalf("safe retry error = %v", err)
	}

	unknownTransport := &FakeOrderTransport{Places: []FakePlace{{Response: PlaceResponse{Delivery: DeliveryPossiblySent}, Err: ErrDeliveryUnknown}}}
	unknown, request, _ := executionFixture(t, unknownTransport, PermitMutationGate{Submission: true})
	if _, err := unknown.Submit(context.Background(), request); !errors.Is(err, executionbroker.ErrOutcomeUnknown) {
		t.Fatalf("possibly-sent error = %v", err)
	}
	if _, err := unknown.Submit(context.Background(), request); !errors.Is(err, executionbroker.ErrOutcomeUnknown) {
		t.Fatalf("UNKNOWN exact retry error = %v", err)
	}
	places, _ := unknownTransport.Counts()
	if places != 1 {
		t.Fatalf("UNKNOWN was resubmitted: calls = %d", places)
	}
}

func TestExecutionAdapterRejectionRateLimitAndSessionExpiry(t *testing.T) {
	for name, scripted := range map[string]FakePlace{
		"rejection":   {Response: PlaceResponse{Rejected: true, Delivery: DeliveryResponse}, Err: ErrTransportRejected},
		"rate limit":  {Response: PlaceResponse{Delivery: DeliveryResponse}, Err: ErrRateLimited},
		"auth expiry": {Response: PlaceResponse{Delivery: DeliveryResponse}, Err: ErrSessionExpired},
	} {
		t.Run(name, func(t *testing.T) {
			adapter, submission, _ := executionFixture(t, &FakeOrderTransport{Places: []FakePlace{scripted}}, PermitMutationGate{Submission: true})
			result, err := adapter.Submit(context.Background(), submission)
			if name == "rate limit" {
				if !errors.Is(err, executionbroker.ErrUnavailable) {
					t.Fatalf("rate-limit error = %v", err)
				}
				return
			}
			if err != nil || result.Status != executionbroker.SubmissionRejected {
				t.Fatalf("Submit() = %#v, %v", result, err)
			}
			if name == "auth expiry" && adapter.session.Snapshot().State != SessionExpired {
				t.Fatalf("session state = %s", adapter.session.Snapshot().State)
			}
		})
	}
}

func TestExecutionAdapterAmbiguousRateLimitAndSessionExpiryNeverRetry(t *testing.T) {
	for name, transportErr := range map[string]error{
		"rate limit":     ErrRateLimited,
		"session expiry": ErrSessionExpired,
	} {
		t.Run(name, func(t *testing.T) {
			transport := &FakeOrderTransport{Places: []FakePlace{{Response: PlaceResponse{Delivery: DeliveryPossiblySent}, Err: transportErr}}}
			adapter, submission, _ := executionFixture(t, transport, PermitMutationGate{Submission: true})
			if _, err := adapter.Submit(context.Background(), submission); !errors.Is(err, executionbroker.ErrOutcomeUnknown) {
				t.Fatalf("ambiguous failure = %v", err)
			}
			if _, err := adapter.Submit(context.Background(), submission); !errors.Is(err, executionbroker.ErrOutcomeUnknown) {
				t.Fatalf("ambiguous retry = %v", err)
			}
			places, _ := transport.Counts()
			if places != 1 {
				t.Fatalf("ambiguous request was resubmitted: calls = %d", places)
			}
		})
	}
}

func TestExecutionAdapterCancellationAndConcurrentSubmit(t *testing.T) {
	transport := &FakeOrderTransport{Places: []FakePlace{{Response: PlaceResponse{BrokerOrderID: "broker-1", Delivery: DeliveryResponse}}}, Cancellations: []FakeCancel{{Response: CancelResponse{Accepted: true, Delivery: DeliveryResponse}}}}
	adapter, submission, clock := executionFixture(t, transport, PermitMutationGate{Submission: true, Cancellation: true})
	var wait sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wait.Add(1)
		go func() { defer wait.Done(); _, err := adapter.Submit(context.Background(), submission); errs <- err }()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Submit() error = %v", err)
		}
	}
	places, _ := transport.Counts()
	if places != 1 {
		t.Fatalf("concurrent place calls = %d", places)
	}
	result, err := adapter.Cancel(context.Background(), executionbroker.CancellationRequest{OrderID: submission.OrderID, ClientOrderID: submission.ClientOrderID, BrokerOrderID: "broker-1", RequestedAt: clock.Now()})
	if err != nil || !result.Accepted {
		t.Fatalf("Cancel() = %#v, %v", result, err)
	}
}

func TestExecutionAdapterShutdownClosesTransportAndJournal(t *testing.T) {
	transport := &FakeOrderTransport{}
	adapter, _, _ := executionFixture(t, transport, PermitMutationGate{})
	adapter.Shutdown()
	adapter.Shutdown()
	if _, err := adapter.EventsAfter(context.Background(), 0, 1); !errors.Is(err, executionbroker.ErrUnavailable) {
		t.Fatalf("EventsAfter() after shutdown error = %v", err)
	}
	if _, err := transport.Orders(context.Background()); !errors.Is(err, ErrStopped) {
		t.Fatalf("transport remained open: %v", err)
	}
}
