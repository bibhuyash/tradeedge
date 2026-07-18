package strategy

import (
	"context"

	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

// Definition is a deterministic, broker-neutral strategy implementation.
// It can inspect only the synchronized input frame and immutable metadata
// supplied by the future runner. Trade proposals remain advisory and cannot
// bypass portfolio allocation, central risk validation, or execution.
type Definition interface {
	Descriptor() strategymodel.Descriptor
	ValidateConfiguration(strategymodel.StrategyConfiguration) error
	InitialState(strategymodel.StrategyConfiguration) (strategymodel.StrategyRuntimeState, error)
	Evaluate(context.Context, strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error)
}
