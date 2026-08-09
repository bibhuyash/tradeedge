// Package replay deterministically rebuilds Phase 6 financial snapshots from verified frames.
package replay

import (
	"context"
	"errors"
	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	"github.com/bibhuyash/tradeedge/internal/accounting/valuation"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	"sort"
	"time"
)

var ErrInvalidReplay = errors.New("invalid financial replay")

type Frame struct {
	PortfolioID portfoliomodel.PortfolioID
	Positions   []accountingmodel.PositionSnapshot
	Marks       map[accountingmodel.PositionID]valuation.MarkPrice
	LogicalTime time.Time
}
type Engine struct{ policy valuation.Policy }

func New(policy valuation.Policy) (*Engine, error) {
	if policy.Validate() != nil {
		return nil, ErrInvalidReplay
	}
	return &Engine{policy}, nil
}
func (e *Engine) Replay(ctx context.Context, startRevision uint64, frames []Frame) ([]valuation.PortfolioFinancialSnapshot, error) {
	result := make([]valuation.PortfolioFinancialSnapshot, 0, len(frames))
	revision := startRevision
	for _, frame := range frames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if frame.PortfolioID.IsZero() || frame.LogicalTime.IsZero() || len(frame.Positions) == 0 {
			return nil, ErrInvalidReplay
		}
		positions := append([]accountingmodel.PositionSnapshot(nil), frame.Positions...)
		sort.Slice(positions, func(i, j int) bool { return positions[i].ID().String() < positions[j].ID().String() })
		values := make([]valuation.PositionValuation, 0, len(positions))
		for _, position := range positions {
			var mark *valuation.MarkPrice
			if item, ok := frame.Marks[position.ID()]; ok {
				copy := item
				mark = &copy
			}
			value, err := valuation.EvaluatePosition(position, mark, frame.LogicalTime, e.policy)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		revision++
		snapshot, err := valuation.Aggregate(frame.PortfolioID, revision, values, frame.LogicalTime)
		if err != nil {
			return nil, err
		}
		result = append(result, snapshot)
	}
	return result, nil
}
