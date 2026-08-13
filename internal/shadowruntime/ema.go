package shadowruntime

import (
	"math"

	"github.com/bibhuyash/tradeedge/internal/qualification"
)

const (
	EMAPolicyVersion = "fixed-point-ema-half-away-from-zero/v1"
	EMAScale         = int64(1_000_000)
	WarmupRequired   = 50
)

type EMAState struct {
	Underlying     qualification.Underlying `json:"underlying"`
	Samples        int                      `json:"samples"`
	EMA20Scaled    int64                    `json:"ema20_scaled"`
	EMA50Scaled    int64                    `json:"ema50_scaled"`
	LastRelation   int8                     `json:"last_relation"`
	LastCandleTime int64                    `json:"last_candle_unix_nano"`
	LastSignal     qualification.Direction  `json:"last_signal,omitempty"`
	LastSignalID   string                   `json:"last_signal_id,omitempty"`
	LastSignalTime int64                    `json:"last_signal_unix_nano,omitempty"`
}

type EMASignal struct {
	Underlying  qualification.Underlying
	Direction   qualification.Direction
	SignalID    string
	AtUnixNano  int64
	SpotMinor   int64
	EMA20Scaled int64
	EMA50Scaled int64
}

func EvaluateEMA(state EMAState, candles []CandlePoint) (EMAState, *EMASignal, error) {
	if state.Underlying != qualification.NIFTY && state.Underlying != qualification.BANKNIFTY {
		return EMAState{}, nil, ErrInvalid
	}
	if len(candles) == 0 || len(candles) > MaximumCandles {
		return EMAState{}, nil, ErrInvalid
	}
	latest := candles[len(candles)-1]
	if state.LastCandleTime != 0 && latest.CloseTime.UnixNano() <= state.LastCandleTime {
		return state, nil, ErrDuplicate
	}
	state.Samples = len(candles)
	state.LastCandleTime = latest.CloseTime.UnixNano()
	if len(candles) < WarmupRequired {
		return state, nil, nil
	}
	fast, err := fixedEMA(candles, 20)
	if err != nil {
		return EMAState{}, nil, err
	}
	slow, err := fixedEMA(candles, 50)
	if err != nil {
		return EMAState{}, nil, err
	}
	state.EMA20Scaled, state.EMA50Scaled = fast, slow
	relation := int8(0)
	if fast > slow {
		relation = 1
	} else if fast < slow {
		relation = -1
	}
	previous := state.LastRelation
	state.LastRelation = relation
	if relation == 0 || previous == 0 || relation == previous {
		return state, nil, nil
	}
	direction := qualification.DirectionLong
	if relation < 0 {
		direction = qualification.DirectionExit
	}
	id := identity(string(state.Underlying), latest.EventID, directionString(direction), latest.CloseTime.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"))
	state.LastSignal, state.LastSignalID, state.LastSignalTime = direction, id, latest.CloseTime.UnixNano()
	return state, &EMASignal{Underlying: state.Underlying, Direction: direction, SignalID: id, AtUnixNano: latest.CloseTime.UnixNano(), SpotMinor: latest.CloseMinor, EMA20Scaled: fast, EMA50Scaled: slow}, nil
}

func fixedEMA(candles []CandlePoint, period int64) (int64, error) {
	if int64(len(candles)) < period || period <= 0 {
		return 0, ErrInvalid
	}
	var value int64
	for index, candle := range candles {
		if candle.CloseMinor <= 0 || candle.CloseMinor > math.MaxInt64/EMAScale {
			return 0, ErrInvalid
		}
		scaled := candle.CloseMinor * EMAScale
		if index == 0 {
			value = scaled
			continue
		}
		delta := scaled - value
		adjustment, err := roundedFraction(delta, 2, period+1)
		if err != nil {
			return 0, err
		}
		if (adjustment > 0 && value > math.MaxInt64-adjustment) || (adjustment < 0 && value < math.MinInt64-adjustment) {
			return 0, ErrInvalid
		}
		value += adjustment
	}
	return value, nil
}

func roundedFraction(value, numerator, denominator int64) (int64, error) {
	if denominator <= 0 || numerator <= 0 || (value > 0 && value > math.MaxInt64/numerator) || (value < 0 && value < math.MinInt64/numerator) {
		return 0, ErrInvalid
	}
	product := value * numerator
	quotient, remainder := product/denominator, product%denominator
	if remainder < 0 {
		remainder = -remainder
	}
	if remainder >= denominator-remainder {
		if product >= 0 {
			quotient++
		} else {
			quotient--
		}
	}
	return quotient, nil
}

func directionString(value qualification.Direction) string { return string(value) }
