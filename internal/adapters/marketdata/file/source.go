package file

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/bibhuyash/tradeedge/internal/marketdata"
)

type Source struct {
	Path string
}

var _ marketdata.Source = Source{}

func (s Source) Stream(
	ctx context.Context,
	query marketdata.SourceQuery,
	sink marketdata.ObservationSink,
) error {
	if sink == nil {
		return marketdata.ErrInvalidObservation
	}
	input, err := os.Open(s.Path)
	if err != nil {
		return err
	}
	defer input.Close()

	var reader io.Reader = input
	if filepath.Ext(s.Path) == ".gz" {
		compressed, err := gzip.NewReader(input)
		if err != nil {
			return err
		}
		defer compressed.Close()
		reader = compressed
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	for position := int64(1); ; position++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var observation marketdata.Observation
		err := decoder.Decode(&observation)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		observation.SourcePosition = position
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
}
