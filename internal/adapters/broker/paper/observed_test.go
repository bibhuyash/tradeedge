package paper

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

func observedSubmission(t *testing.T) executionbroker.Submission {
	t.Helper()
	now := time.Date(2026, 8, 7, 4, 0, 0, 0, time.UTC)
	attempt, _ := executionmodel.NewSubmissionAttemptID("observed-attempt")
	orderSeed, _ := executionmodel.NewSubmissionAttemptID("observed-order")
	clientSeed, _ := executionmodel.NewSubmissionAttemptID("observed-client")
	quantity, _ := domain.NewQuantity(10)
	price, _ := domain.NewPrice(10000, "INR")
	instrument := domain.InstrumentID(sha256.Sum256([]byte("observed-instrument")))
	return executionbroker.Submission{AttemptID: attempt, OrderID: executionmodel.OrderID(orderSeed), ClientOrderID: executionmodel.ClientOrderID(clientSeed), InstrumentID: instrument, Side: domain.SideBuy, Quantity: quantity, LimitPrice: price, SubmittedAt: now}
}

func TestObservedBrokerConcurrentDuplicateQuoteIsIdempotent(t *testing.T) {
	broker := NewObserved()
	submission := observedSubmission(t)
	_, _ = broker.Submit(context.Background(), submission)
	quote := observedQuote(t, submission, submission.SubmittedAt.Add(time.Second), 10)
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() { defer wait.Done(); _ = broker.ObserveQuote(context.Background(), quote) }()
	}
	wait.Wait()
	snapshot, _ := broker.LookupByClientOrderID(context.Background(), submission.ClientOrderID)
	if snapshot.CumulativeFilled != 10 || len(snapshot.Fills) != 1 {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func observedQuote(t *testing.T, submission executionbroker.Submission, at time.Time, askQuantity int64) marketmodel.QuoteEvent {
	t.Helper()
	last, _ := domain.NewPrice(9950, "INR")
	ask, _ := domain.NewPrice(9900, "INR")
	quote, err := marketmodel.NewQuoteEvent(marketmodel.QuoteSpec{InstrumentID: submission.InstrumentID, LastPrice: last, BestAsk: &marketmodel.BookLevel{Price: ask, Quantity: askQuantity}, Volume: 1, ExchangeTime: at, IngestedAt: at, Provenance: marketmodel.Provenance{Provider: "zerodha", ProviderToken: "fixture-token", MasterVersion: "v1", SourceSequence: uint64(at.UnixNano()), HasSequence: true}})
	if err != nil {
		t.Fatal(err)
	}
	return quote
}

func TestObservedBrokerDeterministicFillsDuplicateCheckpointAndShutdown(t *testing.T) {
	broker := NewObserved()
	submission := observedSubmission(t)
	result, err := broker.Submit(context.Background(), submission)
	if err != nil || result.Status != executionbroker.SubmissionAccepted {
		t.Fatalf("Submit()=%#v,%v", result, err)
	}
	stale := observedQuote(t, submission, submission.SubmittedAt, 10)
	if err := broker.ObserveQuote(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	partial := observedQuote(t, submission, submission.SubmittedAt.Add(time.Second), 4)
	if err := broker.ObserveQuote(context.Background(), partial); err != nil {
		t.Fatal(err)
	}
	_ = broker.ObserveQuote(context.Background(), partial)
	snapshot, _ := broker.LookupByClientOrderID(context.Background(), submission.ClientOrderID)
	if snapshot.State != executionmodel.OrderPartiallyFilled || snapshot.CumulativeFilled != 4 || len(snapshot.Fills) != 1 {
		t.Fatalf("partial snapshot=%#v", snapshot)
	}
	restored, err := RestoreObserved(broker.Checkpoint())
	if err != nil {
		t.Fatal(err)
	}
	full := observedQuote(t, submission, submission.SubmittedAt.Add(2*time.Second), 20)
	if err := restored.ObserveQuote(context.Background(), full); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = restored.LookupByClientOrderID(context.Background(), submission.ClientOrderID)
	if snapshot.State != executionmodel.OrderFilled || snapshot.CumulativeFilled != 10 {
		t.Fatalf("full snapshot=%#v", snapshot)
	}
	restored.Shutdown()
	if _, err := restored.Submit(context.Background(), submission); err == nil {
		t.Fatal("Submit after shutdown succeeded")
	}
}

func TestObservedBrokerCancellationNeverCreatesProviderMutation(t *testing.T) {
	broker := NewObserved()
	submission := observedSubmission(t)
	result, _ := broker.Submit(context.Background(), submission)
	cancellation, err := broker.Cancel(context.Background(), executionbroker.CancellationRequest{OrderID: submission.OrderID, ClientOrderID: submission.ClientOrderID, BrokerOrderID: result.BrokerOrderID, RequestedAt: submission.SubmittedAt.Add(time.Second)})
	if err != nil || !cancellation.Accepted {
		t.Fatalf("Cancel()=%#v,%v", cancellation, err)
	}
}
