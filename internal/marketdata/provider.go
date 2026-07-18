package marketdata

import (
	"context"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

type EventSink func(context.Context, domain.MarketEvent) error

type MarketDataProvider interface {
	Subscribe(ctx context.Context, instruments []domain.Instrument, sink EventSink) error
}
