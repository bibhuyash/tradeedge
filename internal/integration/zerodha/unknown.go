package zerodha

import (
	"context"

	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
)

type NonTerminalRepository interface {
	NonTerminalOrders(context.Context, int) ([]executionmodel.Order, error)
}
type RepositoryUnknownSource struct{ Repository NonTerminalRepository }

func (source RepositoryUnknownSource) UnknownCount(ctx context.Context) (int, error) {
	values, err := source.Repository.NonTerminalOrders(ctx, 1000)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, value := range values {
		if value.Spec().State == executionmodel.OrderUnknown {
			count++
		}
	}
	return count, nil
}

var _ UnknownSource = RepositoryUnknownSource{}
