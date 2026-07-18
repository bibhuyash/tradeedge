package strategy

import (
	"context"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

type Strategy interface {
	ID() domain.StrategyID
	Evaluate(ctx context.Context, event domain.MarketEvent) ([]domain.Signal, error)
}
