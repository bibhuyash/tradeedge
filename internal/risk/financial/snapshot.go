// Package financial is the Phase 3 consumer-owned boundary for Phase 6 financial state.
package financial

import (
	"errors"
	"time"

	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	"github.com/bibhuyash/tradeedge/internal/accounting/valuation"
	"github.com/bibhuyash/tradeedge/internal/domain"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

var ErrFinancialStateNotReady = errors.New("financial state not ready for risk")

type Position struct {
	PositionID   accountingmodel.PositionID
	InstrumentID domain.InstrumentID
	Quantity     int64
}
type Snapshot struct {
	SourceID, SourceChecksum                                                         accountingmodel.StateChecksum
	PortfolioID                                                                      portfoliomodel.PortfolioID
	Status                                                                           valuation.Status
	AsOf                                                                             time.Time
	RealizedPnL, UnrealizedPnL, TotalPnL, GrossExposure, LongExposure, ShortExposure valuation.MoneyValue
	Positions                                                                        []Position
}
type Provider interface {
	FinancialSnapshot(portfoliomodel.PortfolioID) (Snapshot, error)
}

func From(source valuation.PortfolioFinancialSnapshot, values []valuation.PositionValuation) (Snapshot, error) {
	if source.Checksum.IsZero() || len(values) != source.PositionCount {
		return Snapshot{}, valuation.ErrInvalid
	}
	result := Snapshot{SourceID: source.ID, SourceChecksum: source.Checksum, PortfolioID: source.PortfolioID, Status: source.Status, AsOf: source.GeneratedAt, RealizedPnL: source.RealizedPnL, UnrealizedPnL: source.UnrealizedPnL, TotalPnL: source.TotalPnL, GrossExposure: source.GrossExposure, LongExposure: source.LongExposure, ShortExposure: source.ShortExposure}
	for _, value := range values {
		if value.PortfolioID != source.PortfolioID {
			return Snapshot{}, valuation.ErrInvalid
		}
		result.Positions = append(result.Positions, Position{value.PositionID, value.InstrumentID, value.NetQuantity})
	}
	return result, nil
}
func (value Snapshot) RequireValuation() error {
	if value.Status != valuation.StatusComplete || !value.UnrealizedPnL.Known() || !value.TotalPnL.Known() || !value.GrossExposure.Known() {
		return ErrFinancialStateNotReady
	}
	return nil
}
