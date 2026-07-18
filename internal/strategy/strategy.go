package strategy

import (
	"context"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

type Strategy interface {
	ID() domain.StrategyID
	Evaluate(ctx context.Context, event model.Event) ([]domain.Signal, error)
}
