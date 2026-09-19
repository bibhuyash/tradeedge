package backtest

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/research/model"
)

const FillPolicyV1 = "tradeedge.research.fill/v1"

type BaselineFillModel struct {
	PolicyVersion string
	SlippageBPS   int64
}

func NewBaselineFillModel(policy string, slippageBPS int64) (BaselineFillModel, error) {
	if strings.TrimSpace(policy) != FillPolicyV1 || slippageBPS < 0 || slippageBPS > 10_000 {
		return BaselineFillModel{}, ErrInvalidConfiguration
	}
	return BaselineFillModel{PolicyVersion: policy, SlippageBPS: slippageBPS}, nil
}
func (m BaselineFillModel) Fill(order SimulatedOrder, observation model.Observation) (Fill, error) {
	if order.ID == "" || order.SignalTime.IsZero() || order.OrderTime.Before(order.SignalTime) || !observation.ExchangeTime().After(order.OrderTime) || observation.InstrumentID() != order.InstrumentID || !order.Quantity.IsValid() {
		return Fill{}, ErrInvalidFill
	}
	var book *domain.Price
	if order.Side == domain.SideBuy {
		book = observation.Ask()
	} else if order.Side == domain.SideSell {
		book = observation.Bid()
	} else {
		return Fill{}, ErrInvalidFill
	}
	if book == nil || book.MinorUnits() <= 0 {
		return Fill{}, ErrNoLiquidity
	}
	adjustment, err := model.MulDiv(book.MinorUnits(), m.SlippageBPS, 10_000, true)
	if err != nil {
		return Fill{}, err
	}
	fillMinor := book.MinorUnits()
	if order.Side == domain.SideBuy {
		fillMinor, err = model.CheckedAdd(fillMinor, adjustment)
	} else {
		fillMinor, err = model.CheckedSub(fillMinor, adjustment)
		if fillMinor < 0 {
			return Fill{}, ErrInvalidFill
		}
	}
	if err != nil {
		return Fill{}, err
	}
	fillPrice, err := domain.NewPrice(fillMinor, book.Currency().String())
	if err != nil {
		return Fill{}, err
	}
	reference := observation.Price()
	spreadPerUnit := int64(0)
	if order.Side == domain.SideBuy && book.MinorUnits() > reference.MinorUnits() {
		spreadPerUnit = book.MinorUnits() - reference.MinorUnits()
	}
	if order.Side == domain.SideSell && reference.MinorUnits() > book.MinorUnits() {
		spreadPerUnit = reference.MinorUnits() - book.MinorUnits()
	}
	spread, err := model.CheckedMul(spreadPerUnit, order.Quantity.Int64())
	if err != nil {
		return Fill{}, err
	}
	slippage, err := model.CheckedMul(adjustment, order.Quantity.Int64())
	if err != nil {
		return Fill{}, err
	}
	digest := sha256.Sum256([]byte("research-fill/v1|" + order.ID + "|" + observation.ID().String()))
	return Fill{ID: hex.EncodeToString(digest[:]), OrderID: order.ID, SignalTime: order.SignalTime, OrderTime: order.OrderTime, FillTime: observation.ExchangeTime(), InstrumentID: order.InstrumentID, Side: order.Side, Quantity: order.Quantity, ReferencePrice: reference, FillPrice: fillPrice, SlippageMinor: slippage, SpreadImpactMinor: spread}, nil
}
