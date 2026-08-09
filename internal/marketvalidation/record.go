// Package marketvalidation defines deterministic, non-authoritative evidence
// for PAPER/SHADOW market-validation sessions. It grants no trading authority.
package marketvalidation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const RecordSchemaVersion = "market-validation-day/v1"

var ErrInvalidRecord = errors.New("invalid market-validation record")

type Status string

const (
	StatusValid              Status = "VALID"
	StatusValidWithIncidents Status = "VALID_WITH_INCIDENTS"
	StatusInvalid            Status = "INVALID"
)

type Scope string

const (
	ScopeOperationsOnly Scope = "OPERATIONS_ONLY"
	ScopeFullPipeline   Scope = "FULL_PIPELINE"
)

type EvidenceReference struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Versions struct {
	CalendarVersion       string `json:"calendar_version"`
	MappingVersion        string `json:"mapping_version"`
	WatchlistVersion      string `json:"watchlist_version"`
	StrategyVersion       string `json:"strategy_version"`
	PortfolioConfigHash   string `json:"portfolio_configuration_hash"`
	RiskConfigurationHash string `json:"risk_configuration_hash"`
}

type Operations struct {
	StartupResult                string    `json:"startup_result"`
	ReadyAt                      time.Time `json:"ready_at"`
	ReadinessIncidents           int64     `json:"readiness_incidents"`
	Restarts                     int64     `json:"restarts"`
	UnexplainedRestarts          int64     `json:"unexplained_restarts"`
	ShutdownResult               string    `json:"shutdown_result"`
	CleanCheckpoint              bool      `json:"clean_checkpoint"`
	CalendarAndSessionVerified   bool      `json:"calendar_and_session_verified"`
	KillSwitchVerified           bool      `json:"kill_switch_verified"`
	MandatoryEvidenceComplete    bool      `json:"mandatory_evidence_complete"`
	LiveBrokerMutationObserved   bool      `json:"live_broker_mutation_observed"`
	DuplicateAuthoritativeEffect int64     `json:"duplicate_authoritative_effects"`
	CriticalIncidents            int64     `json:"critical_incidents"`
	ManualInterventions          []string  `json:"manual_interventions"`
}

type Trading struct {
	StrategiesActive int64 `json:"strategies_active"`
	Proposals        int64 `json:"proposals"`
	Approved         int64 `json:"approved"`
	Modified         int64 `json:"modified"`
	Rejected         int64 `json:"rejected"`
	PaperExecutions  int64 `json:"paper_executions"`
	ShadowExecutions int64 `json:"shadow_executions"`
	Fills            int64 `json:"fills"`
}

type Money struct {
	Availability string `json:"availability"`
	Minor        int64  `json:"minor,omitempty"`
	Currency     string `json:"currency,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type Financial struct {
	Status        string        `json:"status"`
	RealizedPnL   Money         `json:"realized_pnl"`
	UnrealizedPnL Money         `json:"unrealized_pnl"`
	TotalPnL      Money         `json:"total_pnl"`
	MaxDrawdown   Money         `json:"max_drawdown"`
	ByStrategy    []StrategyPnL `json:"by_strategy"`
}

type StrategyPnL struct {
	StrategyID string `json:"strategy_id"`
	Status     string `json:"status"`
	TotalPnL   Money  `json:"total_pnl"`
}

type Execution struct {
	PartialFills             int64    `json:"partial_fills"`
	Unknown                  int64    `json:"unknown"`
	ReconciliationMismatches int64    `json:"reconciliation_mismatches"`
	AssumedSlippageBPS       int32    `json:"assumed_slippage_bps"`
	SlippageModel            string   `json:"slippage_model"`
	Anomalies                []string `json:"anomalies"`
}

type MarketData struct {
	Disconnects           int64 `json:"disconnects"`
	Reconnects            int64 `json:"reconnects"`
	StaleDurationSeconds  int64 `json:"stale_duration_seconds"`
	MissingDataIncidents  int64 `json:"missing_data_incidents"`
	MaximumGapSeconds     int64 `json:"maximum_gap_seconds"`
	ReadinessAvailability int32 `json:"readiness_availability_bps"`
}

type CAS struct {
	Expected          bool     `json:"expected"`
	RegimesObserved   []string `json:"regimes_observed"`
	RestrictedSignals int64    `json:"restricted_signals"`
	BlockedExposure   int64    `json:"blocked_new_exposure"`
	EvidenceRecords   int64    `json:"evidence_records"`
	Anomalies         []string `json:"anomalies"`
}

type Notifications struct {
	TelegramEnabled   bool  `json:"telegram_enabled"`
	DeliverySuccesses int64 `json:"delivery_successes"`
	DeliveryFailures  int64 `json:"delivery_failures"`
	TerminalFailures  int64 `json:"terminal_failures"`
}

type Controls struct {
	KillSwitchEvents     int64 `json:"kill_switch_events"`
	CircuitBreakerEvents int64 `json:"circuit_breaker_events"`
}

type Record struct {
	SchemaVersion string              `json:"schema_version"`
	Checksum      string              `json:"checksum"`
	Date          string              `json:"date"`
	Mode          string              `json:"mode"`
	Scope         Scope               `json:"scope"`
	ReleaseCommit string              `json:"release_commit"`
	Versions      Versions            `json:"versions"`
	Operations    Operations          `json:"operations"`
	Trading       Trading             `json:"trading"`
	Financial     Financial           `json:"financial"`
	Execution     Execution           `json:"execution"`
	MarketData    MarketData          `json:"market_data"`
	CAS           CAS                 `json:"cas"`
	Notifications Notifications       `json:"notifications"`
	Controls      Controls            `json:"controls"`
	FinalStatus   Status              `json:"final_status"`
	StatusReasons []string            `json:"status_reasons"`
	Evidence      []EvidenceReference `json:"evidence"`
}

// Finalize validates evidence, derives validity without considering P&L, and
// adds a deterministic checksum. Callers cannot assert VALID directly.
func Finalize(input Record) (Record, error) {
	input.SchemaVersion = RecordSchemaVersion
	input.Checksum = ""
	input.FinalStatus = ""
	input.StatusReasons = nil
	if err := validateShape(input); err != nil {
		return Record{}, err
	}
	normalize(&input)
	reasons, incidents := classify(input)
	if len(reasons) > 0 {
		input.FinalStatus = StatusInvalid
		input.StatusReasons = reasons
	} else if incidents {
		input.FinalStatus = StatusValidWithIncidents
		input.StatusReasons = []string{"BOUNDED_INCIDENTS_RECORDED"}
	} else {
		input.FinalStatus = StatusValid
		input.StatusReasons = []string{}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return Record{}, ErrInvalidRecord
	}
	sum := sha256.Sum256(raw)
	input.Checksum = hex.EncodeToString(sum[:])
	return input, nil
}

func Verify(value Record) error {
	want := value.Checksum
	if !validDigest(want) {
		return ErrInvalidRecord
	}
	value.Checksum = ""
	value.FinalStatus = ""
	value.StatusReasons = nil
	rebuilt, err := Finalize(value)
	if err != nil || rebuilt.Checksum != want {
		return ErrInvalidRecord
	}
	return nil
}

func validateShape(value Record) error {
	if _, err := time.Parse("2006-01-02", value.Date); err != nil ||
		(value.Mode != "PAPER" && value.Mode != "SHADOW") ||
		(value.Scope != ScopeOperationsOnly && value.Scope != ScopeFullPipeline) ||
		!validCommit(value.ReleaseCommit) {
		return ErrInvalidRecord
	}
	versions := []string{value.Versions.CalendarVersion, value.Versions.MappingVersion,
		value.Versions.WatchlistVersion, value.Versions.StrategyVersion,
		value.Versions.PortfolioConfigHash, value.Versions.RiskConfigurationHash}
	for _, item := range versions {
		if strings.TrimSpace(item) == "" {
			return ErrInvalidRecord
		}
	}
	if value.Operations.StartupResult == "" || value.Operations.ShutdownResult == "" ||
		value.Operations.ReadyAt.IsZero() || value.MarketData.ReadinessAvailability < 0 ||
		value.MarketData.ReadinessAvailability > 10000 || len(value.Evidence) == 0 {
		return ErrInvalidRecord
	}
	for _, n := range nonnegative(value) {
		if n < 0 {
			return ErrInvalidRecord
		}
	}
	for _, item := range value.Evidence {
		clean := filepath.Clean(item.Path)
		lower := strings.ToLower(item.Kind + "|" + item.Path)
		if strings.TrimSpace(item.Kind) == "" || filepath.IsAbs(item.Path) || clean == "." ||
			strings.HasPrefix(clean, "..") || !validDigest(item.SHA256) ||
			strings.Contains(lower, "credential") || strings.Contains(lower, "secret") ||
			strings.Contains(lower, "access_token") || strings.Contains(lower, "api_key") {
			return ErrInvalidRecord
		}
	}
	for _, money := range []Money{value.Financial.RealizedPnL, value.Financial.UnrealizedPnL, value.Financial.TotalPnL, value.Financial.MaxDrawdown} {
		if !validMoney(money) {
			return ErrInvalidRecord
		}
	}
	seenStrategies := map[string]bool{}
	for _, item := range value.Financial.ByStrategy {
		if strings.TrimSpace(item.StrategyID) == "" || seenStrategies[item.StrategyID] ||
			(item.Status != "COMPLETE" && item.Status != "INCOMPLETE") || !validMoney(item.TotalPnL) {
			return ErrInvalidRecord
		}
		seenStrategies[item.StrategyID] = true
	}
	if value.Execution.AssumedSlippageBPS < 0 || value.Execution.AssumedSlippageBPS > 10000 ||
		strings.TrimSpace(value.Execution.SlippageModel) == "" {
		return ErrInvalidRecord
	}
	return nil
}

func validMoney(money Money) bool {
	if money.Availability == "KNOWN" {
		return strings.TrimSpace(money.Currency) != ""
	}
	return money.Availability == "UNAVAILABLE" && strings.TrimSpace(money.Reason) != ""
}

func nonnegative(v Record) []int64 {
	return []int64{v.Operations.ReadinessIncidents, v.Operations.Restarts, v.Operations.UnexplainedRestarts,
		v.Operations.DuplicateAuthoritativeEffect, v.Operations.CriticalIncidents,
		v.Trading.StrategiesActive, v.Trading.Proposals, v.Trading.Approved, v.Trading.Modified,
		v.Trading.Rejected, v.Trading.PaperExecutions, v.Trading.ShadowExecutions, v.Trading.Fills,
		v.Execution.PartialFills, v.Execution.Unknown, v.Execution.ReconciliationMismatches,
		v.MarketData.Disconnects, v.MarketData.Reconnects, v.MarketData.StaleDurationSeconds,
		v.MarketData.MissingDataIncidents, v.MarketData.MaximumGapSeconds, v.CAS.RestrictedSignals,
		v.CAS.BlockedExposure, v.CAS.EvidenceRecords, v.Notifications.DeliverySuccesses,
		v.Notifications.DeliveryFailures, v.Notifications.TerminalFailures,
		v.Controls.KillSwitchEvents, v.Controls.CircuitBreakerEvents}
}

func normalize(value *Record) {
	value.Operations.ReadyAt = value.Operations.ReadyAt.UTC()
	sort.Strings(value.Operations.ManualInterventions)
	sort.Strings(value.Execution.Anomalies)
	sort.Slice(value.Financial.ByStrategy, func(i, j int) bool {
		return value.Financial.ByStrategy[i].StrategyID < value.Financial.ByStrategy[j].StrategyID
	})
	sort.Strings(value.CAS.RegimesObserved)
	sort.Strings(value.CAS.Anomalies)
	sort.Slice(value.Evidence, func(i, j int) bool {
		return value.Evidence[i].Kind+"|"+value.Evidence[i].Path < value.Evidence[j].Kind+"|"+value.Evidence[j].Path
	})
}

func classify(value Record) ([]string, bool) {
	reasons := make([]string, 0)
	add := func(condition bool, reason string) {
		if condition {
			reasons = append(reasons, reason)
		}
	}
	add(value.Operations.StartupResult != "PASS", "STARTUP_NOT_CLEAN")
	add(value.Operations.ShutdownResult != "PASS", "SHUTDOWN_NOT_CLEAN")
	add(!value.Operations.CleanCheckpoint, "CLEAN_CHECKPOINT_MISSING")
	add(!value.Operations.CalendarAndSessionVerified, "SESSION_STATE_UNVERIFIED")
	add(!value.Operations.KillSwitchVerified, "KILL_SWITCH_UNVERIFIED")
	add(!value.Operations.MandatoryEvidenceComplete, "MANDATORY_EVIDENCE_MISSING")
	add(value.Operations.LiveBrokerMutationObserved, "LIVE_BROKER_MUTATION_OBSERVED")
	add(value.Operations.DuplicateAuthoritativeEffect > 0, "DUPLICATE_AUTHORITATIVE_EFFECT")
	add(value.Operations.UnexplainedRestarts > 0, "UNEXPLAINED_RESTART")
	add(value.Operations.CriticalIncidents > 0, "CRITICAL_INCIDENT")
	add(value.Execution.Unknown > 0, "UNRESOLVED_UNKNOWN")
	add(value.Execution.ReconciliationMismatches > 0, "RECONCILIATION_MISMATCH")
	add(value.MarketData.ReadinessAvailability < 9950, "READINESS_BELOW_99_5_PERCENT")
	add(value.MarketData.MaximumGapSeconds > 60, "MARKET_DATA_GAP_EXCEEDED")
	if value.Scope == ScopeFullPipeline {
		add(value.Trading.StrategiesActive == 0, "NO_ACTIVE_PRODUCTION_CANDIDATE")
		add(value.Financial.Status != "COMPLETE" || value.Financial.TotalPnL.Availability != "KNOWN", "FINAL_FINANCIAL_STATE_INCOMPLETE")
	}
	if value.CAS.Expected {
		seen := make(map[string]bool, len(value.CAS.RegimesObserved))
		for _, regime := range value.CAS.RegimesObserved {
			seen[regime] = true
		}
		add(value.CAS.EvidenceRecords == 0 || !seen["PRE_CAS"] || !seen["CAS_ACTIVE"] || !seen["POST_CAS"], "CAS_EVIDENCE_INCOMPLETE")
	}
	sort.Strings(reasons)
	incidents := value.Operations.ReadinessIncidents > 0 || value.Operations.Restarts > 0 ||
		len(value.Operations.ManualInterventions) > 0 || value.MarketData.Disconnects > 0 ||
		value.MarketData.MissingDataIncidents > 0 || value.Notifications.DeliveryFailures > 0 ||
		value.Notifications.TerminalFailures > 0 || len(value.Execution.Anomalies) > 0 ||
		len(value.CAS.Anomalies) > 0 || value.Controls.CircuitBreakerEvents > 0
	return reasons, incidents
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func Marshal(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal market-validation evidence: %w", err)
	}
	return append(raw, '\n'), nil
}
