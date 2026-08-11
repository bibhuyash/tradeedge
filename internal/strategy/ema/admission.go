package ema

import strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"

type PositionState string

const (
	PositionUnknown  PositionState = "UNKNOWN"
	PositionFlat     PositionState = "FLAT"
	PositionOpen     PositionState = "OPEN"
	PositionConflict PositionState = "CONFLICT"
)

type Effect string

const (
	EffectIncrease Effect = "INCREASE"
	EffectReduce   Effect = "REDUCE"
)

// AdmissionInput contains only provider-neutral authoritative gate evidence.
// EvaluateAdmission is pure; central portfolio/risk policy remains downstream.
type AdmissionInput struct {
	Enabled                   bool
	ExecutionMappingAvailable bool
	MarketDataFresh           bool
	MarketDataReady           bool
	SessionRegime             string
	CASRestricted             bool
	InitialCASRevision        string
	CurrentCASRevision        string
	Position                  PositionState
	Effect                    Effect
	CooldownActive            bool
	StopNewExposure           bool
	RiskCircuitOpen           bool
}

type AdmissionDecision struct {
	Allowed bool
	Reason  strategymodel.NoActionReason
}

func EvaluateAdmission(input AdmissionInput) AdmissionDecision {
	if !input.Enabled {
		return blocked(strategymodel.NoActionDisabled)
	}
	if !input.ExecutionMappingAvailable {
		return blocked(strategymodel.NoActionMappingUnavailable)
	}
	if !input.MarketDataFresh || !input.MarketDataReady {
		return blocked(strategymodel.NoActionStaleMarketData)
	}
	if input.InitialCASRevision == "" || input.CurrentCASRevision == "" || input.InitialCASRevision != input.CurrentCASRevision {
		return blocked(strategymodel.NoActionAuthoritativeConflict)
	}
	if input.CASRestricted {
		return blocked(strategymodel.NoActionCASRestricted)
	}
	if input.SessionRegime != "NORMAL_TRADING" && !(input.Effect == EffectReduce && input.SessionRegime == "EOD_CLOSE") {
		return blocked(strategymodel.NoActionSessionNotAllowed)
	}
	if input.Position == PositionUnknown || input.Position == PositionConflict {
		return blocked(strategymodel.NoActionAuthoritativeConflict)
	}
	if input.Effect == EffectIncrease && input.Position == PositionOpen {
		return blocked(strategymodel.NoActionPositionAlreadyOpen)
	}
	if input.Effect == EffectReduce && input.Position != PositionOpen {
		return blocked(strategymodel.NoActionAuthoritativeConflict)
	}
	if input.Effect == EffectIncrease && input.CooldownActive {
		return blocked(strategymodel.NoActionCooldownActive)
	}
	if input.RiskCircuitOpen {
		return blocked(strategymodel.NoActionAuthoritativeConflict)
	}
	if input.Effect == EffectIncrease && input.StopNewExposure {
		return blocked(strategymodel.NoActionAuthoritativeConflict)
	}
	if input.Effect != EffectIncrease && input.Effect != EffectReduce {
		return blocked(strategymodel.NoActionAuthoritativeConflict)
	}
	return AdmissionDecision{Allowed: true}
}

func blocked(reason strategymodel.NoActionReason) AdmissionDecision {
	return AdmissionDecision{Reason: reason}
}
