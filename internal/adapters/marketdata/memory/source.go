package memory

import (
	"context"

	"github.com/bibhuyash/tradeedge/internal/marketdata"
)

type Source struct {
	Observations []marketdata.Observation
}

var _ marketdata.Source = Source{}

func (s Source) Stream(
	ctx context.Context,
	query marketdata.SourceQuery,
	sink marketdata.ObservationSink,
) error {
	for _, observation := range s.Observations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !query.Start.IsZero() && observation.ExchangeTime.Before(query.Start) {
			continue
		}
		if !query.End.IsZero() && !observation.ExchangeTime.Before(query.End) {
			continue
		}
		if err := sink(ctx, observation); err != nil {
			return err
		}
	}
	return nil
}
