package marketvalidation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

const ScorecardSchemaVersion = "market-validation-scorecard/v1"

type RecordSummary struct {
	Date     string `json:"date"`
	Mode     string `json:"mode"`
	Scope    Scope  `json:"scope"`
	Status   Status `json:"status"`
	Checksum string `json:"checksum"`
}

type StrategyPnLSummary struct {
	StrategyID       string `json:"strategy_id"`
	CompleteSessions int    `json:"complete_sessions"`
	TotalPnLMinor    int64  `json:"total_pnl_minor"`
	Currency         string `json:"currency"`
}

type Scorecard struct {
	SchemaVersion             string               `json:"schema_version"`
	Checksum                  string               `json:"checksum"`
	Sessions                  int                  `json:"sessions"`
	ValidSessions             int                  `json:"valid_sessions"`
	ValidWithIncidents        int                  `json:"valid_with_incidents"`
	InvalidSessions           int                  `json:"invalid_sessions"`
	ValidPaperSessions        int                  `json:"valid_paper_sessions"`
	ValidShadowSessions       int                  `json:"valid_shadow_sessions"`
	CleanStartupBPS           int32                `json:"clean_startup_bps"`
	CleanShutdownBPS          int32                `json:"clean_shutdown_bps"`
	AverageReadinessBPS       int32                `json:"average_readiness_bps"`
	Restarts                  int64                `json:"restarts"`
	ManualInterventions       int64                `json:"manual_interventions"`
	Disconnects               int64                `json:"market_data_disconnects"`
	StaleDurationSeconds      int64                `json:"market_data_stale_duration_seconds"`
	MissingDataIncidents      int64                `json:"missing_data_incidents"`
	Proposals                 int64                `json:"proposals"`
	Approved                  int64                `json:"approved"`
	Modified                  int64                `json:"modified"`
	Rejected                  int64                `json:"rejected"`
	SimulatedExecutions       int64                `json:"simulated_executions"`
	Fills                     int64                `json:"fills"`
	PartialFills              int64                `json:"partial_fills"`
	Unknown                   int64                `json:"unknown"`
	ReconciliationMismatches  int64                `json:"reconciliation_mismatches"`
	FillRateBPS               int32                `json:"fill_rate_bps"`
	PartialFillRateBPS        int32                `json:"partial_fill_rate_bps"`
	UnknownFrequencyBPS       int32                `json:"unknown_frequency_bps"`
	AverageAssumedSlippageBPS int32                `json:"average_assumed_slippage_bps"`
	CompletePnLSessions       int                  `json:"complete_pnl_sessions"`
	CompleteTotalPnLMinor     int64                `json:"complete_total_pnl_minor"`
	PnLCurrency               string               `json:"pnl_currency,omitempty"`
	CASObservedSessions       int                  `json:"cas_observed_sessions"`
	CASEvidenceRecords        int64                `json:"cas_evidence_records"`
	TelegramFailures          int64                `json:"telegram_failures"`
	KillSwitchEvents          int64                `json:"kill_switch_events"`
	CircuitBreakerEvents      int64                `json:"circuit_breaker_events"`
	LastFiveConsecutiveValid  bool                 `json:"last_five_consecutive_valid"`
	ExecutionSampleSufficient bool                 `json:"execution_sample_sufficient"`
	MinimumProgramComplete    bool                 `json:"minimum_program_complete"`
	TargetProgramComplete     bool                 `json:"target_program_complete"`
	LiveTradingAuthorized     bool                 `json:"live_trading_authorized"`
	DecisionGate              string               `json:"decision_gate"`
	PnLByStrategy             []StrategyPnLSummary `json:"pnl_by_strategy"`
	Records                   []RecordSummary      `json:"records"`
}

func BuildScorecard(records []Record) (Scorecard, error) {
	if len(records) == 0 || len(records) > 20 {
		return Scorecard{}, ErrInvalidRecord
	}
	values := append([]Record(nil), records...)
	for _, value := range values {
		if Verify(value) != nil {
			return Scorecard{}, ErrInvalidRecord
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Date == values[j].Date {
			return values[i].Mode < values[j].Mode
		}
		return values[i].Date < values[j].Date
	})
	result := Scorecard{SchemaVersion: ScorecardSchemaVersion, Sessions: len(values), LiveTradingAuthorized: false}
	seen := map[string]bool{}
	cleanStart, cleanStop, readiness := 0, 0, int64(0)
	slippage := int64(0)
	strategyPnL := map[string]StrategyPnLSummary{}
	for _, value := range values {
		key := value.Date + "|" + value.Mode
		if seen[key] {
			return Scorecard{}, ErrInvalidRecord
		}
		seen[key] = true
		result.Records = append(result.Records, RecordSummary{Date: value.Date, Mode: value.Mode, Scope: value.Scope, Status: value.FinalStatus, Checksum: value.Checksum})
		switch value.FinalStatus {
		case StatusValid:
			result.ValidSessions++
			if value.Mode == "PAPER" {
				result.ValidPaperSessions++
			} else {
				result.ValidShadowSessions++
			}
		case StatusValidWithIncidents:
			result.ValidWithIncidents++
		case StatusInvalid:
			result.InvalidSessions++
		default:
			return Scorecard{}, ErrInvalidRecord
		}
		if value.Operations.StartupResult == "PASS" {
			cleanStart++
		}
		if value.Operations.ShutdownResult == "PASS" {
			cleanStop++
		}
		readiness += int64(value.MarketData.ReadinessAvailability)
		result.Restarts += value.Operations.Restarts
		result.ManualInterventions += int64(len(value.Operations.ManualInterventions))
		result.Disconnects += value.MarketData.Disconnects
		result.StaleDurationSeconds += value.MarketData.StaleDurationSeconds
		result.MissingDataIncidents += value.MarketData.MissingDataIncidents
		result.Proposals += value.Trading.Proposals
		result.Approved += value.Trading.Approved
		result.Modified += value.Trading.Modified
		result.Rejected += value.Trading.Rejected
		result.SimulatedExecutions += value.Trading.PaperExecutions + value.Trading.ShadowExecutions
		result.Fills += value.Trading.Fills
		result.PartialFills += value.Execution.PartialFills
		result.Unknown += value.Execution.Unknown
		result.ReconciliationMismatches += value.Execution.ReconciliationMismatches
		slippage += int64(value.Execution.AssumedSlippageBPS)
		if value.Financial.Status == "COMPLETE" && value.Financial.TotalPnL.Availability == "KNOWN" {
			if result.PnLCurrency != "" && result.PnLCurrency != value.Financial.TotalPnL.Currency {
				return Scorecard{}, ErrInvalidRecord
			}
			result.PnLCurrency = value.Financial.TotalPnL.Currency
			result.CompletePnLSessions++
			result.CompleteTotalPnLMinor += value.Financial.TotalPnL.Minor
		}
		for _, item := range value.Financial.ByStrategy {
			if item.Status != "COMPLETE" || item.TotalPnL.Availability != "KNOWN" {
				continue
			}
			current := strategyPnL[item.StrategyID]
			if current.Currency != "" && current.Currency != item.TotalPnL.Currency {
				return Scorecard{}, ErrInvalidRecord
			}
			current.StrategyID, current.Currency = item.StrategyID, item.TotalPnL.Currency
			current.CompleteSessions++
			current.TotalPnLMinor += item.TotalPnL.Minor
			strategyPnL[item.StrategyID] = current
		}
		if value.CAS.Expected {
			result.CASObservedSessions++
		}
		result.CASEvidenceRecords += value.CAS.EvidenceRecords
		result.TelegramFailures += value.Notifications.DeliveryFailures + value.Notifications.TerminalFailures
		result.KillSwitchEvents += value.Controls.KillSwitchEvents
		result.CircuitBreakerEvents += value.Controls.CircuitBreakerEvents
	}
	result.CleanStartupBPS = int32(cleanStart * 10000 / len(values))
	result.CleanShutdownBPS = int32(cleanStop * 10000 / len(values))
	result.AverageReadinessBPS = int32(readiness / int64(len(values)))
	result.AverageAssumedSlippageBPS = int32(slippage / int64(len(values)))
	if result.SimulatedExecutions > 0 {
		result.FillRateBPS = int32(min64(result.Fills*10000/result.SimulatedExecutions, 10000))
		result.PartialFillRateBPS = int32(min64(result.PartialFills*10000/result.SimulatedExecutions, 10000))
		result.UnknownFrequencyBPS = int32(min64(result.Unknown*10000/result.SimulatedExecutions, 10000))
	}
	for _, item := range strategyPnL {
		result.PnLByStrategy = append(result.PnLByStrategy, item)
	}
	sort.Slice(result.PnLByStrategy, func(i, j int) bool {
		return result.PnLByStrategy[i].StrategyID < result.PnLByStrategy[j].StrategyID
	})
	if len(values) >= 5 {
		result.LastFiveConsecutiveValid = true
		for _, value := range values[len(values)-5:] {
			if value.FinalStatus != StatusValid {
				result.LastFiveConsecutiveValid = false
			}
		}
	}
	result.ExecutionSampleSufficient = result.SimulatedExecutions >= 30
	result.MinimumProgramComplete = result.ValidSessions >= 10 && result.ValidPaperSessions >= 5 && result.ValidShadowSessions >= 5 && result.LastFiveConsecutiveValid
	validBPS := (result.ValidSessions * 10000) / len(values)
	result.TargetProgramComplete = len(values) >= 20 && validBPS >= 9000 && result.MinimumProgramComplete
	switch {
	case !result.MinimumProgramComplete:
		result.DecisionGate = "INSUFFICIENT_VALID_SESSIONS"
	case result.InvalidSessions > 0:
		result.DecisionGate = "OPERATIONAL_HARDENING_REVIEW"
	case !result.ExecutionSampleSufficient:
		result.DecisionGate = "EXECUTION_SAMPLE_INSUFFICIENT"
	case result.Proposals == 0:
		result.DecisionGate = "STRATEGY_EVIDENCE_UNAVAILABLE"
	default:
		result.DecisionGate = "SEPARATE_POST_VALIDATION_REVIEW_REQUIRED"
	}
	result.Checksum = ""
	raw, _ := json.Marshal(result)
	sum := sha256.Sum256(raw)
	result.Checksum = hex.EncodeToString(sum[:])
	return result, nil
}

func min64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
