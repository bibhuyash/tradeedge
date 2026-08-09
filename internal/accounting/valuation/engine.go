package valuation

import (
	"errors"
	"math"
	"sort"
	"time"

	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

type Policy struct {
	Version                                                     string
	MaximumExchangeAge, MaximumIngestionAge, ClockSkewTolerance time.Duration
}

func DefaultPolicy() Policy {
	return Policy{MarkPolicyVersion, 5 * time.Second, 5 * time.Second, time.Second}
}
func (p Policy) Validate() error {
	if p.Version != MarkPolicyVersion || p.MaximumExchangeAge <= 0 || p.MaximumIngestionAge <= 0 || p.ClockSkewTolerance < 0 {
		return ErrInvalid
	}
	return nil
}

func EvaluatePosition(position accountingmodel.PositionSnapshot, mark *MarkPrice, now time.Time, policy Policy) (PositionValuation, error) {
	if position.IsZero() || now.IsZero() || policy.Validate() != nil {
		return PositionValuation{}, ErrInvalid
	}
	spec := position.Spec()
	base := PositionValuation{PositionID: position.ID(), PortfolioID: spec.PortfolioID, InstrumentID: spec.InstrumentID, PositionRevision: position.Revision(), PositionChecksum: position.Checksum(), NetQuantity: spec.NetQuantity.Int64(), OpenBasis: spec.OpenCostBasis, RealizedPnL: spec.GrossRealizedPnL, ValuedAt: now, Status: StatusUnavailable, Reason: ReasonMissingMark, MarketValue: unavailable(), UnrealizedPnL: unavailable(), GrossPnL: unavailable()}
	if spec.NetQuantity == 0 {
		zero, _ := domain.NewMoney(0, spec.OpenCostBasis.Currency().String())
		base.Status = StatusComplete
		base.Reason = ReasonNone
		base.MarketValue = known(zero)
		base.UnrealizedPnL = known(zero)
		base.GrossPnL = known(spec.GrossRealizedPnL)
		return finalizePosition(base)
	}
	if mark == nil {
		return finalizePosition(base)
	}
	copy := *mark
	base.Mark = &copy
	if mark.InstrumentID != spec.InstrumentID || mark.MarketChecksum.IsZero() {
		base.Reason = ReasonRevisionConflict
		return finalizePosition(base)
	}
	if mark.PriceType != LastTradedPrice {
		base.Status, base.Reason = StatusUnavailable, ReasonInvalidPrice
		return finalizePosition(base)
	}
	if mark.Price.IsZeroValue() || mark.Price.MinorUnits() <= 0 {
		base.Reason = ReasonInvalidPrice
		return finalizePosition(base)
	}
	if mark.Price.Currency() != spec.OpenCostBasis.Currency() {
		base.Reason = ReasonCurrencyMismatch
		return finalizePosition(base)
	}
	if mark.ExchangeTime.After(now.Add(policy.ClockSkewTolerance)) || mark.IngestedAt.After(now.Add(policy.ClockSkewTolerance)) {
		base.Reason = ReasonClockSkew
		return finalizePosition(base)
	}
	if mark.ExchangeTime.After(mark.IngestedAt.Add(policy.ClockSkewTolerance)) {
		base.Reason = ReasonClockSkew
		return finalizePosition(base)
	}
	status, reason := StatusComplete, ReasonNone
	if mark.Readiness != readiness.StateReady || now.Sub(mark.ExchangeTime) > policy.MaximumExchangeAge || now.Sub(mark.IngestedAt) > policy.MaximumIngestionAge {
		status, reason = StatusStale, ReasonStaleMark
	}
	quantity := spec.NetQuantity.Int64()
	if quantity == math.MinInt64 {
		return PositionValuation{}, errors.Join(ErrInvalid, domain.ErrMoneyOverflow)
	}
	if quantity < 0 {
		quantity = -quantity
	}
	if mark.Price.MinorUnits() > math.MaxInt64/quantity {
		return PositionValuation{}, domain.ErrMoneyOverflow
	}
	marketMinor := mark.Price.MinorUnits() * quantity
	market, _ := domain.NewMoney(marketMinor, mark.Price.Currency().String())
	var unrealMinor int64
	if spec.NetQuantity > 0 {
		if marketMinor < math.MinInt64+spec.OpenCostBasis.MinorUnits() {
			return PositionValuation{}, domain.ErrMoneyOverflow
		}
		unrealMinor = marketMinor - spec.OpenCostBasis.MinorUnits()
	} else {
		if spec.OpenCostBasis.MinorUnits() < math.MinInt64+marketMinor {
			return PositionValuation{}, domain.ErrMoneyOverflow
		}
		unrealMinor = spec.OpenCostBasis.MinorUnits() - marketMinor
	}
	unreal, _ := domain.NewMoney(unrealMinor, mark.Price.Currency().String())
	gross, err := spec.GrossRealizedPnL.Add(unreal)
	if err != nil {
		return PositionValuation{}, err
	}
	base.Status = status
	base.Reason = reason
	base.MarketValue = known(market)
	base.UnrealizedPnL = known(unreal)
	base.GrossPnL = known(gross)
	return finalizePosition(base)
}

func Aggregate(portfolioID portfoliomodel.PortfolioID, revision uint64, valuations []PositionValuation, generatedAt time.Time) (PortfolioFinancialSnapshot, error) {
	if portfolioID.IsZero() || revision == 0 || generatedAt.IsZero() || len(valuations) > MaximumPositionsPerPortfolio {
		return PortfolioFinancialSnapshot{}, ErrInvalid
	}
	ordered := append([]PositionValuation(nil), valuations...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].PositionID.String() < ordered[j].PositionID.String() })
	var currency domain.Currency
	var realized, unreal, long, short domain.Money
	status := StatusComplete
	reasons := map[Reason]struct{}{}
	result := PortfolioFinancialSnapshot{PortfolioID: portfolioID, Revision: revision, PositionCount: len(ordered), GeneratedAt: generatedAt, PortfolioEquity: unavailable(ReasonCapitalUnavailable)}
	for i, v := range ordered {
		if v.PortfolioID != portfolioID || v.Checksum.IsZero() || (i > 0 && ordered[i-1].PositionID == v.PositionID) {
			return PortfolioFinancialSnapshot{}, ErrInvalid
		}
		if currency == "" {
			currency = v.RealizedPnL.Currency()
			realized, _ = domain.NewMoney(0, currency.String())
			unreal, _ = domain.NewMoney(0, currency.String())
			long, _ = domain.NewMoney(0, currency.String())
			short, _ = domain.NewMoney(0, currency.String())
		}
		if v.RealizedPnL.Currency() != currency {
			return PortfolioFinancialSnapshot{}, domain.ErrCurrencyMismatch
		}
		var err error
		realized, err = realized.Add(v.RealizedPnL)
		if err != nil {
			return PortfolioFinancialSnapshot{}, err
		}
		src := SourceRevision{v.PositionID, v.PositionRevision, v.PositionChecksum, v.Checksum, accountingmodel.StateChecksum{}}
		if v.Mark != nil {
			src.MarketChecksum = v.Mark.MarketChecksum
		}
		result.Sources = append(result.Sources, src)
		if v.NetQuantity != 0 {
			result.OpenPositionCount++
		}
		switch v.Status {
		case StatusComplete:
			if v.NetQuantity != 0 {
				result.ValuedCount++
			}
		case StatusStale:
			result.ValuedCount++
			result.StaleCount++
			if status == StatusComplete {
				status = StatusStale
			}
			reasons[v.Reason] = struct{}{}
		default:
			result.MissingCount++
			status = StatusPartial
			reasons[v.Reason] = struct{}{}
		}
		if v.UnrealizedPnL.Known() {
			unreal, err = unreal.Add(v.UnrealizedPnL.Value)
			if err != nil {
				return PortfolioFinancialSnapshot{}, err
			}
			if v.NetQuantity > 0 {
				long, err = long.Add(v.MarketValue.Value)
			} else if v.NetQuantity < 0 {
				short, err = short.Add(v.MarketValue.Value)
			}
			if err != nil {
				return PortfolioFinancialSnapshot{}, err
			}
		}
	}
	if currency == "" {
		return PortfolioFinancialSnapshot{}, ErrInvalid
	}
	result.Currency = currency
	result.Status = status
	if result.OpenPositionCount > 0 && result.ValuedCount == 0 {
		result.Status = StatusUnavailable
	}
	for reason := range reasons {
		result.Reasons = append(result.Reasons, reason)
	}
	result.RealizedPnL = known(realized)
	if result.Status == StatusComplete || result.Status == StatusStale {
		total, err := realized.Add(unreal)
		if err != nil {
			return PortfolioFinancialSnapshot{}, err
		}
		gross, err := long.Add(short)
		if err != nil {
			return PortfolioFinancialSnapshot{}, err
		}
		negativeShort, _ := domain.NewMoney(-short.MinorUnits(), currency.String())
		net, err := long.Add(negativeShort)
		if err != nil {
			return PortfolioFinancialSnapshot{}, err
		}
		result.UnrealizedPnL = known(unreal)
		result.TotalPnL = known(total)
		result.LongExposure = known(long)
		result.ShortExposure = known(short)
		result.GrossExposure = known(gross)
		result.NetExposure = known(net)
	} else {
		result.UnrealizedPnL = unavailable()
		result.TotalPnL = unavailable()
		result.LongExposure = unavailable()
		result.ShortExposure = unavailable()
		result.GrossExposure = unavailable()
		result.NetExposure = unavailable()
	}
	return finalizeSnapshot(result)
}
