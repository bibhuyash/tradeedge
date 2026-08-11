// Package ema implements TradeEdge's single Phase 8 production-candidate
// strategy. It is provider-neutral and can only emit advisory proposals.
package ema

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

const (
	Classification        = "PRODUCTION_CANDIDATE"
	DefinitionName        = "nifty-ema-crossover-paper"
	ImplementationVersion = "nifty-ema-crossover/v1"
	ConfigurationSchema   = "nifty-ema-crossover-config/v1"
	StateSchema           = "nifty-ema-crossover-state/v1"
	CalculationPolicy     = "fixed-point-ema-half-away-from-zero/v1"
	Scale                 = int64(1_000_000)
)

var ErrInvalidConfiguration = errors.New("invalid production EMA configuration")

type Configuration struct {
	StrategyID                    string   `json:"strategy_id"`
	Version                       string   `json:"version"`
	Enabled                       bool     `json:"enabled"`
	SignalInstrument              string   `json:"signal_instrument"`
	ExecutionInstrument           string   `json:"execution_instrument"`
	Timeframe                     string   `json:"timeframe"`
	FastPeriod                    int      `json:"fast_ema_period"`
	SlowPeriod                    int      `json:"slow_ema_period"`
	MinimumWarmupSamples          int      `json:"minimum_warmup_samples"`
	FreshnessSeconds              int64    `json:"freshness_threshold_seconds"`
	AllowedSessionRegimes         []string `json:"allowed_session_regimes"`
	CooldownSeconds               int64    `json:"cooldown_seconds"`
	MaxSimultaneousPositionIntent int      `json:"max_simultaneous_position_intent"`
	QuantityLots                  int64    `json:"quantity_lots"`
	SizingBPS                     int32    `json:"sizing_bps"`
	ExitRule                      string   `json:"exit_rule"`
	CalculationPolicy             string   `json:"calculation_policy"`
}

type runtimeState struct {
	Evaluations        uint64 `json:"evaluations"`
	CanonicalSamples   uint64 `json:"canonical_samples"`
	FastEMA            int64  `json:"fast_ema_scaled"`
	SlowEMA            int64  `json:"slow_ema_scaled"`
	LastRelation       int8   `json:"last_relation"`
	LastSignalUnixNano int64  `json:"last_signal_unix_nano"`
}

type Strategy struct {
	descriptor strategymodel.Descriptor
	signal     domain.InstrumentID
	execution  domain.InstrumentID
	maximum    int
	freshness  time.Duration
}

func New(signal, execution domain.InstrumentID, interval marketmodel.CandleInterval, maximum int, freshness time.Duration) (*Strategy, error) {
	if signal.IsZero() || execution.IsZero() || signal == execution || maximum < 2 || maximum > strategymodel.MaximumLookback || freshness <= 0 {
		return nil, ErrInvalidConfiguration
	}
	definitionID, _ := strategymodel.NewDefinitionID(DefinitionName)
	subscriptions, err := strategymodel.NewSubscriptionSpec(strategymodel.SubscriptionLatestCompleted, []strategymodel.InputSubscription{
		{Role: "execution", InstrumentID: execution, Interval: interval, Required: true, Trigger: false, Lookback: 1, MaximumAge: freshness},
		{Role: "signal", InstrumentID: signal, Interval: interval, Required: true, Trigger: true, Lookback: maximum, MaximumAge: freshness},
	})
	if err != nil {
		return nil, err
	}
	descriptor, err := strategymodel.NewDescriptor(strategymodel.VersionManifest{
		DefinitionID: definitionID, ImplementationVersion: ImplementationVersion,
		InputContractVersion: "completed-candle-latest-frame/v1", ConfigurationSchemaVersion: ConfigurationSchema,
		StateSchemaVersion: StateSchema, ResultSchemaVersion: "strategy-result/v1", ProposalSchemaVersion: "proposal/v1",
	}, subscriptions)
	if err != nil {
		return nil, err
	}
	return &Strategy{descriptor: descriptor, signal: signal, execution: execution, maximum: maximum, freshness: freshness}, nil
}

func (s *Strategy) Descriptor() strategymodel.Descriptor { return s.descriptor }
func (s *Strategy) ValidateConfiguration(value strategymodel.StrategyConfiguration) error {
	_, err := s.configuration(value)
	return err
}
func (s *Strategy) InitialState(value strategymodel.StrategyConfiguration) (strategymodel.StrategyRuntimeState, error) {
	if _, err := s.configuration(value); err != nil {
		return strategymodel.StrategyRuntimeState{}, err
	}
	return encodeState(runtimeState{})
}

func (s *Strategy) Evaluate(ctx context.Context, input strategymodel.EvaluationContext) (strategymodel.EvaluationResult, error) {
	if err := ctx.Err(); err != nil {
		return strategymodel.EvaluationResult{}, err
	}
	cfg, err := s.configuration(input.Configuration)
	if err != nil {
		return strategymodel.EvaluationResult{}, err
	}
	var state runtimeState
	if err := json.Unmarshal(input.PriorState.CanonicalJSON(), &state); err != nil || state.LastRelation < -1 || state.LastRelation > 1 {
		return strategymodel.EvaluationResult{}, strategymodel.ErrInvalidRuntimeState
	}
	if state.Evaluations == math.MaxUint64 {
		return strategymodel.EvaluationResult{}, errors.New("EMA evaluation counter overflow")
	}
	state.Evaluations++
	if !cfg.Enabled {
		return noAction(state, strategymodel.NoActionDisabled, "production candidate is disabled")
	}
	series := input.Frame.Series()
	var signal []marketmodel.CompletedCandleEvent
	var execution marketmodel.CompletedCandleEvent
	for _, item := range series {
		switch item.Role {
		case "signal":
			signal = item.Candles
		case "execution":
			if len(item.Candles) == 1 {
				execution = item.Candles[0]
			}
		}
	}
	if len(signal) == 0 || execution.ID().IsZero() {
		return strategymodel.EvaluationResult{}, strategymodel.ErrInvalidCandleFrame
	}
	state.CanonicalSamples = uint64(len(signal))
	if len(signal) < cfg.MinimumWarmupSamples || len(signal) < cfg.SlowPeriod {
		return noAction(state, strategymodel.NoActionInsufficientHistory, "EMA warmup is incomplete")
	}
	fast, err := calculate(signal, cfg.FastPeriod)
	if err != nil {
		return strategymodel.EvaluationResult{}, err
	}
	slow, err := calculate(signal, cfg.SlowPeriod)
	if err != nil {
		return strategymodel.EvaluationResult{}, err
	}
	state.FastEMA, state.SlowEMA = fast, slow
	relation := compare(fast, slow)
	previous := state.LastRelation
	state.LastRelation = relation
	if relation == 0 || previous == 0 || relation == previous {
		return noAction(state, strategymodel.NoActionNoCrossover, "no edge-triggered EMA crossover")
	}
	if relation > 0 && state.LastSignalUnixNano != 0 && input.LogicalTime.Sub(time.Unix(0, state.LastSignalUnixNano)) < time.Duration(cfg.CooldownSeconds)*time.Second {
		return noAction(state, strategymodel.NoActionCooldownActive, "strategy cooldown is active")
	}
	state.LastSignalUnixNano = input.LogicalTime.UnixNano()
	next, err := encodeState(state)
	if err != nil {
		return strategymodel.EvaluationResult{}, err
	}
	side, rationale := domain.SideBuy, "EMA_BULLISH_CROSSOVER"
	if relation < 0 {
		side, rationale = domain.SideSell, "EMA_BEARISH_EXIT"
	}
	latestSignal := signal[len(signal)-1]
	return strategymodel.NewTradeProposalResult(next, strategymodel.ProposalDraft{
		SchemaVersion: "proposal/v1",
		Legs:          []strategymodel.ProposalLeg{{InstrumentID: s.execution, Side: side, Ratio: uint32(cfg.QuantityLots), ReferencePrice: execution.Close(), MaxDeviationBPS: 100}},
		Sizing:        strategymodel.SizingIntent{Kind: strategymodel.SizingStrategyBudgetBPS, ValueBPS: cfg.SizingBPS},
		ValidFrom:     input.LogicalTime, ExpiresAt: input.LogicalTime.Add(time.Duration(cfg.FreshnessSeconds) * time.Second),
		RationaleCode: rationale, Explanation: "fixed-point EMA20/EMA50 crossover on canonical completed candles",
		Evidence: []strategymodel.Evidence{
			{Code: "FAST_EMA", SourceEventIDs: []marketmodel.EventID{latestSignal.ID()}, Value: fast, Unit: "PRICE_MINOR_UNITS_X1E6", Explanation: fmt.Sprintf("period=%d policy=%s", cfg.FastPeriod, CalculationPolicy)},
			{Code: "SLOW_EMA", SourceEventIDs: []marketmodel.EventID{latestSignal.ID()}, Value: slow, Unit: "PRICE_MINOR_UNITS_X1E6", Explanation: fmt.Sprintf("period=%d policy=%s", cfg.SlowPeriod, CalculationPolicy)},
		},
		RiskHints:           []strategymodel.RiskHint{{Code: "MAX_POSITION_INTENT", Value: int64(cfg.MaxSimultaneousPositionIntent), Unit: "COUNT"}, {Code: "QUANTITY_LOTS", Value: cfg.QuantityLots, Unit: "LOTS"}},
		ExitPolicyReference: "ema-bearish-crossover-and-eod/v1",
	})
}

func (s *Strategy) configuration(value strategymodel.StrategyConfiguration) (Configuration, error) {
	if value.SchemaVersion() != ConfigurationSchema {
		return Configuration{}, ErrInvalidConfiguration
	}
	var cfg Configuration
	decoder := json.NewDecoder(strings.NewReader(string(value.CanonicalJSON())))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || cfg.StrategyID != DefinitionName || cfg.Version != "1" ||
		cfg.SignalInstrument != s.signal.String() || cfg.ExecutionInstrument != s.execution.String() || cfg.Timeframe != "1m" ||
		cfg.FastPeriod != 20 || cfg.SlowPeriod != 50 || cfg.SlowPeriod > s.maximum || cfg.MinimumWarmupSamples < cfg.SlowPeriod ||
		time.Duration(cfg.FreshnessSeconds)*time.Second != s.freshness || cfg.CooldownSeconds < 0 || cfg.CooldownSeconds > 3600 || cfg.MaxSimultaneousPositionIntent != 1 ||
		cfg.QuantityLots != 1 || cfg.SizingBPS <= 0 || cfg.SizingBPS > 1000 || cfg.ExitRule != "BEARISH_CROSSOVER_OR_EOD_CLOSE" || cfg.CalculationPolicy != CalculationPolicy || len(cfg.AllowedSessionRegimes) == 0 {
		return Configuration{}, ErrInvalidConfiguration
	}
	allowed := append([]string(nil), cfg.AllowedSessionRegimes...)
	sort.Strings(allowed)
	if strings.Join(allowed, ",") != "NORMAL_TRADING" {
		return Configuration{}, ErrInvalidConfiguration
	}
	return cfg, nil
}

func calculate(candles []marketmodel.CompletedCandleEvent, period int) (int64, error) {
	if period <= 0 || len(candles) < period {
		return 0, ErrInvalidConfiguration
	}
	var ema int64
	for i, candle := range candles {
		price := candle.Close().MinorUnits()
		if price <= 0 || price > math.MaxInt64/Scale {
			return 0, errors.New("EMA fixed-point overflow")
		}
		scaled := price * Scale
		if i == 0 {
			ema = scaled
			continue
		}
		delta := scaled - ema
		adjustment, err := roundedFraction(delta, 2, int64(period+1))
		if err != nil {
			return 0, err
		}
		if (adjustment > 0 && ema > math.MaxInt64-adjustment) || (adjustment < 0 && ema < math.MinInt64-adjustment) {
			return 0, errors.New("EMA fixed-point overflow")
		}
		ema += adjustment
	}
	return ema, nil
}

func roundedFraction(value, numerator, denominator int64) (int64, error) {
	if denominator <= 0 || (value > 0 && value > math.MaxInt64/numerator) || (value < 0 && value < math.MinInt64/numerator) {
		return 0, errors.New("EMA fixed-point overflow")
	}
	product := value * numerator
	if product >= 0 {
		return (product + denominator/2) / denominator, nil
	}
	return (product - denominator/2) / denominator, nil
}
func compare(left, right int64) int8 {
	if left > right {
		return 1
	}
	if left < right {
		return -1
	}
	return 0
}
func noAction(state runtimeState, reason strategymodel.NoActionReason, explanation string) (strategymodel.EvaluationResult, error) {
	next, err := encodeState(state)
	if err != nil {
		return strategymodel.EvaluationResult{}, err
	}
	return strategymodel.NewNoActionResult(next, reason, explanation)
}
func encodeState(value runtimeState) (strategymodel.StrategyRuntimeState, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return strategymodel.StrategyRuntimeState{}, err
	}
	return strategymodel.NewStrategyRuntimeState(StateSchema, raw)
}
