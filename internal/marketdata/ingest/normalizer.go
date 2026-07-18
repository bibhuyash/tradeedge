package ingest

import (
	"context"
	"fmt"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

type Normalizer struct {
	Resolver marketdata.InstrumentResolver
	Calendar model.MarketCalendar
}

func (n Normalizer) Normalize(ctx context.Context, observation marketdata.Observation) (model.Event, error) {
	if n.Resolver == nil {
		return nil, fmt.Errorf("%w: resolver is required", marketdata.ErrInvalidObservation)
	}
	instrument, masterVersion, err := n.Resolver.ResolveProviderRef(
		ctx, observation.Provider, observation.ProviderToken, observation.ExchangeTime)
	if err != nil {
		return nil, fmt.Errorf("resolve provider instrument: %w", err)
	}
	currency, err := domain.NewCurrency(observation.Currency)
	if err != nil || currency != instrument.Currency() {
		return nil, model.ErrCurrencyMismatch
	}
	var session model.MarketSession
	if n.Calendar != nil {
		sessionLookupTime := observation.ExchangeTime
		if observation.Kind == marketdata.ObservationCandle {
			sessionLookupTime = observation.OpenTime
		}
		var active bool
		session, active, err = n.Calendar.SessionAt(ctx, instrument.Exchange(), sessionLookupTime)
		if err != nil {
			return nil, fmt.Errorf("resolve market session: %w", err)
		}
		if !active {
			return nil, model.ErrInvalidSession
		}
	}
	provenance := model.Provenance{
		Provider:       observation.Provider,
		ProviderToken:  observation.ProviderToken,
		MasterVersion:  masterVersion,
		SourceSequence: observation.SourceSequence,
		HasSequence:    observation.HasSequence,
	}
	switch observation.Kind {
	case marketdata.ObservationQuote:
		if n.Calendar != nil && !session.Contains(observation.ExchangeTime) {
			return nil, model.ErrInvalidSession
		}
		last, err := domain.NewPrice(observation.LastMinor, observation.Currency)
		if err != nil {
			return nil, fmt.Errorf("normalize quote price: %w", err)
		}
		bid, err := normalizeLevel(observation.BidMinor, observation.BidQuantity, observation.Currency)
		if err != nil {
			return nil, err
		}
		ask, err := normalizeLevel(observation.AskMinor, observation.AskQuantity, observation.Currency)
		if err != nil {
			return nil, err
		}
		event, err := model.NewQuoteEvent(model.QuoteSpec{
			InstrumentID: instrument.ID(),
			LastPrice:    last,
			BestBid:      bid,
			BestAsk:      ask,
			Volume:       observation.Volume,
			OpenInterest: observation.OpenInterest,
			ExchangeTime: observation.ExchangeTime,
			IngestedAt:   observation.IngestedAt,
			Provenance:   provenance,
		})
		if err != nil {
			return nil, fmt.Errorf("validate quote: %w", err)
		}
		if err := validateTickAlignment(event.LastPrice(), instrument.TickSize()); err != nil {
			return nil, err
		}
		for _, level := range []*model.BookLevel{event.BestBid(), event.BestAsk()} {
			if level != nil {
				if err := validateTickAlignment(level.Price, instrument.TickSize()); err != nil {
					return nil, err
				}
			}
		}
		return event, nil
	case marketdata.ObservationCandle:
		if n.Calendar != nil {
			if observation.OpenTime.Before(session.Open) || observation.CloseTime.After(session.Close) {
				return nil, model.ErrInvalidSession
			}
			duration, valid := observation.Interval.Duration()
			if !valid || (observation.Interval == model.Interval1Day &&
				(!observation.OpenTime.Equal(session.Open) || !observation.CloseTime.Equal(session.Close))) ||
				(observation.Interval != model.Interval1Day &&
					observation.OpenTime.Sub(session.Open)%duration != 0) {
				return nil, model.ErrInvalidSession
			}
		}
		open, err := domain.NewPrice(observation.OpenMinor, observation.Currency)
		if err != nil {
			return nil, err
		}
		high, err := domain.NewPrice(observation.HighMinor, observation.Currency)
		if err != nil {
			return nil, err
		}
		low, err := domain.NewPrice(observation.LowMinor, observation.Currency)
		if err != nil {
			return nil, err
		}
		closePrice, err := domain.NewPrice(observation.CloseMinor, observation.Currency)
		if err != nil {
			return nil, err
		}
		event, err := model.NewCompletedCandleEvent(model.CandleSpec{
			InstrumentID: instrument.ID(),
			Interval:     observation.Interval,
			OpenTime:     observation.OpenTime,
			CloseTime:    observation.CloseTime,
			Open:         open,
			High:         high,
			Low:          low,
			Close:        closePrice,
			Volume:       observation.Volume,
			EventCount:   observation.EventCount,
			OpenInterest: observation.OpenInterest,
			IngestedAt:   observation.IngestedAt,
			Provenance:   provenance,
		})
		if err != nil {
			return nil, fmt.Errorf("validate candle: %w", err)
		}
		for _, price := range []domain.Price{event.Open(), event.High(), event.Low(), event.Close()} {
			if err := validateTickAlignment(price, instrument.TickSize()); err != nil {
				return nil, err
			}
		}
		return event, nil
	default:
		return nil, marketdata.ErrInvalidObservation
	}
}

func validateTickAlignment(price, tick domain.Price) error {
	if tick.MinorUnits() <= 0 || price.MinorUnits()%tick.MinorUnits() != 0 {
		return marketdata.ErrInvalidObservation
	}
	return nil
}

func normalizeLevel(minor *int64, quantity int64, currency string) (*model.BookLevel, error) {
	if minor == nil {
		if quantity != 0 {
			return nil, marketdata.ErrInvalidObservation
		}
		return nil, nil
	}
	price, err := domain.NewPrice(*minor, currency)
	if err != nil {
		return nil, err
	}
	return &model.BookLevel{Price: price, Quantity: quantity}, nil
}
