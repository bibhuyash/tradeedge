package execution

import (
	"context"
	"errors"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

var (
	ErrOrderNotFound         = errors.New("order not found")
	ErrClientRequestConflict = errors.New("client request ID was reused for a different order")
	ErrOrderIDConflict       = errors.New("generated order ID already exists")
	ErrInvalidOrderState     = errors.New("invalid order state transition")
)

// Broker is owned by the execution boundary. Strategies must never receive it.
type Broker interface {
	SubmitOrder(ctx context.Context, request domain.OrderRequest) (domain.Order, error)
	LookupOrder(ctx context.Context, id domain.OrderID) (domain.Order, error)
	CancelOrder(ctx context.Context, id domain.OrderID) (domain.Order, error)
	Positions(ctx context.Context, accountID domain.AccountID) ([]domain.Position, error)
}
