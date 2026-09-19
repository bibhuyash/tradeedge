package cost

import (
	"errors"
	"strings"

	"github.com/bibhuyash/tradeedge/internal/research/model"
)

var ErrInvalidCost = errors.New("invalid research cost configuration")

const SchemaV1 = "tradeedge.research.cost/v1"

type Base string

const (
	BaseBuyNotional   Base = "BUY_NOTIONAL"
	BaseSellNotional  Base = "SELL_NOTIONAL"
	BaseTotalNotional Base = "TOTAL_NOTIONAL"
	BasePriorCharges  Base = "PRIOR_CHARGES"
)

type Rounding string

const (
	RoundFloor Rounding = "FLOOR"
	RoundCeil  Rounding = "CEIL"
)

type Rate struct {
	Numerator   int64    `json:"numerator"`
	Denominator int64    `json:"denominator"`
	Base        Base     `json:"base"`
	Rounding    Rounding `json:"rounding"`
}
type Config struct {
	SchemaVersion     string `json:"schema_version"`
	Version           string `json:"version"`
	Brokerage         Rate   `json:"brokerage"`
	STT               Rate   `json:"stt"`
	ExchangeCharges   Rate   `json:"exchange_charges"`
	GST               Rate   `json:"gst"`
	StampDuty         Rate   `json:"stamp_duty"`
	RegulatoryCharges Rate   `json:"regulatory_charges"`
}
type Input struct {
	BuyNotional  int64
	SellNotional int64
	GrossPnL     int64
	Slippage     int64
	SpreadImpact int64
}
type Breakdown struct {
	GrossPnL          int64 `json:"gross_pnl_minor"`
	Brokerage         int64 `json:"brokerage_minor"`
	STT               int64 `json:"stt_minor"`
	ExchangeCharges   int64 `json:"exchange_charges_minor"`
	GST               int64 `json:"gst_minor"`
	StampDuty         int64 `json:"stamp_duty_minor"`
	RegulatoryCharges int64 `json:"regulatory_charges_minor"`
	Slippage          int64 `json:"slippage_minor"`
	SpreadImpact      int64 `json:"spread_impact_minor"`
	TotalCosts        int64 `json:"total_costs_minor"`
	NetPnL            int64 `json:"net_pnl_minor"`
}
type CostBreakdown = Breakdown
type Model struct{ config Config }

func New(config Config) (Model, error) {
	if config.SchemaVersion != SchemaV1 || strings.TrimSpace(config.Version) == "" {
		return Model{}, ErrInvalidCost
	}
	for _, rate := range []Rate{config.Brokerage, config.STT, config.ExchangeCharges, config.GST, config.StampDuty, config.RegulatoryCharges} {
		if validateRate(rate) != nil {
			return Model{}, ErrInvalidCost
		}
	}
	return Model{config: config}, nil
}
func (m Model) Version() string { return m.config.Version }
func (m Model) Calculate(input Input) (Breakdown, error) {
	if input.BuyNotional < 0 || input.SellNotional < 0 || input.Slippage < 0 || input.SpreadImpact < 0 {
		return Breakdown{}, ErrInvalidCost
	}
	prior := int64(0)
	values := make([]int64, 6)
	rates := []Rate{m.config.Brokerage, m.config.STT, m.config.ExchangeCharges, m.config.RegulatoryCharges, m.config.GST, m.config.StampDuty}
	for i, rate := range rates {
		base, err := baseValue(rate.Base, input, prior)
		if err != nil {
			return Breakdown{}, err
		}
		values[i], err = model.MulDiv(base, rate.Numerator, rate.Denominator, rate.Rounding == RoundCeil)
		if err != nil {
			return Breakdown{}, err
		}
		prior, err = model.CheckedAdd(prior, values[i])
		if err != nil {
			return Breakdown{}, err
		}
	}
	// rates order above permits GST (or any later component) to explicitly use PRIOR_CHARGES.
	result := Breakdown{GrossPnL: input.GrossPnL, Brokerage: values[0], STT: values[1], ExchangeCharges: values[2], RegulatoryCharges: values[3], GST: values[4], StampDuty: values[5], Slippage: input.Slippage, SpreadImpact: input.SpreadImpact}
	total := int64(0)
	for _, v := range []int64{result.Brokerage, result.STT, result.ExchangeCharges, result.GST, result.StampDuty, result.RegulatoryCharges, result.Slippage, result.SpreadImpact} {
		var err error
		total, err = model.CheckedAdd(total, v)
		if err != nil {
			return Breakdown{}, err
		}
	}
	result.TotalCosts = total
	net, err := model.CheckedSub(input.GrossPnL, total)
	if err != nil {
		return Breakdown{}, err
	}
	result.NetPnL = net
	return result, nil
}
func validateRate(rate Rate) error {
	if rate.Numerator < 0 || rate.Denominator <= 0 {
		return ErrInvalidCost
	}
	switch rate.Base {
	case BaseBuyNotional, BaseSellNotional, BaseTotalNotional, BasePriorCharges:
	default:
		return ErrInvalidCost
	}
	if rate.Rounding != RoundFloor && rate.Rounding != RoundCeil {
		return ErrInvalidCost
	}
	return nil
}
func baseValue(base Base, input Input, prior int64) (int64, error) {
	switch base {
	case BaseBuyNotional:
		return input.BuyNotional, nil
	case BaseSellNotional:
		return input.SellNotional, nil
	case BaseTotalNotional:
		return model.CheckedAdd(input.BuyNotional, input.SellNotional)
	case BasePriorCharges:
		return prior, nil
	default:
		return 0, ErrInvalidCost
	}
}
