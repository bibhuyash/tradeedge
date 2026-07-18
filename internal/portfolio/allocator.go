package portfolio

import (
	"context"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

type PortfolioAllocator interface {
	Allocate(ctx context.Context, request domain.AllocationRequest) (domain.Allocation, error)
}
