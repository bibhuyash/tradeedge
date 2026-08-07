package zerodha

import (
	"context"

	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

type QuoteObserver interface {
	ObserveQuote(context.Context, marketmodel.QuoteEvent) error
}

// StreamRouter forwards canonical quotes to simulated execution. Order updates
// are admitted only when an exact M2 correlation already exists; unrelated
// account activity is ignored and can never mutate paper OMS state.
type StreamRouter struct {
	Quotes    QuoteObserver
	Execution *ExecutionAdapter
}

func (router StreamRouter) ObserveQuote(ctx context.Context, quote marketmodel.QuoteEvent) error {
	if router.Quotes == nil {
		return nil
	}
	return router.Quotes.ObserveQuote(ctx, quote)
}
func (router StreamRouter) ObserveOrder(ctx context.Context, order BrokerOrder, trades []BrokerTrade) error {
	if router.Execution == nil {
		return nil
	}
	router.Execution.state.mu.Lock()
	_, known := router.Execution.state.tags[order.Tag]
	router.Execution.state.mu.Unlock()
	if !known {
		return nil
	}
	return router.Execution.IngestOrderUpdate(ctx, order, trades)
}

var _ StreamSink = StreamRouter{}
