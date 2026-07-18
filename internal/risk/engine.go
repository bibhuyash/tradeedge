package risk

import (
	"context"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

type RiskEngine interface {
	Validate(ctx context.Context, allocation domain.Allocation) (domain.RiskDecision, error)
}
