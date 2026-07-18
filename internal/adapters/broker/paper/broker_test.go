package paper

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/execution"
)

func TestSubmitOrderIsIdempotent(t *testing.T) {
	var generated atomic.Int32
	broker := newTestBroker(t, func() (domain.OrderID, error) {
		generated.Add(1)
		return mustOrderID(t, "paper-1"), nil
	})
	request := validRequest(t, "request-1")

	first, err := broker.SubmitOrder(context.Background(), request)
	if err != nil {
		t.Fatalf("SubmitOrder() error = %v", err)
	}
	second, err := broker.SubmitOrder(context.Background(), request)
	if err != nil {
		t.Fatalf("duplicate SubmitOrder() error = %v", err)
	}
	if first != second {
		t.Fatalf("duplicate result differs: first=%#v second=%#v", first, second)
	}
	if generated.Load() != 1 {
		t.Fatalf("ID generator called %d times, want 1", generated.Load())
	}
	if first.State != domain.OrderAcknowledged {
		t.Fatalf("state = %s, want %s", first.State, domain.OrderAcknowledged)
	}
}

func TestSubmitOrderRejectsConflictingClientRequest(t *testing.T) {
	broker := newTestBroker(t, func() (domain.OrderID, error) {
		return mustOrderID(t, "paper-1"), nil
	})
	first := validRequest(t, "request-1")
	if _, err := broker.SubmitOrder(context.Background(), first); err != nil {
		t.Fatalf("SubmitOrder() error = %v", err)
	}

	second := first
	second.Side = domain.SideSell
	if _, err := broker.SubmitOrder(context.Background(), second); !errors.Is(err, execution.ErrClientRequestConflict) {
		t.Fatalf("SubmitOrder() error = %v, want ErrClientRequestConflict", err)
	}
}

func TestLookupCancelAndPositions(t *testing.T) {
	broker := newTestBroker(t, func() (domain.OrderID, error) {
		return mustOrderID(t, "paper-1"), nil
	})
	order, err := broker.SubmitOrder(context.Background(), validRequest(t, "request-1"))
	if err != nil {
		t.Fatalf("SubmitOrder() error = %v", err)
	}
	found, err := broker.LookupOrder(context.Background(), order.ID)
	if err != nil || found != order {
		t.Fatalf("LookupOrder() = %#v, %v", found, err)
	}

	cancelled, err := broker.CancelOrder(context.Background(), order.ID)
	if err != nil {
		t.Fatalf("CancelOrder() error = %v", err)
	}
	if cancelled.State != domain.OrderCancelled {
		t.Fatalf("state = %s, want %s", cancelled.State, domain.OrderCancelled)
	}
	again, err := broker.CancelOrder(context.Background(), order.ID)
	if err != nil || again != cancelled {
		t.Fatalf("idempotent CancelOrder() = %#v, %v", again, err)
	}

	positions, err := broker.Positions(context.Background(), order.Request.AccountID)
	if err != nil {
		t.Fatalf("Positions() error = %v", err)
	}
	if len(positions) != 0 {
		t.Fatalf("Positions() = %#v, want empty", positions)
	}
}

func TestOperationsRespectCancelledContext(t *testing.T) {
	broker := newTestBroker(t, func() (domain.OrderID, error) {
		return mustOrderID(t, "paper-1"), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := broker.SubmitOrder(ctx, validRequest(t, "request-1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("SubmitOrder() error = %v, want context.Canceled", err)
	}
	if _, err := broker.LookupOrder(ctx, mustOrderID(t, "missing")); !errors.Is(err, context.Canceled) {
		t.Fatalf("LookupOrder() error = %v, want context.Canceled", err)
	}
}

func TestConcurrentDuplicateSubmissionsCreateOneOrder(t *testing.T) {
	var generated atomic.Int32
	broker := newTestBroker(t, func() (domain.OrderID, error) {
		count := generated.Add(1)
		return mustOrderID(t, fmt.Sprintf("paper-%d", count)), nil
	})
	request := validRequest(t, "request-1")

	const callers = 32
	results := make(chan domain.Order, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			order, err := broker.SubmitOrder(context.Background(), request)
			results <- order
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("SubmitOrder() error = %v", err)
		}
	}
	for order := range results {
		if order.ID != domain.OrderID("paper-1") {
			t.Fatalf("order ID = %s, want paper-1", order.ID)
		}
	}
	if generated.Load() != 1 {
		t.Fatalf("ID generator called %d times, want 1", generated.Load())
	}
}

func newTestBroker(t *testing.T, generator IDGenerator) *Broker {
	t.Helper()
	now := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC)
	broker, err := New(func() time.Time { return now }, generator)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return broker
}

func validRequest(t *testing.T, clientID string) domain.OrderRequest {
	t.Helper()
	instrument, _ := domain.NewInstrument("NSE", "NFO-OPT", "NIFTY", "123")
	quantity, _ := domain.NewQuantity(50)
	price, _ := domain.NewPrice(12550, "INR")
	requestID, _ := domain.NewClientRequestID(clientID)
	accountID, _ := domain.NewAccountID("paper-account")
	strategyID, _ := domain.NewStrategyID("strategy-1")
	return domain.OrderRequest{
		ClientRequestID: requestID,
		AccountID:       accountID,
		StrategyID:      strategyID,
		Instrument:      instrument,
		Side:            domain.SideBuy,
		Quantity:        quantity,
		LimitPrice:      price,
	}
}

func mustOrderID(t *testing.T, value string) domain.OrderID {
	t.Helper()
	id, err := domain.NewOrderID(value)
	if err != nil {
		t.Fatalf("NewOrderID() error = %v", err)
	}
	return id
}
