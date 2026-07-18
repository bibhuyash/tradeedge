package paper

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/execution"
)

type Clock func() time.Time
type IDGenerator func() (domain.OrderID, error)

type Broker struct {
	mu              sync.RWMutex
	clock           Clock
	idGenerator     IDGenerator
	orders          map[domain.OrderID]domain.Order
	orderByClientID map[domain.ClientRequestID]domain.OrderID
}

var _ execution.Broker = (*Broker)(nil)

func New(clock Clock, idGenerator IDGenerator) (*Broker, error) {
	if clock == nil {
		return nil, errors.New("paper broker clock is required")
	}
	if idGenerator == nil {
		return nil, errors.New("paper broker ID generator is required")
	}
	return &Broker{
		clock:           clock,
		idGenerator:     idGenerator,
		orders:          make(map[domain.OrderID]domain.Order),
		orderByClientID: make(map[domain.ClientRequestID]domain.OrderID),
	}, nil
}

func (b *Broker) SubmitOrder(ctx context.Context, request domain.OrderRequest) (domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}
	if err := request.Validate(); err != nil {
		return domain.Order{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if existingID, found := b.orderByClientID[request.ClientRequestID]; found {
		existing := b.orders[existingID]
		if existing.Request != request {
			return domain.Order{}, execution.ErrClientRequestConflict
		}
		return existing, nil
	}

	id, err := b.idGenerator()
	if err != nil {
		return domain.Order{}, err
	}
	if id == "" {
		return domain.Order{}, domain.ErrInvalidID
	}
	if _, found := b.orders[id]; found {
		return domain.Order{}, execution.ErrOrderIDConflict
	}

	now := b.clock()
	order := domain.Order{
		ID:        id,
		Request:   request,
		State:     domain.OrderAcknowledged,
		CreatedAt: now,
		UpdatedAt: now,
	}
	b.orders[id] = order
	b.orderByClientID[request.ClientRequestID] = id
	return order, nil
}

func (b *Broker) LookupOrder(ctx context.Context, id domain.OrderID) (domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	order, found := b.orders[id]
	if !found {
		return domain.Order{}, execution.ErrOrderNotFound
	}
	return order, nil
}

func (b *Broker) CancelOrder(ctx context.Context, id domain.OrderID) (domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	order, found := b.orders[id]
	if !found {
		return domain.Order{}, execution.ErrOrderNotFound
	}
	switch order.State {
	case domain.OrderAcknowledged:
		order.State = domain.OrderCancelled
		order.UpdatedAt = b.clock()
		b.orders[id] = order
		return order, nil
	case domain.OrderCancelled:
		return order, nil
	default:
		return domain.Order{}, execution.ErrInvalidOrderState
	}
}

func (b *Broker) Positions(ctx context.Context, accountID domain.AccountID) ([]domain.Position, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if accountID == "" {
		return nil, domain.ErrInvalidID
	}
	// Phase 0 does not simulate fills, so it cannot create positions.
	return []domain.Position{}, nil
}
