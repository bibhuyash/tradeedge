// Package report renders deterministic, canonical research results.
package report

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/bibhuyash/tradeedge/internal/canonicaljson"
	"github.com/bibhuyash/tradeedge/internal/research/backtest"
	"github.com/bibhuyash/tradeedge/internal/research/model"
)

const SchemaV1 = "tradeedge.research.report/v1"

var ErrInvalidReport = errors.New("invalid research report")

type Rational struct {
	Numerator   int64 `json:"numerator"`
	Denominator int64 `json:"denominator"`
	Available   bool  `json:"available"`
}
type OpenPosition struct {
	InstrumentID    string `json:"instrument_id"`
	Side            string `json:"side"`
	Quantity        int64  `json:"quantity"`
	EntryFillTime   string `json:"entry_fill_time"`
	EntryPriceMinor int64  `json:"entry_price_minor"`
	StrategyVersion string `json:"strategy_version"`
}
type ResearchReport struct {
	SchemaVersion        string                  `json:"schema_version"`
	DatasetVersion       string                  `json:"dataset_version"`
	StrategyVersion      string                  `json:"strategy_version"`
	CostVersion          string                  `json:"cost_version"`
	Start                string                  `json:"start"`
	End                  string                  `json:"end"`
	Evaluations          int64                   `json:"evaluations"`
	Signals              int64                   `json:"signals"`
	Trades               int64                   `json:"trades"`
	WinningTrades        int64                   `json:"winning_trades"`
	LosingTrades         int64                   `json:"losing_trades"`
	GrossPnLMinor        int64                   `json:"gross_pnl_minor"`
	TotalCostsMinor      int64                   `json:"total_costs_minor"`
	NetPnLMinor          int64                   `json:"net_pnl_minor"`
	RealizedPnLMinor     int64                   `json:"realized_pnl_minor"`
	UnrealizedPnLMinor   int64                   `json:"unrealized_pnl_minor"`
	StartingCapitalMinor int64                   `json:"starting_capital_minor"`
	CashMinor            int64                   `json:"cash_minor"`
	Currency             string                  `json:"currency"`
	AverageWinnerMinor   int64                   `json:"average_winner_minor"`
	AverageLoserMinor    int64                   `json:"average_loser_minor"`
	WinRateBPS           int64                   `json:"win_rate_bps"`
	ProfitFactor         Rational                `json:"profit_factor"`
	ExpectancyMinor      int64                   `json:"expectancy_minor"`
	MaxDrawdownMinor     int64                   `json:"max_drawdown_minor"`
	ClosedTrades         []backtest.Trade        `json:"closed_trades"`
	OpenPositions        []OpenPosition          `json:"open_positions"`
	RejectedFills        []backtest.RejectedFill `json:"rejected_fills"`
}
type Reporter struct{}

func (Reporter) Report(result backtest.Result) ([]byte, error) {
	report, err := Build(result)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	return canonicaljson.Object(raw, 16<<20)
}
func Build(result backtest.Result) (ResearchReport, error) {
	if result.DatasetVersion == "" || result.StrategyVersion == "" || result.Start.IsZero() || result.End.Before(result.Start) || result.Cash.IsZeroValue() {
		return ResearchReport{}, ErrInvalidReport
	}
	r := ResearchReport{SchemaVersion: SchemaV1, DatasetVersion: result.DatasetVersion, StrategyVersion: result.StrategyVersion, CostVersion: result.CostVersion, Start: utc(result.Start), End: utc(result.End), Evaluations: result.Evaluations, Signals: result.Signals, Trades: int64(len(result.Trades)), RealizedPnLMinor: result.RealizedPnL.MinorUnits(), UnrealizedPnLMinor: result.UnrealizedPnL.MinorUnits(), StartingCapitalMinor: result.StartingCapital.MinorUnits(), CashMinor: result.Cash.MinorUnits(), Currency: result.Cash.Currency().String(), ClosedTrades: append([]backtest.Trade{}, result.Trades...), OpenPositions: []OpenPosition{}, RejectedFills: append([]backtest.RejectedFill{}, result.RejectedFills...)}
	r.TotalCostsMinor = result.TransactionCosts.MinorUnits()
	profits, losses := int64(0), int64(0)
	for _, trade := range result.Trades {
		var err error
		r.GrossPnLMinor, err = model.CheckedAdd(r.GrossPnLMinor, trade.GrossPnLMinor)
		if err != nil {
			return ResearchReport{}, err
		}
		if trade.NetPnLMinor > 0 {
			r.WinningTrades++
			profits, err = model.CheckedAdd(profits, trade.NetPnLMinor)
			if err != nil {
				return ResearchReport{}, err
			}
		} else if trade.NetPnLMinor < 0 {
			r.LosingTrades++
			losses, err = model.CheckedAdd(losses, -trade.NetPnLMinor)
			if err != nil {
				return ResearchReport{}, err
			}
		}
	}
	openEntryImpact := int64(0)
	for _, position := range result.OpenPositions {
		impact, err := model.CheckedAdd(position.EntryCosts.Slippage, position.EntryCosts.SpreadImpact)
		if err != nil {
			return ResearchReport{}, err
		}
		openEntryImpact, err = model.CheckedAdd(openEntryImpact, impact)
		if err != nil {
			return ResearchReport{}, err
		}
	}
	var err error
	r.GrossPnLMinor, err = model.CheckedAdd(r.GrossPnLMinor, result.UnrealizedPnL.MinorUnits())
	if err != nil {
		return ResearchReport{}, err
	}
	r.GrossPnLMinor, err = model.CheckedAdd(r.GrossPnLMinor, openEntryImpact)
	if err != nil {
		return ResearchReport{}, err
	}
	r.NetPnLMinor, err = model.CheckedSub(r.GrossPnLMinor, r.TotalCostsMinor)
	if err != nil {
		return ResearchReport{}, err
	}
	if r.WinningTrades > 0 {
		r.AverageWinnerMinor = profits / r.WinningTrades
	}
	if r.LosingTrades > 0 {
		r.AverageLoserMinor = -(losses / r.LosingTrades)
	}
	if r.Trades > 0 {
		r.WinRateBPS = r.WinningTrades * 10_000 / r.Trades
		closedNet := profits - losses
		r.ExpectancyMinor = closedNet / r.Trades
	}
	if losses > 0 {
		numerator, denominator := reduce(profits, losses)
		r.ProfitFactor = Rational{Numerator: numerator, Denominator: denominator, Available: true}
	}
	r.MaxDrawdownMinor = maxDrawdown(result.EquityCurve)
	for _, position := range result.OpenPositions {
		r.OpenPositions = append(r.OpenPositions, OpenPosition{InstrumentID: position.InstrumentID.String(), Side: string(position.Side), Quantity: position.Quantity.Int64(), EntryFillTime: utc(position.EntryFillTime), EntryPriceMinor: position.EntryFillPrice.MinorUnits(), StrategyVersion: position.StrategyVersion})
	}
	return r, nil
}
func maxDrawdown(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	peak := values[0]
	maximum := int64(0)
	for _, value := range values {
		if value > peak {
			peak = value
		}
		drawdown := peak - value
		if drawdown > maximum {
			maximum = drawdown
		}
	}
	return maximum
}
func reduce(a, b int64) (int64, int64) {
	if a == 0 {
		return 0, 1
	}
	x, y := a, b
	for y != 0 {
		x, y = y, x%y
	}
	return a / x, b / x
}
func utc(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
