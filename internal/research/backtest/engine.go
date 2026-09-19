package backtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/research/cost"
	"github.com/bibhuyash/tradeedge/internal/research/features"
	"github.com/bibhuyash/tradeedge/internal/research/model"
)

type Engine struct {
	source   HistoricalSource
	features FeatureEngine
	strategy ResearchStrategy
	fills    FillModel
	costs    CostModel
	config   Config
}

func NewEngine(source HistoricalSource, featureEngine FeatureEngine, strategy ResearchStrategy, fillModel FillModel, costModel CostModel, config Config) (Engine, error) {
	if source == nil || featureEngine == nil || strategy == nil || fillModel == nil || costModel == nil || config.PolicyVersion != EnginePolicyV1 || config.StartingCapital.IsZeroValue() || config.StartingCapital.MinorUnits() < 0 || !config.Quantity.IsValid() {
		return Engine{}, ErrInvalidConfiguration
	}
	return Engine{source: source, features: featureEngine, strategy: strategy, fills: fillModel, costs: costModel, config: config}, nil
}

type pendingOrder struct {
	order SimulatedOrder
	exit  bool
}

func (e Engine) Run(ctx context.Context) (Result, error) {
	observations := e.source.Observations()
	if len(observations) == 0 {
		return Result{}, ErrInvalidConfiguration
	}
	instruments := e.source.Instruments()
	byID := make(map[domain.InstrumentID]model.ResearchInstrument, len(instruments))
	for _, v := range instruments {
		byID[v.ID()] = v
	}
	result := Result{DatasetVersion: e.source.DatasetVersion(), StrategyVersion: e.strategy.Version(), CostVersion: e.costs.Version(), Start: observations[0].ExchangeTime(), End: observations[len(observations)-1].ExchangeTime(), StartingCapital: e.config.StartingCapital, Cash: e.config.StartingCapital}
	zero, _ := domain.NewMoney(0, e.config.StartingCapital.Currency().String())
	result.RealizedPnL = zero
	result.UnrealizedPnL = zero
	result.TransactionCosts = zero
	history := make(map[domain.InstrumentID][]model.Observation)
	positions := make(map[domain.InstrumentID]Position)
	pending := make(map[domain.InstrumentID]pendingOrder)
	seenFills := make(map[string]struct{})
	lastPrice := make(map[domain.InstrumentID]domain.Price)
	for _, observation := range observations {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		id := observation.InstrumentID()
		lastPrice[id] = observation.Price()
		if queued, ok := pending[id]; ok && observation.ExchangeTime().After(queued.order.OrderTime) {
			fill, err := e.fills.Fill(queued.order, observation)
			delete(pending, id)
			if errors.Is(err, ErrNoLiquidity) {
				result.RejectedFills = append(result.RejectedFills, RejectedFill{OrderID: queued.order.ID, At: observation.ExchangeTime(), Reason: ErrNoLiquidity.Error()})
			} else if err != nil {
				return Result{}, err
			} else {
				if _, duplicate := seenFills[fill.ID]; duplicate {
					return Result{}, ErrInvalidFill
				}
				seenFills[fill.ID] = struct{}{}
				fillCosts, err := e.costForFill(fill)
				if err != nil {
					return Result{}, err
				}
				result.TransactionCosts, err = moneyAddMinor(result.TransactionCosts, fillCosts.TotalCosts)
				if err != nil {
					return Result{}, err
				}
				cashCharges, err := model.CheckedSub(fillCosts.TotalCosts, fillCosts.Slippage+fillCosts.SpreadImpact)
				if err != nil {
					return Result{}, err
				}
				result.Cash, err = applyCash(result.Cash, fill, cashCharges)
				if err != nil {
					return Result{}, err
				}
				if queued.exit {
					position, exists := positions[id]
					if !exists || position.Quantity != fill.Quantity || position.Side == fill.Side {
						return Result{}, ErrPositionUnderflow
					}
					trade, err := e.closeTrade(position, fill, fillCosts)
					if err != nil {
						return Result{}, err
					}
					result.Trades = append(result.Trades, trade)
					result.RealizedPnL, err = moneyAddMinor(result.RealizedPnL, trade.NetPnLMinor)
					if err != nil {
						return Result{}, err
					}
					delete(positions, id)
				} else {
					if _, exists := positions[id]; exists {
						return Result{}, ErrInvalidDecision
					}
					positions[id] = Position{InstrumentID: id, Side: fill.Side, Quantity: fill.Quantity, EntrySignalTime: fill.SignalTime, EntryOrderTime: fill.OrderTime, EntryFillTime: fill.FillTime, EntryReferencePrice: fill.ReferencePrice, EntryFillPrice: fill.FillPrice, EntryCosts: fillCosts, StrategyVersion: e.strategy.Version()}
				}
			}
		}
		history[id] = append(history[id], observation)
		instrument, ok := byID[id]
		if !ok || !instrument.AvailableAt(observation.ExchangeTime()) {
			return Result{}, model.ErrInvalidDataset
		}
		view, err := features.NewPointInTimeView(observation.ExchangeTime(), instrument, history[id])
		if err != nil {
			return Result{}, err
		}
		frame, err := e.features.Build(view)
		if err != nil {
			return Result{}, err
		}
		position, hasPosition := positions[id]
		portfolio := PortfolioView{}
		if hasPosition {
			copy := position
			portfolio.Position = &copy
		}
		decision, err := e.strategy.Evaluate(ctx, frame, portfolio)
		if err != nil {
			return Result{}, err
		}
		result.Evaluations++
		if decision.Kind != DecisionNoAction {
			result.Signals++
			if _, exists := pending[id]; exists {
				return Result{}, ErrInvalidDecision
			}
			side, exit, err := decisionSide(decision, portfolio)
			if err != nil {
				return Result{}, err
			}
			order := newOrder(decision, side, e.config.Quantity, e.strategy.Version())
			pending[id] = pendingOrder{order: order, exit: exit}
		}
		equity, err := currentEquity(result.Cash, positions, lastPrice)
		if err != nil {
			return Result{}, err
		}
		result.EquityCurve = append(result.EquityCurve, equity)
	}
	keys := make([]string, 0, len(positions))
	positionByKey := make(map[string]Position, len(positions))
	for id, p := range positions {
		k := id.String()
		keys = append(keys, k)
		positionByKey[k] = p
	}
	sort.Strings(keys)
	for _, key := range keys {
		result.OpenPositions = append(result.OpenPositions, positionByKey[key])
	}
	unrealized := int64(0)
	for id, p := range positions {
		mark := lastPrice[id]
		delta, err := model.CheckedSub(mark.MinorUnits(), p.EntryFillPrice.MinorUnits())
		if p.Side == domain.SideSell {
			delta = -delta
		}
		if err != nil {
			return Result{}, err
		}
		value, err := model.CheckedMul(delta, p.Quantity.Int64())
		if err != nil {
			return Result{}, err
		}
		unrealized, err = model.CheckedAdd(unrealized, value)
		if err != nil {
			return Result{}, err
		}
	}
	result.UnrealizedPnL, _ = domain.NewMoney(unrealized, e.config.StartingCapital.Currency().String())
	return result, nil
}
func decisionSide(d Decision, p PortfolioView) (domain.Side, bool, error) {
	switch d.Kind {
	case DecisionLong:
		if p.Position != nil {
			return "", false, ErrInvalidDecision
		}
		return domain.SideBuy, false, nil
	case DecisionShort:
		if p.Position != nil {
			return "", false, ErrInvalidDecision
		}
		return domain.SideSell, false, nil
	case DecisionExit:
		if p.Position == nil {
			return "", false, ErrInvalidDecision
		}
		if p.Position.Side == domain.SideBuy {
			return domain.SideSell, true, nil
		}
		return domain.SideBuy, true, nil
	default:
		return "", false, ErrInvalidDecision
	}
}
func newOrder(d Decision, side domain.Side, quantity domain.Quantity, strategy string) SimulatedOrder {
	payload := "research-order/v1|" + d.Timestamp.UTC().Format("2006-01-02T15:04:05.999999999Z07:00") + "|" + d.InstrumentID.String() + "|" + string(side) + "|" + strategy
	sum := sha256.Sum256([]byte(payload))
	return SimulatedOrder{ID: hex.EncodeToString(sum[:]), SignalTime: d.Timestamp.UTC(), OrderTime: d.Timestamp.UTC(), InstrumentID: d.InstrumentID, Side: side, Quantity: quantity, StrategyVersion: strategy}
}
func (e Engine) costForFill(fill Fill) (cost.Breakdown, error) {
	notional, err := model.CheckedMul(fill.FillPrice.MinorUnits(), fill.Quantity.Int64())
	if err != nil {
		return cost.Breakdown{}, err
	}
	input := cost.Input{Slippage: fill.SlippageMinor, SpreadImpact: fill.SpreadImpactMinor}
	if fill.Side == domain.SideBuy {
		input.BuyNotional = notional
	} else {
		input.SellNotional = notional
	}
	return e.costs.Calculate(input)
}
func applyCash(cash domain.Money, fill Fill, charges int64) (domain.Money, error) {
	notional, err := model.CheckedMul(fill.FillPrice.MinorUnits(), fill.Quantity.Int64())
	if err != nil {
		return domain.Money{}, err
	}
	delta := -notional
	if fill.Side == domain.SideSell {
		delta = notional
	}
	delta, err = model.CheckedSub(delta, charges)
	if err != nil {
		return domain.Money{}, err
	}
	return moneyAddMinor(cash, delta)
}
func moneyAddMinor(value domain.Money, minor int64) (domain.Money, error) {
	other, err := domain.NewMoney(minor, value.Currency().String())
	if err != nil {
		return domain.Money{}, err
	}
	return value.Add(other)
}
func (e Engine) closeTrade(position Position, exit Fill, exitCosts cost.Breakdown) (Trade, error) {
	entryRef, exitRef := position.EntryReferencePrice.MinorUnits(), exit.ReferencePrice.MinorUnits()
	delta, err := model.CheckedSub(exitRef, entryRef)
	if position.Side == domain.SideSell {
		delta = -delta
	}
	if err != nil {
		return Trade{}, err
	}
	gross, err := model.CheckedMul(delta, position.Quantity.Int64())
	if err != nil {
		return Trade{}, err
	}
	combined, err := combineCosts(position.EntryCosts, exitCosts, gross)
	if err != nil {
		return Trade{}, err
	}
	sum := sha256.Sum256([]byte("research-trade/v1|" + position.InstrumentID.String() + "|" + position.EntryFillTime.String() + "|" + exit.FillTime.String()))
	return Trade{ID: hex.EncodeToString(sum[:]), InstrumentID: position.InstrumentID.String(), Side: string(position.Side), Quantity: position.Quantity.Int64(), SignalTime: position.EntrySignalTime, OrderTime: position.EntryOrderTime, FillTime: position.EntryFillTime, ExitSignalTime: exit.SignalTime, ExitOrderTime: exit.OrderTime, ExitFillTime: exit.FillTime, FillPriceMinor: position.EntryFillPrice.MinorUnits(), ExitPriceMinor: exit.FillPrice.MinorUnits(), GrossPnLMinor: gross, Costs: combined, NetPnLMinor: combined.NetPnL, StrategyVersion: position.StrategyVersion, DatasetVersion: e.source.DatasetVersion()}, nil
}
func combineCosts(a, b cost.Breakdown, gross int64) (cost.Breakdown, error) {
	r := cost.Breakdown{GrossPnL: gross}
	targets := []*int64{&r.Brokerage, &r.STT, &r.ExchangeCharges, &r.GST, &r.StampDuty, &r.RegulatoryCharges, &r.Slippage, &r.SpreadImpact}
	left := []int64{a.Brokerage, a.STT, a.ExchangeCharges, a.GST, a.StampDuty, a.RegulatoryCharges, a.Slippage, a.SpreadImpact}
	right := []int64{b.Brokerage, b.STT, b.ExchangeCharges, b.GST, b.StampDuty, b.RegulatoryCharges, b.Slippage, b.SpreadImpact}
	for i := range targets {
		v, err := model.CheckedAdd(left[i], right[i])
		if err != nil {
			return cost.Breakdown{}, err
		}
		*targets[i] = v
		r.TotalCosts, err = model.CheckedAdd(r.TotalCosts, v)
		if err != nil {
			return cost.Breakdown{}, err
		}
	}
	var err error
	r.NetPnL, err = model.CheckedSub(gross, r.TotalCosts)
	return r, err
}
func currentEquity(cash domain.Money, positions map[domain.InstrumentID]Position, last map[domain.InstrumentID]domain.Price) (int64, error) {
	value := cash.MinorUnits()
	for id, p := range positions {
		notional, err := model.CheckedMul(last[id].MinorUnits(), p.Quantity.Int64())
		if err != nil {
			return 0, err
		}
		if p.Side == domain.SideBuy {
			value, err = model.CheckedAdd(value, notional)
		} else {
			value, err = model.CheckedSub(value, notional)
		}
		if err != nil {
			return 0, err
		}
	}
	return value, nil
}
