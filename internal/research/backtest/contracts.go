package backtest

import (
	"context"
	"errors"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/research/cost"
	"github.com/bibhuyash/tradeedge/internal/research/features"
	"github.com/bibhuyash/tradeedge/internal/research/model"
)

var (
	ErrInvalidConfiguration = errors.New("invalid research backtest configuration")
	ErrInvalidDecision      = errors.New("invalid research decision")
	ErrInvalidFill          = errors.New("invalid research fill")
	ErrNoLiquidity          = errors.New("research fill rejected: required bid/ask unavailable")
	ErrPositionUnderflow    = errors.New("research position underflow")
)

const EnginePolicyV1 = "tradeedge.research.engine/v1"

type DecisionKind string

const (
	DecisionNoAction DecisionKind = "NO_ACTION"
	DecisionLong     DecisionKind = "LONG"
	DecisionShort    DecisionKind = "SHORT"
	DecisionExit     DecisionKind = "EXIT"
)

type Score struct {
	Value int64 `json:"value"`
	Scale int64 `json:"scale"`
}
type Decision struct {
	Kind         DecisionKind
	Timestamp    time.Time
	InstrumentID domain.InstrumentID
	Reason       string
	Score        *Score
}

type SimulatedOrder struct {
	ID              string
	SignalTime      time.Time
	OrderTime       time.Time
	InstrumentID    domain.InstrumentID
	Side            domain.Side
	Quantity        domain.Quantity
	StrategyVersion string
}
type Fill struct {
	ID                string
	OrderID           string
	SignalTime        time.Time
	OrderTime         time.Time
	FillTime          time.Time
	InstrumentID      domain.InstrumentID
	Side              domain.Side
	Quantity          domain.Quantity
	ReferencePrice    domain.Price
	FillPrice         domain.Price
	SlippageMinor     int64
	SpreadImpactMinor int64
}

type Position struct {
	InstrumentID        domain.InstrumentID
	Side                domain.Side
	Quantity            domain.Quantity
	EntrySignalTime     time.Time
	EntryOrderTime      time.Time
	EntryFillTime       time.Time
	EntryReferencePrice domain.Price
	EntryFillPrice      domain.Price
	EntryCosts          cost.Breakdown
	StrategyVersion     string
}

type Trade struct {
	ID              string         `json:"id"`
	InstrumentID    string         `json:"instrument_id"`
	Side            string         `json:"side"`
	Quantity        int64          `json:"quantity"`
	SignalTime      time.Time      `json:"signal_time"`
	OrderTime       time.Time      `json:"order_time"`
	FillTime        time.Time      `json:"fill_time"`
	ExitSignalTime  time.Time      `json:"exit_signal_time"`
	ExitOrderTime   time.Time      `json:"exit_order_time"`
	ExitFillTime    time.Time      `json:"exit_fill_time"`
	FillPriceMinor  int64          `json:"fill_price_minor"`
	ExitPriceMinor  int64          `json:"exit_price_minor"`
	GrossPnLMinor   int64          `json:"gross_pnl_minor"`
	Costs           cost.Breakdown `json:"costs"`
	NetPnLMinor     int64          `json:"net_pnl_minor"`
	StrategyVersion string         `json:"strategy_version"`
	DatasetVersion  string         `json:"dataset_version"`
}

type RejectedFill struct {
	OrderID string    `json:"order_id"`
	At      time.Time `json:"at"`
	Reason  string    `json:"reason"`
}
type PortfolioView struct{ Position *Position }

type HistoricalSource interface {
	DatasetVersion() string
	Instruments() []model.ResearchInstrument
	Observations() []model.Observation
}
type FeatureEngine interface {
	Build(features.PointInTimeView) (features.FeatureFrame, error)
}
type ResearchStrategy interface {
	Version() string
	Evaluate(context.Context, features.FeatureFrame, PortfolioView) (Decision, error)
}
type FillModel interface {
	Fill(SimulatedOrder, model.Observation) (Fill, error)
}
type CostModel interface {
	Version() string
	Calculate(cost.Input) (cost.Breakdown, error)
}
type Reporter interface{ Report(Result) ([]byte, error) }

type Config struct {
	PolicyVersion   string
	StartingCapital domain.Money
	Quantity        domain.Quantity
}
type Result struct {
	DatasetVersion   string
	StrategyVersion  string
	CostVersion      string
	Start            time.Time
	End              time.Time
	Evaluations      int64
	Signals          int64
	Trades           []Trade
	RejectedFills    []RejectedFill
	OpenPositions    []Position
	StartingCapital  domain.Money
	Cash             domain.Money
	RealizedPnL      domain.Money
	UnrealizedPnL    domain.Money
	TransactionCosts domain.Money
	EquityCurve      []int64
}
