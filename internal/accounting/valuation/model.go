// Package valuation derives immutable financial state from authoritative positions and canonical marks.
package valuation

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

const (
	SchemaVersion                = "phase-6-financial/v1"
	MarkPolicyVersion            = "phase-6-m3-latest-canonical-ltp/v1"
	MaximumPositionsPerPortfolio = 250
)

var ErrInvalid = errors.New("invalid financial valuation")

type Status string

// PriceType preserves exchange price provenance. Only LastTradedPrice is an
// eligible Phase 6 valuation input; CAS and official-close observations must
// remain distinguishable evidence.
type PriceType string

const (
	LastTradedPrice     PriceType = "LAST_TRADED_PRICE"
	CASIndicativePrice  PriceType = "CAS_INDICATIVE_PRICE"
	CASReferencePrice   PriceType = "CAS_REFERENCE_PRICE"
	CASEquilibriumPrice PriceType = "CAS_EQUILIBRIUM_PRICE"
	OfficialClosePrice  PriceType = "OFFICIAL_CLOSE_PRICE"
)

const (
	StatusComplete    Status = "COMPLETE"
	StatusPartial     Status = "PARTIAL"
	StatusStale       Status = "STALE"
	StatusUnavailable Status = "UNAVAILABLE"
)

type Reason string

const (
	ReasonNone               Reason = "NONE"
	ReasonMissingMark        Reason = "MISSING_MARK"
	ReasonStaleMark          Reason = "STALE_MARK"
	ReasonInvalidPrice       Reason = "INVALID_PRICE"
	ReasonClockSkew          Reason = "CLOCK_SKEW"
	ReasonMarketUnavailable  Reason = "MARKET_UNAVAILABLE"
	ReasonRevisionConflict   Reason = "MARKET_REVISION_CONFLICT"
	ReasonCurrencyMismatch   Reason = "CURRENCY_MISMATCH"
	ReasonOverflow           Reason = "ARITHMETIC_OVERFLOW"
	ReasonCapitalUnavailable Reason = "AUTHORITATIVE_CAPITAL_UNAVAILABLE"
)

func (s Status) valid() bool {
	return s == StatusComplete || s == StatusPartial || s == StatusStale || s == StatusUnavailable
}

type MarkPrice struct {
	InstrumentID    domain.InstrumentID           `json:"instrument_id"`
	Price           domain.Price                  `json:"-"`
	PriceType       PriceType                     `json:"price_type"`
	EventID         marketmodel.EventID           `json:"event_id"`
	MarketRevision  string                        `json:"market_revision"`
	MarketChecksum  accountingmodel.StateChecksum `json:"market_checksum"`
	ExchangeTime    time.Time                     `json:"exchange_time"`
	IngestedAt      time.Time                     `json:"ingested_at"`
	Readiness       readiness.State               `json:"readiness"`
	ReadinessReason readiness.ReasonCode          `json:"readiness_reason"`
}

func NewMarkPrice(quote marketmodel.QuoteEvent, revision string, checksum accountingmodel.StateChecksum, state readiness.State, reason readiness.ReasonCode) (MarkPrice, error) {
	revision = strings.TrimSpace(revision)
	if quote.ID().IsZero() || quote.InstrumentID().IsZero() || quote.LastPrice().IsZeroValue() || quote.LastPrice().MinorUnits() <= 0 ||
		revision == "" || checksum.IsZero() || quote.ExchangeTime().IsZero() || quote.IngestedAt().IsZero() || quote.IngestedAt().Before(quote.ExchangeTime()) {
		return MarkPrice{}, ErrInvalid
	}
	return MarkPrice{InstrumentID: quote.InstrumentID(), Price: quote.LastPrice(), PriceType: LastTradedPrice, EventID: quote.ID(), MarketRevision: revision, MarketChecksum: checksum, ExchangeTime: quote.ExchangeTime().UTC(), IngestedAt: quote.IngestedAt().UTC(), Readiness: state, ReadinessReason: reason}, nil
}

type MoneyValue struct {
	Availability portfoliomodel.Availability `json:"availability"`
	Value        domain.Money                `json:"-"`
	Reason       Reason                      `json:"reason,omitempty"`
}

func known(value domain.Money) MoneyValue {
	return MoneyValue{Availability: portfoliomodel.AvailabilityKnown, Value: value}
}
func unavailable(reasons ...Reason) MoneyValue {
	value := MoneyValue{Availability: portfoliomodel.AvailabilityUnavailable}
	if len(reasons) > 0 {
		value.Reason = reasons[0]
	}
	return value
}
func (m MoneyValue) Known() bool {
	return m.Availability == portfoliomodel.AvailabilityKnown && !m.Value.IsZeroValue()
}

type PositionValuation struct {
	ID               accountingmodel.StateChecksum    `json:"id"`
	Checksum         accountingmodel.StateChecksum    `json:"checksum"`
	PositionID       accountingmodel.PositionID       `json:"position_id"`
	PortfolioID      portfoliomodel.PortfolioID       `json:"portfolio_id"`
	InstrumentID     domain.InstrumentID              `json:"instrument_id"`
	PositionRevision accountingmodel.PositionRevision `json:"position_revision"`
	PositionChecksum accountingmodel.StateChecksum    `json:"position_checksum"`
	NetQuantity      int64                            `json:"net_quantity"`
	OpenBasis        domain.Money                     `json:"-"`
	RealizedPnL      domain.Money                     `json:"-"`
	Mark             *MarkPrice                       `json:"mark,omitempty"`
	MarketValue      MoneyValue                       `json:"market_value"`
	UnrealizedPnL    MoneyValue                       `json:"unrealized_pnl"`
	GrossPnL         MoneyValue                       `json:"gross_pnl"`
	Status           Status                           `json:"status"`
	Reason           Reason                           `json:"reason"`
	ValuedAt         time.Time                        `json:"valued_at"`
	canonical        []byte
}

type moneyWire struct {
	Minor    int64  `json:"minor"`
	Currency string `json:"currency"`
}
type measuredWire struct {
	Availability string     `json:"availability"`
	Reason       string     `json:"reason,omitempty"`
	Money        *moneyWire `json:"money,omitempty"`
}

func wireMoney(value domain.Money) moneyWire {
	return moneyWire{value.MinorUnits(), value.Currency().String()}
}
func wireMeasured(value MoneyValue) measuredWire {
	if !value.Known() {
		return measuredWire{Availability: string(value.Availability), Reason: string(value.Reason)}
	}
	w := wireMoney(value.Value)
	return measuredWire{Availability: string(value.Availability), Reason: string(value.Reason), Money: &w}
}

func finalizePosition(value PositionValuation) (PositionValuation, error) {
	if value.PositionID.IsZero() || value.PortfolioID.IsZero() || value.InstrumentID.IsZero() || value.PositionRevision.Validate() != nil || value.PositionChecksum.IsZero() || value.OpenBasis.IsZeroValue() || value.RealizedPnL.IsZeroValue() || !value.Status.valid() || value.ValuedAt.IsZero() {
		return PositionValuation{}, ErrInvalid
	}
	var mark any
	if value.Mark != nil {
		mark = struct {
			InstrumentID, PriceType, EventID, Revision, Checksum, ExchangeTime, IngestedAt, Readiness, Reason string
			Price                                                                                             moneyWire
		}{value.Mark.InstrumentID.String(), string(value.Mark.PriceType), value.Mark.EventID.String(), value.Mark.MarketRevision, value.Mark.MarketChecksum.String(), value.Mark.ExchangeTime.UTC().Format(time.RFC3339Nano), value.Mark.IngestedAt.UTC().Format(time.RFC3339Nano), string(value.Mark.Readiness), string(value.Mark.ReadinessReason), moneyWire{value.Mark.Price.MinorUnits(), value.Mark.Price.Currency().String()}}
	}
	raw, _ := json.Marshal(struct {
		SchemaVersion, PolicyVersion, PositionID, PortfolioID, InstrumentID, PositionChecksum, Status, Reason, ValuedAt string
		PositionRevision                                                                                                uint64
		NetQuantity                                                                                                     int64
		OpenBasis, RealizedPnL                                                                                          moneyWire
		Mark                                                                                                            any `json:"mark,omitempty"`
		MarketValue, UnrealizedPnL, GrossPnL                                                                            measuredWire
	}{SchemaVersion, MarkPolicyVersion, value.PositionID.String(), value.PortfolioID.String(), value.InstrumentID.String(), value.PositionChecksum.String(), string(value.Status), string(value.Reason), value.ValuedAt.UTC().Format(time.RFC3339Nano), uint64(value.PositionRevision), value.NetQuantity, wireMoney(value.OpenBasis), wireMoney(value.RealizedPnL), mark, wireMeasured(value.MarketValue), wireMeasured(value.UnrealizedPnL), wireMeasured(value.GrossPnL)})
	sum, _ := accountingmodel.NewStateChecksum("position-valuation/v1", raw)
	value.ID = sum
	value.Checksum = sum
	value.ValuedAt = value.ValuedAt.UTC()
	value.canonical = raw
	return value, nil
}
func (v PositionValuation) CanonicalJSON() []byte { return append([]byte(nil), v.canonical...) }
func (v PositionValuation) MarshalJSON() ([]byte, error) {
	if len(v.canonical) == 0 {
		return nil, ErrInvalid
	}
	return v.CanonicalJSON(), nil
}

type SourceRevision struct {
	PositionID        accountingmodel.PositionID       `json:"position_id"`
	PositionRevision  accountingmodel.PositionRevision `json:"position_revision"`
	PositionChecksum  accountingmodel.StateChecksum    `json:"position_checksum"`
	ValuationChecksum accountingmodel.StateChecksum    `json:"valuation_checksum"`
	MarketChecksum    accountingmodel.StateChecksum    `json:"market_checksum,omitempty"`
}
type PortfolioFinancialSnapshot struct {
	ID, Checksum                                                                                  accountingmodel.StateChecksum
	PortfolioID                                                                                   portfoliomodel.PortfolioID
	Revision                                                                                      uint64
	Status                                                                                        Status
	Reasons                                                                                       []Reason
	Currency                                                                                      domain.Currency
	PositionCount, OpenPositionCount, ValuedCount, StaleCount, MissingCount                       int
	RealizedPnL, UnrealizedPnL, TotalPnL, LongExposure, ShortExposure, GrossExposure, NetExposure MoneyValue
	PortfolioEquity                                                                               MoneyValue
	Sources                                                                                       []SourceRevision
	GeneratedAt                                                                                   time.Time
	canonical                                                                                     []byte
}

func (v PortfolioFinancialSnapshot) CanonicalJSON() []byte {
	return append([]byte(nil), v.canonical...)
}
func (v PortfolioFinancialSnapshot) MarshalJSON() ([]byte, error) {
	if len(v.canonical) == 0 {
		return nil, ErrInvalid
	}
	return v.CanonicalJSON(), nil
}

func finalizeSnapshot(value PortfolioFinancialSnapshot) (PortfolioFinancialSnapshot, error) {
	if value.PortfolioID.IsZero() || value.Revision == 0 || !value.Status.valid() || value.Currency == "" || value.GeneratedAt.IsZero() || len(value.Sources) > MaximumPositionsPerPortfolio {
		return PortfolioFinancialSnapshot{}, ErrInvalid
	}
	sort.Slice(value.Sources, func(i, j int) bool {
		return value.Sources[i].PositionID.String() < value.Sources[j].PositionID.String()
	})
	sort.Slice(value.Reasons, func(i, j int) bool { return value.Reasons[i] < value.Reasons[j] })
	raw, _ := json.Marshal(struct {
		SchemaVersion, PolicyVersion, PortfolioID, Status, Currency, GeneratedAt                                       string
		Revision                                                                                                       uint64
		Reasons                                                                                                        []Reason
		PositionCount, OpenPositionCount, ValuedCount, StaleCount, MissingCount                                        int
		RealizedPnL, UnrealizedPnL, TotalPnL, LongExposure, ShortExposure, GrossExposure, NetExposure, PortfolioEquity measuredWire
		Sources                                                                                                        []SourceRevision
	}{SchemaVersion, MarkPolicyVersion, value.PortfolioID.String(), string(value.Status), value.Currency.String(), value.GeneratedAt.UTC().Format(time.RFC3339Nano), value.Revision, value.Reasons, value.PositionCount, value.OpenPositionCount, value.ValuedCount, value.StaleCount, value.MissingCount, wireMeasured(value.RealizedPnL), wireMeasured(value.UnrealizedPnL), wireMeasured(value.TotalPnL), wireMeasured(value.LongExposure), wireMeasured(value.ShortExposure), wireMeasured(value.GrossExposure), wireMeasured(value.NetExposure), wireMeasured(value.PortfolioEquity), value.Sources})
	sum, _ := accountingmodel.NewStateChecksum("portfolio-financial-snapshot/v1", raw)
	value.ID = sum
	value.Checksum = sum
	value.GeneratedAt = value.GeneratedAt.UTC()
	value.canonical = raw
	return value, nil
}
