package backtest

import (
	"context"
	"errors"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/research/features"
)

const ControlStrategyV1 = "CONTROL_STRATEGY_V1"

type ControlStrategy struct{ direction domain.Side }

func NewControlStrategy(direction domain.Side) (ControlStrategy, error) {
	if direction != domain.SideBuy && direction != domain.SideSell {
		return ControlStrategy{}, ErrInvalidConfiguration
	}
	return ControlStrategy{direction: direction}, nil
}
func (ControlStrategy) Version() string { return ControlStrategyV1 }
func (s ControlStrategy) Evaluate(ctx context.Context, frame features.FeatureFrame, portfolio PortfolioView) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	ids := frame.SourceObservationIDs()
	kind := DecisionNoAction
	reason := "CONTROL_WAIT"
	if portfolio.Position == nil && len(ids) == 2 {
		if s.direction == domain.SideBuy {
			kind = DecisionLong
		} else {
			kind = DecisionShort
		}
		reason = "CONTROL_SECOND_OBSERVATION_ENTRY"
	} else if portfolio.Position != nil && len(ids) >= 3 {
		kind = DecisionExit
		reason = "CONTROL_NEXT_EVALUATION_EXIT"
	}
	decision := Decision{Kind: kind, Timestamp: frame.AsOf(), InstrumentID: frame.Instrument().ID(), Reason: reason}
	if err := validateDecision(decision, frame); err != nil {
		return Decision{}, err
	}
	return decision, nil
}
func validateDecision(value Decision, frame features.FeatureFrame) error {
	if value.Timestamp.IsZero() || !value.Timestamp.Equal(frame.AsOf()) || value.InstrumentID != frame.Instrument().ID() || value.Reason == "" {
		return ErrInvalidDecision
	}
	switch value.Kind {
	case DecisionNoAction, DecisionLong, DecisionShort, DecisionExit:
	default:
		return ErrInvalidDecision
	}
	if value.Score != nil && value.Score.Scale <= 0 {
		return errors.New("invalid decision score")
	}
	return nil
}
