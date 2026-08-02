package zerodha

import (
	"context"
	"sync"
)

type FakePlace struct {
	Response PlaceResponse
	Err      error
}

type FakeCancel struct {
	Response CancelResponse
	Err      error
}

type FakeOrderTransport struct {
	mu            sync.Mutex
	Places        []FakePlace
	Cancellations []FakeCancel
	OrderBook     []BrokerOrder
	TradeBook     []BrokerTrade
	placeAt       int
	cancelAt      int
	closed        bool
}

func (fake *FakeOrderTransport) Place(ctx context.Context, _ OrderRequest) (PlaceResponse, error) {
	if err := ctx.Err(); err != nil {
		return PlaceResponse{Delivery: DeliveryNotSent}, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.closed || fake.placeAt >= len(fake.Places) {
		return PlaceResponse{Delivery: DeliveryNotSent}, ErrUnavailable
	}
	value := fake.Places[fake.placeAt]
	fake.placeAt++
	return value.Response, value.Err
}

func (fake *FakeOrderTransport) Cancel(ctx context.Context, _, _ string) (CancelResponse, error) {
	if err := ctx.Err(); err != nil {
		return CancelResponse{Delivery: DeliveryNotSent}, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.closed || fake.cancelAt >= len(fake.Cancellations) {
		return CancelResponse{Delivery: DeliveryNotSent}, ErrUnavailable
	}
	value := fake.Cancellations[fake.cancelAt]
	fake.cancelAt++
	return value.Response, value.Err
}

func (fake *FakeOrderTransport) Orders(ctx context.Context) ([]BrokerOrder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.closed {
		return nil, ErrStopped
	}
	return append([]BrokerOrder(nil), fake.OrderBook...), nil
}

func (fake *FakeOrderTransport) Trades(ctx context.Context) ([]BrokerTrade, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.closed {
		return nil, ErrStopped
	}
	return append([]BrokerTrade(nil), fake.TradeBook...), nil
}

func (fake *FakeOrderTransport) Close() {
	fake.mu.Lock()
	fake.closed = true
	fake.mu.Unlock()
}

func (fake *FakeOrderTransport) Counts() (places, cancels int) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.placeAt, fake.cancelAt
}
