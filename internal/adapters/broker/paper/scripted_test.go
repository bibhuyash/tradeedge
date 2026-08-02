package paper

import (
	"context"
	"errors"
	"testing"
	"time"

	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	executionfixture "github.com/bibhuyash/tradeedge/internal/execution/testfixture"
)

type manualClock struct{ now time.Time }

func (clock *manualClock) Now() time.Time              { return clock.now }
func (clock *manualClock) Advance(value time.Duration) { clock.now = clock.now.Add(value) }

func paperRequest(t *testing.T, clock *manualClock) executionbroker.Submission {
	t.Helper()
	fixture, err := executionfixture.New(false)
	if err != nil {
		t.Fatal(err)
	}
	order := fixture.Orders[0]
	attempt, _ := executionmodel.NewSubmissionAttemptID(order.ID().String(), "attempt")
	return executionbroker.Submission{AttemptID: attempt, OrderID: order.ID(), ClientOrderID: order.ClientOrderID(), InstrumentID: order.Spec().Leg.InstrumentID, Side: order.Spec().Leg.Side, Quantity: order.Spec().Leg.Quantity, LimitPrice: order.Spec().Leg.LimitPrice, SubmittedAt: clock.Now()}
}

func TestScriptedBrokerScenarios(t *testing.T) {
	behaviors := []Behavior{BehaviorImmediateFill, BehaviorPartialFill, BehaviorDelayedFill, BehaviorReject, BehaviorTimeout, BehaviorLostResponse, BehaviorDuplicateEvents, BehaviorOutOfOrder, BehaviorHold, BehaviorLateFill}
	for _, behavior := range behaviors {
		t.Run(string(behavior), func(t *testing.T) {
			clock := &manualClock{time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)}
			broker, err := NewScripted(clock, []Scenario{{Behavior: behavior, PartialQuantity: 20, Delay: time.Second}})
			if err != nil {
				t.Fatal(err)
			}
			request := paperRequest(t, clock)
			result, submitErr := broker.Submit(context.Background(), request)
			switch behavior {
			case BehaviorTimeout, BehaviorLostResponse:
				if !errors.Is(submitErr, executionbroker.ErrOutcomeUnknown) {
					t.Fatalf("unknown: %v", submitErr)
				}
				if _, err = broker.LookupByClientOrderID(context.Background(), request.ClientOrderID); err != nil {
					t.Fatal("lost accepted order")
				}
			case BehaviorReject:
				if submitErr != nil || result.Status != executionbroker.SubmissionRejected {
					t.Fatalf("reject: %v %v", result, submitErr)
				}
			default:
				if submitErr != nil || result.Status != executionbroker.SubmissionAccepted {
					t.Fatalf("submit: %v %v", result, submitErr)
				}
			}
			batch, eventsErr := broker.EventsAfter(context.Background(), 0, 100)
			if eventsErr != nil {
				t.Fatal(eventsErr)
			}
			if behavior == BehaviorPartialFill || behavior == BehaviorDelayedFill {
				clock.Advance(time.Second)
				later, _ := broker.EventsAfter(context.Background(), batch.NextCursor, 100)
				batch.Events = append(batch.Events, later.Events...)
			}
			if behavior == BehaviorImmediateFill && len(batch.Events) != 2 {
				t.Fatalf("events=%d", len(batch.Events))
			}
			if behavior == BehaviorDuplicateEvents && (len(batch.Events) != 3 || batch.Events[1].EventID != batch.Events[2].EventID) {
				t.Fatal("duplicate event missing")
			}
			if behavior == BehaviorOutOfOrder && (len(batch.Events) != 3 || batch.Events[1].Type != executionmodel.ReportFill || batch.Events[2].Type != executionmodel.ReportAcknowledged) {
				t.Fatal("out-of-order script missing")
			}
		})
	}
}

func TestScriptedBrokerIdempotencyUnavailabilityCancellationAndRestore(t *testing.T) {
	clock := &manualClock{time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)}
	broker, _ := NewScripted(clock, []Scenario{{Behavior: BehaviorLateFill, UnavailableAttempts: 1}})
	request := paperRequest(t, clock)
	if _, err := broker.Submit(context.Background(), request); !errors.Is(err, executionbroker.ErrUnavailable) {
		t.Fatalf("unavailable: %v", err)
	}
	first, err := broker.Submit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := broker.Submit(context.Background(), request)
	if err != nil || first != second {
		t.Fatal("exact retry diverged")
	}
	changed := request
	changed.Quantity++
	if _, err = broker.Submit(context.Background(), changed); !errors.Is(err, executionbroker.ErrIdentityConflict) {
		t.Fatalf("collision: %v", err)
	}
	_, _ = broker.EventsAfter(context.Background(), 0, 100)
	if _, err = broker.Cancel(context.Background(), executionbroker.CancellationRequest{OrderID: request.OrderID, ClientOrderID: request.ClientOrderID, BrokerOrderID: first.BrokerOrderID, RequestedAt: clock.Now()}); err != nil {
		t.Fatal(err)
	}
	cancelled, _ := broker.EventsAfter(context.Background(), 1, 100)
	if len(cancelled.Events) != 1 || cancelled.Events[0].Type != executionmodel.ReportCancelled {
		t.Fatal("cancellation missing")
	}
	clock.Advance(time.Second)
	late, _ := broker.EventsAfter(context.Background(), cancelled.NextCursor, 100)
	if len(late.Events) != 1 || late.Events[0].Type != executionmodel.ReportFill {
		t.Fatal("late fill missing")
	}
	checkpoint := broker.Checkpoint()
	restored, err := RestoreScripted(clock, []Scenario{{Behavior: BehaviorLateFill, UnavailableAttempts: 1}}, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := restored.Snapshot(context.Background(), 100)
	if len(snapshot.Orders) != 1 || snapshot.Orders[0].State != executionmodel.OrderFilled {
		t.Fatal("restore diverged")
	}
	snapshot.Orders[0].Fills[0].ExecutionID = "mutated"
	again, _ := restored.LookupByClientOrderID(context.Background(), request.ClientOrderID)
	if again.Fills[0].ExecutionID == "mutated" {
		t.Fatal("snapshot was not defensively copied")
	}
	checkpointOrder := checkpoint.Orders[request.ClientOrderID]
	checkpointOrder.snapshot.Fills[0].ExecutionID = "checkpoint-mutated"
	checkpoint.Orders[request.ClientOrderID] = checkpointOrder
	original, _ := broker.LookupByClientOrderID(context.Background(), request.ClientOrderID)
	if original.Fills[0].ExecutionID == "checkpoint-mutated" {
		t.Fatal("checkpoint was not defensively copied")
	}
}
