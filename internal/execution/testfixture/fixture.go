package testfixture

import (
	"github.com/bibhuyash/tradeedge/internal/domain"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	riskfixture "github.com/bibhuyash/tradeedge/internal/risk/testfixture"
)

type Fixture struct {
	Intent executionmodel.ExecutionIntent
	Plan   executionmodel.OrderPlan
	Orders []executionmodel.Order
}

func New(multi bool) (Fixture, error) {
	decision, err := riskfixture.ApprovedDecision()
	if multi {
		decision, err = riskfixture.ApprovedMultiLegDecision()
	}
	if err != nil {
		return Fixture{}, err
	}
	approved, _ := decision.ApprovedAllocation()
	legs := make([]executionmodel.IntentLeg, len(approved.LegBounds))
	for index, bound := range approved.LegBounds {
		legs[index] = executionmodel.IntentLeg{InstrumentID: bound.InstrumentID, Side: bound.Side, Ratio: bound.Ratio, Quantity: bound.MaximumUnits, LotSize: bound.LotSize}
	}
	intent, err := executionmodel.NewExecutionIntent(executionmodel.ExecutionIntentSpec{SchemaVersion: "execution-intent/v1", Decision: decision, MaximumCapital: approved.MaximumCapital, Legs: legs, PortfolioRevision: decision.Spec().ExpectedPortfolioRevision + 1, CreatedAt: decision.Spec().GeneratedAt, ExpiresAt: decision.Spec().ExpiresAt})
	if err != nil {
		return Fixture{}, err
	}
	price, _ := domain.NewPrice(100, "INR")
	drafts := make([]executionmodel.OrderLegDraft, len(legs))
	var buy domain.InstrumentID
	for _, leg := range legs {
		if leg.Side == domain.SideBuy {
			buy = leg.InstrumentID
		}
	}
	for index, leg := range legs {
		drafts[index] = executionmodel.OrderLegDraft{InstrumentID: leg.InstrumentID, Side: leg.Side, Quantity: leg.Quantity, LimitPrice: price, Protective: leg.Side == domain.SideBuy}
		if leg.Side == domain.SideSell {
			drafts[index].DependsOn = []domain.InstrumentID{buy}
		}
	}
	plan, err := executionmodel.NewOrderPlan(executionmodel.OrderPlanSpec{SchemaVersion: "order-plan/v1", Intent: intent, Legs: drafts, CreatedAt: intent.Spec().CreatedAt, ExpiresAt: intent.Spec().ExpiresAt})
	if err != nil {
		return Fixture{}, err
	}
	orders := make([]executionmodel.Order, len(plan.Legs()))
	for index, leg := range plan.Legs() {
		orders[index], err = executionmodel.NewOrder(plan, leg.ID, plan.Spec().CreatedAt)
		if err != nil {
			return Fixture{}, err
		}
	}
	return Fixture{intent, plan, orders}, nil
}
