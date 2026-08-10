package marketvalidation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"
)

const (
	Day0EvidenceSchemaVersion = "market-validation-day0-evidence/v1"
	Day0GateSchemaVersion     = "market-validation-day0-gate/v1"
	Day1GateSchemaVersion     = "market-validation-day1-gate/v1"
)

// Day0Evidence is an operator-completed, non-secret summary of an observation
// session. The gate intentionally requires explicit evidence for every M2
// operational concern and independently enforces the zero-activity invariant.
type Day0Evidence struct {
	SchemaVersion         string    `json:"schema_version"`
	TradingDate           string    `json:"trading_date"`
	AuthorizationChecksum string    `json:"authorization_checksum"`
	CollectedAt           time.Time `json:"collected_at"`
	SessionVerified       bool      `json:"session_verified"`
	MarketDataVerified    bool      `json:"market_data_verified"`
	ReadinessVerified     bool      `json:"readiness_verified"`
	WebSocketVerified     bool      `json:"websocket_verified"`
	MappingsVerified      bool      `json:"mappings_verified"`
	CASVerified           bool      `json:"cas_verified"`
	TelegramVerified      bool      `json:"telegram_verified"`
	CheckpointsVerified   bool      `json:"checkpoints_verified"`
	EODDrainVerified      bool      `json:"eod_drain_verified"`
	ShutdownVerified      bool      `json:"shutdown_verified"`
	ReadinessBasisPoints  int       `json:"readiness_basis_points"`
	MaximumDataGapSeconds int       `json:"maximum_data_gap_seconds"`
	ActiveStrategies      int64     `json:"active_strategies"`
	Proposals             int64     `json:"proposals"`
	Orders                int64     `json:"orders"`
	Fills                 int64     `json:"fills"`
	RealBrokerMutations   int64     `json:"real_broker_mutations"`
	LiveTradingAuthorized bool      `json:"live_trading_authorized"`
}

type GateReport struct {
	SchemaVersion           string    `json:"schema_version"`
	TradingDate             string    `json:"trading_date"`
	AuthorizationChecksum   string    `json:"authorization_checksum"`
	EvidenceSHA256          string    `json:"evidence_sha256"`
	CheckedAt               time.Time `json:"checked_at"`
	Passed                  bool      `json:"passed"`
	Reasons                 []string  `json:"reasons"`
	Strategy                string    `json:"strategy"`
	ExecutionClassification string    `json:"execution_classification"`
	LiveTradingAuthorized   bool      `json:"live_trading_authorized"`
}

func DecodeDay0Evidence(raw []byte) (Day0Evidence, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value Day0Evidence
	if decoder.Decode(&value) != nil || !errors.Is(decoder.Decode(&struct{}{}), io.EOF) {
		return Day0Evidence{}, ErrInvalidRecord
	}
	return value, nil
}

func EvaluateDay0(raw []byte, authorization AuthorizationManifest, checkedAt time.Time) (GateReport, error) {
	evidence, err := DecodeDay0Evidence(raw)
	if err != nil {
		return GateReport{}, err
	}
	report := GateReport{SchemaVersion: Day0GateSchemaVersion, TradingDate: evidence.TradingDate, AuthorizationChecksum: evidence.AuthorizationChecksum, CheckedAt: checkedAt.UTC(), Strategy: "NONE", ExecutionClassification: "OPERATIONAL_PAPER_PNL", LiveTradingAuthorized: false}
	sum := sha256.Sum256(raw)
	report.EvidenceSHA256 = hex.EncodeToString(sum[:])
	if evidence.SchemaVersion != Day0EvidenceSchemaVersion || evidence.TradingDate != authorization.TradingDate || evidence.AuthorizationChecksum != authorization.Checksum || authorization.Scope != ScopeOperationsOnly {
		report.Reasons = append(report.Reasons, "AUTHORIZATION_MISMATCH")
	}
	if evidence.CollectedAt.IsZero() || evidence.LiveTradingAuthorized || authorization.LiveTradingAuthorized {
		report.Reasons = append(report.Reasons, "UNSAFE_EVIDENCE")
	}
	checks := []struct {
		passed bool
		reason string
	}{
		{evidence.SessionVerified, "SESSION_EVIDENCE_MISSING"}, {evidence.MarketDataVerified, "MARKET_DATA_EVIDENCE_MISSING"},
		{evidence.ReadinessVerified, "READINESS_EVIDENCE_MISSING"}, {evidence.WebSocketVerified, "WEBSOCKET_EVIDENCE_MISSING"},
		{evidence.MappingsVerified, "MAPPING_EVIDENCE_MISSING"}, {evidence.CASVerified, "CAS_EVIDENCE_MISSING"},
		{evidence.TelegramVerified, "TELEGRAM_EVIDENCE_MISSING"}, {evidence.CheckpointsVerified, "CHECKPOINT_EVIDENCE_MISSING"},
		{evidence.EODDrainVerified, "EOD_DRAIN_EVIDENCE_MISSING"}, {evidence.ShutdownVerified, "SHUTDOWN_EVIDENCE_MISSING"},
	}
	for _, check := range checks {
		if !check.passed {
			report.Reasons = append(report.Reasons, check.reason)
		}
	}
	if evidence.ReadinessBasisPoints < 9950 {
		report.Reasons = append(report.Reasons, "READINESS_BELOW_THRESHOLD")
	}
	if evidence.MaximumDataGapSeconds < 0 || evidence.MaximumDataGapSeconds > 60 {
		report.Reasons = append(report.Reasons, "MARKET_DATA_GAP_EXCEEDED")
	}
	if evidence.ActiveStrategies != 0 {
		report.Reasons = append(report.Reasons, "ACTIVE_STRATEGY_OBSERVED")
	}
	if evidence.Proposals != 0 {
		report.Reasons = append(report.Reasons, "PROPOSAL_OBSERVED")
	}
	if evidence.Orders != 0 {
		report.Reasons = append(report.Reasons, "ORDER_OBSERVED")
	}
	if evidence.Fills != 0 {
		report.Reasons = append(report.Reasons, "FILL_OBSERVED")
	}
	if evidence.RealBrokerMutations != 0 {
		report.Reasons = append(report.Reasons, "REAL_BROKER_MUTATION_OBSERVED")
	}
	sort.Strings(report.Reasons)
	report.Passed = len(report.Reasons) == 0
	return report, nil
}

func EvaluateDay1(day0 GateReport, authorization AuthorizationManifest, checkedAt time.Time) GateReport {
	report := GateReport{SchemaVersion: Day1GateSchemaVersion, TradingDate: authorization.TradingDate, AuthorizationChecksum: authorization.Checksum, CheckedAt: checkedAt.UTC(), Strategy: authorization.Strategy.Name, ExecutionClassification: "OPERATIONAL_PAPER_PNL", LiveTradingAuthorized: false}
	if day0.SchemaVersion != Day0GateSchemaVersion || !day0.Passed || day0.AuthorizationChecksum == "" || authorization.Artifacts.PrerequisiteDay0Gate == nil || authorization.Artifacts.PrerequisiteDay0Gate.Identity != day0.EvidenceSHA256 {
		report.Reasons = append(report.Reasons, "DAY0_PASS_REQUIRED")
	}
	if authorization.Scope != ScopeFullPipeline || !authorization.Strategy.Enabled || authorization.Strategy.Classification != "PRODUCTION_CANDIDATE" || strings.TrimSpace(authorization.Strategy.Name) == "" || authorization.Strategy.Name == "NONE" {
		report.Reasons = append(report.Reasons, ErrStrategyBlocked.Error())
	}
	if authorization.LiveTradingAuthorized {
		report.Reasons = append(report.Reasons, "LIVE_AUTHORIZATION_FORBIDDEN")
	}
	sort.Strings(report.Reasons)
	report.Passed = len(report.Reasons) == 0
	return report
}
