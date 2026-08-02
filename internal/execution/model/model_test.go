package model_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	"github.com/bibhuyash/tradeedge/internal/risk/testfixture"
)

func fixtureIntent(t *testing.T, decision riskmodel.PortfolioRiskDecision) executionmodel.ExecutionIntent {
	t.Helper()
	approved, ok := decision.ApprovedAllocation()
	if !ok {
		t.Fatal("fixture has no authority")
	}
	legs := make([]executionmodel.IntentLeg, len(approved.LegBounds))
	for index, bound := range approved.LegBounds {
		legs[index] = executionmodel.IntentLeg{InstrumentID: bound.InstrumentID, Side: bound.Side, Ratio: bound.Ratio, Quantity: bound.MaximumUnits, LotSize: bound.LotSize}
	}
	value, err := executionmodel.NewExecutionIntent(executionmodel.ExecutionIntentSpec{SchemaVersion: "execution-intent/v1", Decision: decision,
		MaximumCapital: approved.MaximumCapital, Legs: legs, PortfolioRevision: decision.Spec().ExpectedPortfolioRevision + 1,
		CreatedAt: decision.Spec().GeneratedAt, ExpiresAt: decision.Spec().ExpiresAt})
	if err != nil {
		t.Fatalf("intent: %v", err)
	}
	return value
}

func fixturePlan(t *testing.T, multi bool) (executionmodel.ExecutionIntent, executionmodel.OrderPlan) {
	t.Helper()
	var decision riskmodel.PortfolioRiskDecision
	var err error
	if multi {
		decision, err = testfixture.ApprovedMultiLegDecision()
	} else {
		decision, err = testfixture.ApprovedDecision()
	}
	if err != nil {
		t.Fatal(err)
	}
	intent := fixtureIntent(t, decision)
	price, _ := domain.NewPrice(100, "INR")
	drafts := make([]executionmodel.OrderLegDraft, len(intent.Spec().Legs))
	var buy domain.InstrumentID
	for _, leg := range intent.Spec().Legs {
		if leg.Side == domain.SideBuy {
			buy = leg.InstrumentID
		}
	}
	for index, leg := range intent.Spec().Legs {
		drafts[index] = executionmodel.OrderLegDraft{InstrumentID: leg.InstrumentID, Side: leg.Side, Quantity: leg.Quantity, LimitPrice: price, Protective: leg.Side == domain.SideBuy}
		if leg.Side == domain.SideSell {
			drafts[index].DependsOn = []domain.InstrumentID{buy}
		}
	}
	plan, err := executionmodel.NewOrderPlan(executionmodel.OrderPlanSpec{SchemaVersion: "order-plan/v1", Intent: intent, Legs: drafts, CreatedAt: intent.Spec().CreatedAt, ExpiresAt: intent.Spec().ExpiresAt})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return intent, plan
}

func fixtureOrder(t *testing.T) executionmodel.Order {
	t.Helper()
	_, plan := fixturePlan(t, false)
	value, err := executionmodel.NewOrder(plan, plan.Legs()[0].ID, plan.Spec().CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func report(t *testing.T, order executionmodel.Order, kind executionmodel.ReportType, cumulative int64, sequence int) executionmodel.ExecutionReport {
	t.Helper()
	reasons := map[executionmodel.ReportType]executionmodel.TransitionReason{executionmodel.ReportPlanned: executionmodel.ReasonPlanned, executionmodel.ReportSubmissionPending: executionmodel.ReasonSubmissionStarted, executionmodel.ReportSubmitted: executionmodel.ReasonBrokerAccepted, executionmodel.ReportAcknowledged: executionmodel.ReasonBrokerAcknowledged, executionmodel.ReportPartialFill: executionmodel.ReasonBrokerFill, executionmodel.ReportFill: executionmodel.ReasonBrokerFill, executionmodel.ReportCancelPending: executionmodel.ReasonCancellationRequested, executionmodel.ReportCancelled: executionmodel.ReasonBrokerCancelled, executionmodel.ReportRejected: executionmodel.ReasonBrokerRejected, executionmodel.ReportExpired: executionmodel.ReasonAuthorityExpired, executionmodel.ReportFailed: executionmodel.ReasonInternalFailure, executionmodel.ReportUnknown: executionmodel.ReasonSubmissionOutcomeUnknown}
	now := order.Spec().CreatedAt.Add(time.Duration(sequence) * time.Second)
	value, err := executionmodel.NewExecutionReport(executionmodel.ExecutionReportSpec{SchemaVersion: "execution-report/v1", Source: "TEST", SourceEventID: fmt.Sprintf("event-%d", sequence), OrderID: order.ID(), ClientOrderID: order.ClientOrderID(), BrokerOrderID: "opaque-broker-reference", Type: kind, Reason: reasons[kind], CumulativeFilled: cumulative, OccurredAt: now, ReceivedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func apply(t *testing.T, order executionmodel.Order, kind executionmodel.ReportType, cumulative int64, sequence int, fillQuantity int64) executionmodel.Order {
	t.Helper()
	event := report(t, order, kind, cumulative, sequence)
	var fill *executionmodel.Fill
	if fillQuantity > 0 {
		quantity, _ := domain.NewQuantity(fillQuantity)
		price, _ := domain.NewPrice(100, "INR")
		value, err := executionmodel.NewFill(executionmodel.FillSpec{SchemaVersion: "fill/v1", Source: "TEST", SourceExecutionID: fmt.Sprintf("fill-%d", sequence), OrderID: order.ID(), ReportID: event.ID(), Quantity: quantity, Price: price, OccurredAt: event.Spec().OccurredAt})
		if err != nil {
			t.Fatal(err)
		}
		fill = &value
	}
	next, err := executionmodel.ApplyExecutionReport(order, event, fill)
	if err != nil {
		t.Fatalf("apply %s: %v", kind, err)
	}
	return next
}

func TestExecutionIntentAuthorityAndDeterminism(t *testing.T) {
	decision, _ := testfixture.ApprovedDecision()
	first := fixtureIntent(t, decision)
	second := fixtureIntent(t, decision)
	if first.ID() != second.ID() || !bytes.Equal(first.CanonicalJSON(), second.CanonicalJSON()) {
		t.Fatal("identical authority was not deterministic")
	}
	copySpec := first.Spec()
	copySpec.Legs[0].Quantity = 0
	if first.Spec().Legs[0].Quantity == 0 {
		t.Fatal("intent exposed mutable legs")
	}
	approved, _ := decision.ApprovedAllocation()
	tooMuch, _ := domain.NewQuantity(approved.LegBounds[0].MaximumUnits.Int64() + approved.LegBounds[0].LotSize.Int64())
	_, err := executionmodel.NewExecutionIntent(executionmodel.ExecutionIntentSpec{SchemaVersion: "execution-intent/v1", Decision: decision, MaximumCapital: approved.MaximumCapital, Legs: []executionmodel.IntentLeg{{InstrumentID: approved.LegBounds[0].InstrumentID, Side: approved.LegBounds[0].Side, Ratio: approved.LegBounds[0].Ratio, Quantity: tooMuch, LotSize: approved.LegBounds[0].LotSize}}, PortfolioRevision: decision.Spec().ExpectedPortfolioRevision + 1, CreatedAt: decision.Spec().GeneratedAt, ExpiresAt: decision.Spec().ExpiresAt})
	if !errors.Is(err, executionmodel.ErrAuthorityEscalation) {
		t.Fatalf("authority escalation: %v", err)
	}
	_, err = executionmodel.NewExecutionIntent(executionmodel.ExecutionIntentSpec{SchemaVersion: "execution-intent/v1", Decision: decision, MaximumCapital: approved.MaximumCapital, Legs: first.Spec().Legs, PortfolioRevision: decision.Spec().ExpectedPortfolioRevision + 1, CreatedAt: decision.Spec().ExpiresAt, ExpiresAt: decision.Spec().ExpiresAt.Add(time.Second)})
	if !errors.Is(err, executionmodel.ErrDecisionExpired) {
		t.Fatalf("expired decision: %v", err)
	}
	stale := first.Spec()
	stale.PortfolioRevision++
	_, err = executionmodel.NewExecutionIntent(stale)
	if !errors.Is(err, executionmodel.ErrDecisionStale) {
		t.Fatalf("stale revision: %v", err)
	}
	modified, _ := testfixture.ModifiedDecision()
	_ = fixtureIntent(t, modified)
}

func TestOrderPlanDependenciesAndBuyBeforeSell(t *testing.T) {
	intent, plan := fixturePlan(t, true)
	if len(plan.Legs()) != 2 {
		t.Fatal("expected two legs")
	}
	var buy, sell executionmodel.OrderLeg
	for _, leg := range plan.Legs() {
		if leg.Side == domain.SideBuy {
			buy = leg
		} else {
			sell = leg
		}
	}
	if !buy.Protective || len(sell.DependsOn) != 1 || sell.DependsOn[0] != buy.ID {
		t.Fatal("protective dependency was not preserved")
	}
	price, _ := domain.NewPrice(100, "INR")
	drafts := []executionmodel.OrderLegDraft{}
	for _, leg := range intent.Spec().Legs {
		drafts = append(drafts, executionmodel.OrderLegDraft{InstrumentID: leg.InstrumentID, Side: leg.Side, Quantity: leg.Quantity, LimitPrice: price, Protective: leg.Side == domain.SideBuy})
	}
	_, err := executionmodel.NewOrderPlan(executionmodel.OrderPlanSpec{SchemaVersion: "order-plan/v1", Intent: intent, Legs: drafts, CreatedAt: intent.Spec().CreatedAt, ExpiresAt: intent.Spec().ExpiresAt})
	if !errors.Is(err, executionmodel.ErrUnsafeLegSequence) {
		t.Fatalf("unsafe sell: %v", err)
	}
	for index := range drafts {
		if drafts[index].Side == domain.SideBuy {
			drafts[index].DependsOn = []domain.InstrumentID{sell.InstrumentID}
		} else {
			drafts[index].DependsOn = []domain.InstrumentID{buy.InstrumentID}
		}
	}
	_, err = executionmodel.NewOrderPlan(executionmodel.OrderPlanSpec{SchemaVersion: "order-plan/v1", Intent: intent, Legs: drafts, CreatedAt: intent.Spec().CreatedAt, ExpiresAt: intent.Spec().ExpiresAt})
	if !errors.Is(err, executionmodel.ErrDependencyCycle) {
		t.Fatalf("cycle: %v", err)
	}
}

func TestOrderStateMachineNormalAndAdversarialPaths(t *testing.T) {
	order := fixtureOrder(t)
	order = apply(t, order, executionmodel.ReportPlanned, 0, 1, 0)
	order = apply(t, order, executionmodel.ReportSubmissionPending, 0, 2, 0)
	order = apply(t, order, executionmodel.ReportSubmitted, 0, 3, 0)
	order = apply(t, order, executionmodel.ReportAcknowledged, 0, 4, 0)
	order = apply(t, order, executionmodel.ReportPartialFill, 20, 5, 20)
	stale := apply(t, order, executionmodel.ReportAcknowledged, 20, 6, 0)
	if stale.Spec().State != executionmodel.OrderPartiallyFilled {
		t.Fatal("out-of-order report regressed state")
	}
	order = apply(t, stale, executionmodel.ReportFill, 50, 7, 30)
	if order.Spec().State != executionmodel.OrderFilled || !order.Spec().State.Terminal() {
		t.Fatal("order did not fill")
	}
	invalid := fixtureOrder(t)
	_, err := executionmodel.ApplyExecutionReport(invalid, report(t, invalid, executionmodel.ReportAcknowledged, 0, 20), nil)
	if !errors.Is(err, executionmodel.ErrInvalidTransition) {
		t.Fatalf("invalid transition: %v", err)
	}
	over := fixtureOrder(t)
	over = apply(t, over, executionmodel.ReportPlanned, 0, 21, 0)
	quantity, _ := domain.NewQuantity(51)
	price, _ := domain.NewPrice(100, "INR")
	event := report(t, over, executionmodel.ReportFill, 51, 22)
	fill, _ := executionmodel.NewFill(executionmodel.FillSpec{SchemaVersion: "fill/v1", Source: "TEST", SourceExecutionID: "overfill", OrderID: over.ID(), ReportID: event.ID(), Quantity: quantity, Price: price, OccurredAt: event.Spec().OccurredAt})
	_, err = executionmodel.ApplyExecutionReport(over, event, &fill)
	if !errors.Is(err, executionmodel.ErrOverfill) {
		t.Fatalf("overfill: %v", err)
	}
}

func TestCancellationRejectionExpiryUnknownAndLateFill(t *testing.T) {
	cancelled := fixtureOrder(t)
	cancelled = apply(t, cancelled, executionmodel.ReportPlanned, 0, 30, 0)
	cancelled = apply(t, cancelled, executionmodel.ReportSubmissionPending, 0, 31, 0)
	cancelled = apply(t, cancelled, executionmodel.ReportSubmitted, 0, 32, 0)
	cancelled = apply(t, cancelled, executionmodel.ReportCancelPending, 0, 33, 0)
	cancelled = apply(t, cancelled, executionmodel.ReportCancelled, 0, 34, 0)
	if !cancelled.Spec().State.Terminal() {
		t.Fatal("cancelled not terminal")
	}
	cancelled = apply(t, cancelled, executionmodel.ReportFill, 50, 35, 50)
	if cancelled.Spec().State != executionmodel.OrderFilled {
		t.Fatal("late fill did not correct cancellation")
	}
	rejected := fixtureOrder(t)
	rejected = apply(t, rejected, executionmodel.ReportPlanned, 0, 40, 0)
	rejected = apply(t, rejected, executionmodel.ReportSubmissionPending, 0, 41, 0)
	rejected = apply(t, rejected, executionmodel.ReportRejected, 0, 42, 0)
	if rejected.Spec().State != executionmodel.OrderRejected {
		t.Fatal("rejection missing")
	}
	expired := apply(t, fixtureOrder(t), executionmodel.ReportExpired, 0, 50, 0)
	if expired.Spec().State != executionmodel.OrderExpired {
		t.Fatal("expiry missing")
	}
	unknown := fixtureOrder(t)
	unknown = apply(t, unknown, executionmodel.ReportPlanned, 0, 60, 0)
	unknown = apply(t, unknown, executionmodel.ReportSubmissionPending, 0, 61, 0)
	unknown = apply(t, unknown, executionmodel.ReportUnknown, 0, 62, 0)
	unknown = apply(t, unknown, executionmodel.ReportAcknowledged, 0, 63, 0)
	if unknown.Spec().State != executionmodel.OrderAcknowledged {
		t.Fatal("unknown did not resolve explicitly")
	}
}
