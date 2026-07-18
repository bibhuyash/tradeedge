// Package movingaverage contains a non-production engineering fixture used to
// validate the strategy framework. It makes no profitability claim.
package movingaverage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

const Classification = "NON_PRODUCTION_ENGINEERING_FIXTURE"

var ErrInvalidConfiguration = errors.New("invalid moving-average fixture configuration")

type Configuration struct {
	ShortWindow int   `json:"short_window"`
	LongWindow  int   `json:"long_window"`
	SizingBPS   int32 `json:"sizing_bps"`
}

type runtimeState struct {
	Evaluations  uint64 `json:"evaluations"`
	LastRelation int8   `json:"last_relation"`
}

type Strategy struct {
	descriptor strategymodel.Descriptor
	maximum    int
}

func New(instrument domain.InstrumentID, interval marketmodel.CandleInterval, maximum int) (*Strategy, error) {
	if instrument.IsZero() || maximum < 2 || maximum > strategymodel.MaximumLookback {
		return nil, ErrInvalidConfiguration
	}
	definitionID, _ := strategymodel.NewDefinitionID("moving-average-crossover-engineering-fixture")
	subscriptions, err := strategymodel.NewSubscriptionSpec(
		strategymodel.SubscriptionSingleStream,
		[]strategymodel.InputSubscription{{
			Role: "primary", InstrumentID: instrument, Interval: interval,
			Required: true, Trigger: true, Lookback: maximum,
		}},
	)
	if err != nil {
		return nil, err
	}
	descriptor, err := strategymodel.NewDescriptor(strategymodel.VersionManifest{
		DefinitionID: definitionID, ImplementationVersion: "engineering-fixture/v1",
		InputContractVersion:       "candle-frame/v1",
		ConfigurationSchemaVersion: "moving-average-config/v1",
		StateSchemaVersion:         "moving-average-state/v1",
		ResultSchemaVersion:        "strategy-result/v1", ProposalSchemaVersion: "proposal/v1",
	}, subscriptions)
	if err != nil {
		return nil, err
	}
	return &Strategy{descriptor: descriptor, maximum: maximum}, nil
}

func (strategy *Strategy) Descriptor() strategymodel.Descriptor { return strategy.descriptor }

func (strategy *Strategy) ValidateConfiguration(configuration strategymodel.StrategyConfiguration) error {
	_, err := strategy.configuration(configuration)
	return err
}

func (strategy *Strategy) InitialState(
	configuration strategymodel.StrategyConfiguration,
) (strategymodel.StrategyRuntimeState, error) {
	if _, err := strategy.configuration(configuration); err != nil {
		return strategymodel.StrategyRuntimeState{}, err
	}
	return encodeState(runtimeState{})
}

func (strategy *Strategy) Evaluate(
	ctx context.Context,
	input strategymodel.EvaluationContext,
) (strategymodel.EvaluationResult, error) {
	if err := ctx.Err(); err != nil {
		return strategymodel.EvaluationResult{}, err
	}
	configuration, err := strategy.configuration(input.Configuration)
	if err != nil {
		return strategymodel.EvaluationResult{}, err
	}
	var state runtimeState
	if err := json.Unmarshal(input.PriorState.CanonicalJSON(), &state); err != nil ||
		state.LastRelation < -1 || state.LastRelation > 1 {
		return strategymodel.EvaluationResult{}, strategymodel.ErrInvalidRuntimeState
	}
	series := input.Frame.Series()
	if len(series) != 1 || len(series[0].Candles) == 0 {
		return strategymodel.EvaluationResult{}, strategymodel.ErrInvalidCandleFrame
	}
	candles := series[0].Candles
	if state.Evaluations == math.MaxUint64 {
		return strategymodel.EvaluationResult{}, errors.New("moving-average evaluation counter overflow")
	}
	state.Evaluations++
	if len(candles) < configuration.LongWindow {
		next, _ := encodeState(state)
		return strategymodel.NewNoActionResult(
			next, strategymodel.NoActionInsufficientHistory, "long-window warm-up incomplete",
		)
	}
	shortAverage, err := average(candles[len(candles)-configuration.ShortWindow:])
	if err != nil {
		return strategymodel.EvaluationResult{}, err
	}
	longAverage, err := average(candles[len(candles)-configuration.LongWindow:])
	if err != nil {
		return strategymodel.EvaluationResult{}, err
	}
	relation := int8(0)
	if shortAverage > longAverage {
		relation = 1
	} else if shortAverage < longAverage {
		relation = -1
	}
	crossover := state.LastRelation != 0 && relation != 0 && relation != state.LastRelation
	state.LastRelation = relation
	next, err := encodeState(state)
	if err != nil {
		return strategymodel.EvaluationResult{}, err
	}
	if !crossover {
		return strategymodel.NewNoActionResult(
			next, strategymodel.NoActionConditionsNotMet, "no new moving-average crossover",
		)
	}
	side, rationale := domain.SideBuy, "BULLISH_CROSSOVER"
	if relation < 0 {
		side, rationale = domain.SideSell, "BEARISH_CROSSOVER"
	}
	last := candles[len(candles)-1]
	draft := strategymodel.ProposalDraft{
		SchemaVersion: "proposal/v1",
		Legs: []strategymodel.ProposalLeg{{
			InstrumentID: series[0].InstrumentID, Side: side, Ratio: 1,
			ReferencePrice: last.Close(), MaxDeviationBPS: 100,
		}},
		Sizing: strategymodel.SizingIntent{
			Kind: strategymodel.SizingStrategyBudgetBPS, ValueBPS: configuration.SizingBPS,
		},
		ValidFrom: input.LogicalTime, ExpiresAt: input.LogicalTime.Add(5 * time.Minute),
		RationaleCode: rationale,
		Explanation:   "integer completed-candle moving-average crossover engineering fixture",
		Evidence: []strategymodel.Evidence{{
			Code: rationale, SourceEventIDs: []marketmodel.EventID{last.ID()},
			Value: shortAverage - longAverage, Unit: "PRICE_MINOR_UNITS",
			Explanation: fmt.Sprintf("short=%d long=%d", shortAverage, longAverage),
		}},
		ExitPolicyReference: "engineering-fixture-exit/v1",
	}
	return strategymodel.NewTradeProposalResult(next, draft)
}

func (strategy *Strategy) configuration(
	value strategymodel.StrategyConfiguration,
) (Configuration, error) {
	if value.SchemaVersion() != strategy.descriptor.Manifest.ConfigurationSchemaVersion {
		return Configuration{}, ErrInvalidConfiguration
	}
	var result Configuration
	if err := json.Unmarshal(value.CanonicalJSON(), &result); err != nil ||
		result.ShortWindow <= 0 || result.LongWindow <= 0 ||
		result.ShortWindow >= result.LongWindow || result.LongWindow > strategy.maximum ||
		result.SizingBPS <= 0 || result.SizingBPS > 10000 {
		return Configuration{}, ErrInvalidConfiguration
	}
	return result, nil
}

func average(candles []marketmodel.CompletedCandleEvent) (int64, error) {
	var total int64
	for _, candle := range candles {
		value := candle.Close().MinorUnits()
		if value > 0 && total > math.MaxInt64-value {
			return 0, errors.New("moving-average integer overflow")
		}
		total += value
	}
	return total / int64(len(candles)), nil
}

func encodeState(value runtimeState) (strategymodel.StrategyRuntimeState, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return strategymodel.StrategyRuntimeState{}, err
	}
	return strategymodel.NewStrategyRuntimeState("moving-average-state/v1", raw)
}
