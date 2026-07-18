package strategy

import (
	"context"
	"testing"

	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

func TestDefinitionContractRemainsBrokerNeutral(t *testing.T) {
	t.Parallel()
	var _ Definition = fixtureDefinition{}
}

type fixtureDefinition struct{}

func (fixtureDefinition) Descriptor() strategymodel.Descriptor {
	return strategymodel.Descriptor{}
}
func (fixtureDefinition) ValidateConfiguration(strategymodel.StrategyConfiguration) error {
	return nil
}
func (fixtureDefinition) InitialState(
	strategymodel.StrategyConfiguration,
) (strategymodel.StrategyRuntimeState, error) {
	return strategymodel.StrategyRuntimeState{}, nil
}
func (fixtureDefinition) Evaluate(
	context.Context,
	strategymodel.EvaluationContext,
) (strategymodel.EvaluationResult, error) {
	return strategymodel.EvaluationResult{}, nil
}
