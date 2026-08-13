package derivatives

import (
	"context"
	"errors"
	"time"

	accountingcoordinator "github.com/bibhuyash/tradeedge/internal/accounting/coordinator"
	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	accountingstorage "github.com/bibhuyash/tradeedge/internal/accounting/storage"
	accountingvaluation "github.com/bibhuyash/tradeedge/internal/accounting/valuation"
	accountingmemory "github.com/bibhuyash/tradeedge/internal/adapters/accounting/memory"
	executionmemory "github.com/bibhuyash/tradeedge/internal/adapters/execution/memory"
	runtimememory "github.com/bibhuyash/tradeedge/internal/adapters/riskruntime/memory"
	"github.com/bibhuyash/tradeedge/internal/domain"
	executioncoordinator "github.com/bibhuyash/tradeedge/internal/execution/coordinator"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	"github.com/bibhuyash/tradeedge/internal/notification"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	riskrunner "github.com/bibhuyash/tradeedge/internal/risk/runner"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

const ConnectedPipelineVersion = "phase8-m2-released-pipeline-composition/v1"

var ErrConnectedPipeline = errors.New("connected derivatives pipeline failed closed")

type ConnectedMode string

const (
	ConnectedShadow ConnectedMode = "SHADOW"
	ConnectedPaper  ConnectedMode = "PAPER"
)

type ConnectedRequest struct {
	Mode                      ConnectedMode
	Proposal                  strategymodel.TradeProposal
	PortfolioID               portfoliomodel.PortfolioID
	ExpectedPortfolioRevision portfoliomodel.PortfolioRevision
	RiskPolicyID              riskmodel.RiskPolicyID
	MasterVersion             instrumentmaster.Version
	At                        time.Time
	ExistingOpenOption        domain.InstrumentID
	Session                   string
	CASRestricted             bool
	StopNewExposure           bool
	Selection                 Selection
	Mark                      *accountingvaluation.MarkPrice
}

type ConnectedResult struct {
	RiskReceipt riskrunner.Receipt
	Decision    riskmodel.PortfolioRiskDecision
	Intent      executionmodel.ExecutionIntent
	Plan        executionmodel.OrderPlan
	Orders      []executionmodel.Order
	Fills       []executionmodel.Fill
	Accounting  []accountingstorage.PublicationReceipt
	Position    accountingmodel.PositionSnapshot
	Valuation   accountingvaluation.PositionValuation
}

// ReleasedPipeline is only composition. Phase 3 owns risk, Phase 4 owns
// intent/OMS/PAPER execution, and Phase 6 owns fill accounting. SHADOW returns
// after the committed Phase 3 decision and cannot reach either mutation owner.
type ReleasedPipeline struct {
	Risk       *riskrunner.Runner
	RiskStore  *runtimememory.Store
	Execution  *executioncoordinator.Coordinator
	OMS        *executionmemory.Store
	Accounting *accountingcoordinator.Coordinator
	Positions  *accountingmemory.Store
	Observer   Observer
}

func (p ReleasedPipeline) Process(ctx context.Context, request ConnectedRequest) (ConnectedResult, error) {
	if p.Risk == nil || p.RiskStore == nil || request.Proposal.IsZero() || request.PortfolioID.IsZero() || request.ExpectedPortfolioRevision.Validate() != nil || request.RiskPolicyID.IsZero() || request.MasterVersion == "" || request.At.IsZero() || (request.Mode != ConnectedShadow && request.Mode != ConnectedPaper) {
		return ConnectedResult{}, ErrConnectedPipeline
	}
	leg := request.Proposal.Draft().Legs[0]
	if request.CASRestricted {
		p.emit(request, notification.KindValidationIncident, notification.CategoryCAS, notification.SeverityWarning, "CAS_RESTRICTED", leg, "BLOCKED")
		return ConnectedResult{}, ErrCASRestricted
	}
	if request.Session != "NORMAL_TRADING" && !(leg.Side == domain.SideSell && request.Session == "EOD_CLOSE") {
		p.emit(request, notification.KindValidationIncident, notification.CategoryControl, notification.SeverityWarning, "SESSION_NOT_ALLOWED", leg, "BLOCKED")
		return ConnectedResult{}, ErrSessionNotAllowed
	}
	if leg.Side == domain.SideBuy && request.StopNewExposure {
		p.emit(request, notification.KindValidationIncident, notification.CategoryControl, notification.SeverityWarning, "STOP_NEW_EXPOSURE", leg, "BLOCKED")
		return ConnectedResult{}, ErrStopNewExposure
	}
	if leg.Side == domain.SideSell && (request.ExistingOpenOption.IsZero() || request.ExistingOpenOption != leg.InstrumentID) {
		return ConnectedResult{}, ErrConnectedPipeline
	}
	p.emit(request, notification.KindStrategyReady, notification.CategoryStrategy, notification.SeverityInfo, "reference candidate ready", leg, "READY")
	p.emit(request, notification.KindOptionSelected, notification.CategoryStrategy, notification.SeverityInfo, "selected option own quote is execution authority", leg, "READY")
	proposalKind := notification.KindEntryProposal
	if leg.Side == domain.SideSell {
		proposalKind = notification.KindExitProposal
	}
	p.emit(request, proposalKind, notification.CategoryProposal, notification.SeverityInfo, request.Proposal.ID().String(), leg, "COMMITTED")
	riskReceipt, err := p.Risk.EvaluateProposal(ctx, riskrunner.Request{PortfolioID: request.PortfolioID, ProposalID: request.Proposal.ID(), ExpectedRevision: request.ExpectedPortfolioRevision, RiskPolicyID: request.RiskPolicyID, InstrumentMasterVersion: request.MasterVersion, LogicalTime: request.At})
	result := ConnectedResult{RiskReceipt: riskReceipt}
	if err != nil {
		p.emit(request, notification.KindValidationIncident, notification.CategoryRisk, notification.SeverityWarning, err.Error(), leg, "FAILED_CLOSED")
		return result, err
	}
	decision, err := p.RiskStore.Decision(ctx, riskReceipt.DecisionID)
	if err != nil {
		return result, err
	}
	result.Decision = decision
	if decision.Spec().Outcome != riskmodel.DecisionApproved && decision.Spec().Outcome != riskmodel.DecisionModified {
		p.emit(request, notification.KindRiskRejected, notification.CategoryRisk, notification.SeverityWarning, decision.ID().String(), leg, string(decision.Spec().Outcome))
		return result, nil
	}
	p.emit(request, notification.KindRiskApproved, notification.CategoryRisk, notification.SeverityInfo, decision.ID().String(), leg, string(decision.Spec().Outcome))
	if request.Mode == ConnectedShadow {
		p.emit(request, notification.KindShadowSignal, notification.CategoryExecution, notification.SeverityInfo, "broker order = NONE", leg, "NO_BROKER_MUTATION")
		return result, nil
	}
	if p.Execution == nil || p.OMS == nil || p.Accounting == nil || p.Positions == nil {
		return result, ErrConnectedPipeline
	}
	approved, ok := decision.ApprovedAllocation()
	if !ok || len(approved.LegBounds) != 1 {
		return result, ErrConnectedPipeline
	}
	bound := approved.LegBounds[0]
	if bound.InstrumentID != leg.InstrumentID || bound.Side != leg.Side || bound.MaximumUnits.Int64() <= 0 || bound.MaximumUnits.Int64()%bound.LotSize.Int64() != 0 {
		return result, ErrConnectedPipeline
	}
	reducing := bound.Side == domain.SideSell && request.ExistingOpenOption == bound.InstrumentID
	intent, err := executionmodel.NewExecutionIntent(executionmodel.ExecutionIntentSpec{SchemaVersion: "execution-intent/v1", Decision: decision, MaximumCapital: approved.MaximumCapital, Legs: []executionmodel.IntentLeg{{InstrumentID: bound.InstrumentID, Side: bound.Side, Ratio: bound.Ratio, Quantity: bound.MaximumUnits, LotSize: bound.LotSize, ReducingExposure: reducing}}, PortfolioRevision: decision.Spec().ExpectedPortfolioRevision + 1, CreatedAt: request.At, ExpiresAt: decision.Spec().ExpiresAt})
	if err != nil {
		return result, err
	}
	result.Intent = intent
	plan, err := executionmodel.NewOrderPlan(executionmodel.OrderPlanSpec{SchemaVersion: "order-plan/v1", Intent: intent, Legs: []executionmodel.OrderLegDraft{{InstrumentID: bound.InstrumentID, Side: bound.Side, Quantity: bound.MaximumUnits, LimitPrice: leg.ReferencePrice, Protective: bound.Side == domain.SideBuy, ReducingExposure: reducing}}, CreatedAt: request.At, ExpiresAt: decision.Spec().ExpiresAt})
	if err != nil {
		return result, err
	}
	result.Plan = plan
	order, err := executionmodel.NewOrder(plan, plan.Legs()[0].ID, request.At)
	if err != nil {
		return result, err
	}
	result.Orders = []executionmodel.Order{order}
	if _, err = p.OMS.RegisterPlan(ctx, intent, plan, result.Orders); err != nil {
		return result, err
	}
	if _, err = p.Execution.ExecutePlan(ctx, plan.ID(), request.At); err != nil {
		p.emit(request, notification.KindExecutionUnknown, notification.CategoryExecution, notification.SeverityCritical, err.Error(), leg, "RECONCILIATION_REQUIRED")
		p.emit(request, notification.KindValidationIncident, notification.CategoryExecution, notification.SeverityWarning, err.Error(), leg, "FAILED_CLOSED")
		return result, err
	}
	fills, err := p.OMS.Fills(ctx, order.ID())
	if err != nil {
		return result, err
	}
	result.Fills = fills
	if len(fills) > 0 {
		p.emit(request, notification.KindPaperFill, notification.CategoryExecution, notification.SeverityInfo, fills[0].ID().String(), leg, "COMMITTED")
	}
	for _, fill := range fills {
		accountingFill, buildErr := accountingmodel.NewAccountingFill(accountingmodel.AccountingFillSpec{SchemaVersion: "accounting-fill/v1", Fill: fill, PortfolioID: request.PortfolioID, InstrumentID: bound.InstrumentID, Side: bound.Side, ReceivedAt: fill.Spec().OccurredAt})
		if buildErr != nil {
			return result, buildErr
		}
		receipt, applyErr := p.Accounting.ApplyFill(ctx, accountingFill)
		if applyErr != nil {
			return result, applyErr
		}
		result.Accounting = append(result.Accounting, receipt)
	}
	positionID, err := accountingmodel.NewPositionID(request.PortfolioID.String(), leg.InstrumentID.String())
	if err != nil {
		return result, err
	}
	position, err := p.Positions.Position(ctx, positionID)
	if err != nil {
		return result, err
	}
	result.Position = position
	result.Valuation, err = accountingvaluation.EvaluatePosition(position, request.Mark, request.At, accountingvaluation.DefaultPolicy())
	if err != nil {
		return result, err
	}
	kind := notification.KindPositionUpdate
	state := string(position.State())
	if position.State() == accountingmodel.PositionFlat {
		kind = notification.KindPositionClosed
	}
	p.emit(request, kind, notification.CategoryValuation, notification.SeverityInfo, result.Valuation.ID.String(), leg, state)
	return result, nil
}

func (p ReleasedPipeline) emit(request ConnectedRequest, kind notification.Kind, category notification.Category, severity notification.Severity, subject string, leg strategymodel.ProposalLeg, state string) {
	if p.Observer == nil {
		return
	}
	details := notification.Details{Subject: subject, State: state, InstrumentID: leg.InstrumentID.String(), PortfolioID: request.PortfolioID.String(), ReferenceID: request.Proposal.ID().String(), PriceMinor: leg.ReferencePrice.MinorUnits(), Currency: leg.ReferencePrice.Currency().String()}
	if !request.Selection.Option.Instrument.ID().IsZero() {
		instrument := request.Selection.Option.Instrument
		details.FutureInstrumentID = request.Selection.Future.Instrument.ID().String()
		details.Expiry = instrument.Expiry().String()
		details.OptionType = string(instrument.OptionType())
		details.StrikeMinor = instrument.Strike().MinorUnits()
		details.Quantity = instrument.LotSize().Int64()
	}
	event, err := notification.NewEvent(notification.EventSpec{SourceID: request.Proposal.ID().String() + "|" + string(kind), TradingDate: request.At.UTC().Format("2006-01-02"), Mode: string(request.Mode), OccurredAt: request.At, Category: category, Kind: kind, Severity: severity, Details: details})
	if err != nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		p.Observer.Observe(event)
	}()
}
