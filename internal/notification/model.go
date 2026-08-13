// Package notification defines provider-neutral, non-authoritative operational
// events and bounded delivery contracts. Nothing in this package may influence
// trading decisions or state transitions.
package notification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const SchemaVersion = "phase-7-m2-operational-event/v1"

var ErrInvalid = errors.New("invalid operational notification value")

type Severity string

const (
	SeverityInfo     Severity = "INFO"
	SeverityWarning  Severity = "WARNING"
	SeverityCritical Severity = "CRITICAL"
)

type Category string

const (
	CategoryRuntime        Category = "RUNTIME"
	CategoryReadiness      Category = "READINESS"
	CategoryStrategy       Category = "STRATEGY"
	CategoryProposal       Category = "PROPOSAL"
	CategoryRisk           Category = "RISK"
	CategoryExecution      Category = "EXECUTION"
	CategoryReconciliation Category = "RECONCILIATION"
	CategoryControl        Category = "CONTROL"
	CategoryCAS            Category = "CAS"
	CategoryValuation      Category = "VALUATION"
	CategoryReporting      Category = "REPORTING"
)

type Kind string

const (
	KindRuntimeReady              Kind = "RUNTIME_READY"
	KindRuntimeDegraded           Kind = "RUNTIME_DEGRADED"
	KindRuntimeHalted             Kind = "RUNTIME_HALTED"
	KindReadinessLost             Kind = "READINESS_LOST"
	KindReadinessRestored         Kind = "READINESS_RESTORED"
	KindStrategyActivated         Kind = "STRATEGY_ACTIVATED"
	KindStrategyRestricted        Kind = "STRATEGY_RESTRICTED"
	KindStrategyHalted            Kind = "STRATEGY_HALTED"
	KindStrategyReady             Kind = "STRATEGY_READY"
	KindOptionSelected            Kind = "OPTION_SELECTED"
	KindEntryProposal             Kind = "ENTRY_PROPOSAL"
	KindExitProposal              Kind = "EXIT_PROPOSAL"
	KindProposalGenerated         Kind = "TRADE_PROPOSAL_GENERATED"
	KindRiskApproved              Kind = "RISK_APPROVED"
	KindRiskModified              Kind = "RISK_MODIFIED"
	KindRiskRejected              Kind = "RISK_REJECTED"
	KindPaperSubmitted            Kind = "PAPER_EXECUTION_SUBMITTED"
	KindPaperPartialFill          Kind = "PAPER_PARTIAL_FILL"
	KindPaperFill                 Kind = "PAPER_FILL"
	KindShadowTrade               Kind = "SHADOW_TRADE"
	KindShadowSignal              Kind = "SHADOW_SIGNAL"
	KindPositionUpdate            Kind = "POSITION_UPDATE"
	KindPositionClosed            Kind = "POSITION_CLOSED"
	KindValidationIncident        Kind = "VALIDATION_INCIDENT"
	KindShadowQualification       Kind = "SHADOW_QUALIFICATION"
	KindShadowQualificationResult Kind = "SHADOW_QUALIFICATION_RESULT"
	KindShadowSessionReady        Kind = "SHADOW_SESSION_READY"
	KindShadowSessionClosed       Kind = "SHADOW_SESSION_CLOSED"
	KindExecutionUnknown          Kind = "EXECUTION_UNKNOWN"
	KindReconciliationMismatch    Kind = "RECONCILIATION_MISMATCH"
	KindBrokerOnlyExposure        Kind = "BROKER_ONLY_EXPOSURE"
	KindKillSwitch                Kind = "KILL_SWITCH_ACTIVATED"
	KindCircuitBreaker            Kind = "CIRCUIT_BREAKER_ACTIVATED"
	KindPreCAS                    Kind = "PRE_CAS"
	KindCASActive                 Kind = "CAS_ACTIVE"
	KindPostCAS                   Kind = "POST_CAS"
	KindCASRestricted             Kind = "CAS_RESTRICTED"
	KindValuationPartial          Kind = "VALUATION_PARTIAL"
	KindValuationStale            Kind = "VALUATION_STALE"
	KindValuationUnavailable      Kind = "VALUATION_UNAVAILABLE"
	KindFinancialSnapshot         Kind = "FINANCIAL_SNAPSHOT"
	KindDailyPnLWarning           Kind = "DAILY_PNL_WARNING"
	KindEndOfDay                  Kind = "END_OF_DAY_SUMMARY"
)

type Details struct {
	Subject                string `json:"subject,omitempty"`
	State                  string `json:"state,omitempty"`
	Reason                 string `json:"reason,omitempty"`
	InstrumentID           string `json:"instrument_id,omitempty"`
	StrategyID             string `json:"strategy_id,omitempty"`
	PortfolioID            string `json:"portfolio_id,omitempty"`
	ReferenceID            string `json:"reference_id,omitempty"`
	Quantity               int64  `json:"quantity,omitempty"`
	PriceMinor             int64  `json:"price_minor,omitempty"`
	Currency               string `json:"currency,omitempty"`
	Count                  int64  `json:"count,omitempty"`
	ValuationStatus        string `json:"valuation_status,omitempty"`
	FinancialChecksum      string `json:"financial_checksum,omitempty"`
	RealizedAvailability   string `json:"realized_availability,omitempty"`
	RealizedMinor          int64  `json:"realized_minor,omitempty"`
	UnrealizedAvailability string `json:"unrealized_availability,omitempty"`
	UnrealizedMinor        int64  `json:"unrealized_minor,omitempty"`
	TotalAvailability      string `json:"total_availability,omitempty"`
	TotalMinor             int64  `json:"total_minor,omitempty"`
	CalendarVersion        string `json:"calendar_version,omitempty"`
	ConfigurationVersion   string `json:"configuration_version,omitempty"`
	ConfigurationChecksum  string `json:"configuration_checksum,omitempty"`
	FutureInstrumentID     string `json:"future_instrument_id,omitempty"`
	Expiry                 string `json:"expiry,omitempty"`
	OptionType             string `json:"option_type,omitempty"`
	StrikeMinor            int64  `json:"strike_minor,omitempty"`
	Underlying             string `json:"underlying,omitempty"`
	SpotMinor              int64  `json:"spot_minor,omitempty"`
	FutureMinor            int64  `json:"future_minor,omitempty"`
	BidMinor               int64  `json:"bid_minor,omitempty"`
	AskMinor               int64  `json:"ask_minor,omitempty"`
	LTPMinor               int64  `json:"ltp_minor,omitempty"`
	EMA20Scaled            int64  `json:"ema20_scaled,omitempty"`
	EMA50Scaled            int64  `json:"ema50_scaled,omitempty"`
	Regime                 string `json:"regime,omitempty"`
}

type Event struct {
	SchemaVersion string    `json:"schema_version"`
	ID            string    `json:"id"`
	Checksum      string    `json:"checksum"`
	SourceID      string    `json:"source_id"`
	TradingDate   string    `json:"trading_date"`
	OccurredAt    time.Time `json:"occurred_at"`
	Mode          string    `json:"mode"`
	Category      Category  `json:"category"`
	Kind          Kind      `json:"kind"`
	Severity      Severity  `json:"severity"`
	Details       Details   `json:"details"`
}

type EventSpec struct {
	SourceID, TradingDate, Mode string
	OccurredAt                  time.Time
	Category                    Category
	Kind                        Kind
	Severity                    Severity
	Details                     Details
}

func NewEvent(spec EventSpec) (Event, error) {
	spec.SourceID, spec.TradingDate, spec.Mode = strings.TrimSpace(spec.SourceID), strings.TrimSpace(spec.TradingDate), strings.TrimSpace(spec.Mode)
	if _, err := time.Parse("2006-01-02", spec.TradingDate); err != nil || spec.SourceID == "" || (spec.Mode != "PAPER" && spec.Mode != "SHADOW") || spec.OccurredAt.IsZero() || !validSeverity(spec.Severity) || !validCategory(spec.Category) || !validKind(spec.Kind) {
		return Event{}, ErrInvalid
	}
	spec.OccurredAt = spec.OccurredAt.UTC()
	raw, err := json.Marshal(struct {
		SchemaVersion, SourceID, TradingDate, OccurredAt, Mode, Category, Kind, Severity string
		Details                                                                          Details `json:"details"`
	}{SchemaVersion, spec.SourceID, spec.TradingDate, spec.OccurredAt.Format(time.RFC3339Nano), spec.Mode, string(spec.Category), string(spec.Kind), string(spec.Severity), spec.Details})
	if err != nil {
		return Event{}, ErrInvalid
	}
	sum := sha256.Sum256(raw)
	idRaw := sha256.Sum256([]byte(SchemaVersion + "|" + string(spec.Kind) + "|" + spec.SourceID))
	return Event{SchemaVersion: SchemaVersion, ID: hex.EncodeToString(idRaw[:]), Checksum: hex.EncodeToString(sum[:]), SourceID: spec.SourceID, TradingDate: spec.TradingDate, OccurredAt: spec.OccurredAt, Mode: spec.Mode, Category: spec.Category, Kind: spec.Kind, Severity: spec.Severity, Details: spec.Details}, nil
}

func validSeverity(v Severity) bool {
	return v == SeverityInfo || v == SeverityWarning || v == SeverityCritical
}
func validCategory(v Category) bool {
	switch v {
	case CategoryRuntime, CategoryReadiness, CategoryStrategy, CategoryProposal, CategoryRisk, CategoryExecution, CategoryReconciliation, CategoryControl, CategoryCAS, CategoryValuation, CategoryReporting:
		return true
	}
	return false
}
func validKind(v Kind) bool {
	switch v {
	case KindRuntimeReady, KindRuntimeDegraded, KindRuntimeHalted, KindReadinessLost, KindReadinessRestored, KindStrategyActivated, KindStrategyRestricted, KindStrategyHalted, KindStrategyReady, KindOptionSelected, KindEntryProposal, KindExitProposal, KindProposalGenerated, KindRiskApproved, KindRiskModified, KindRiskRejected, KindPaperSubmitted, KindPaperPartialFill, KindPaperFill, KindShadowTrade, KindShadowSignal, KindPositionUpdate, KindPositionClosed, KindValidationIncident, KindShadowQualification, KindShadowQualificationResult, KindShadowSessionReady, KindShadowSessionClosed, KindExecutionUnknown, KindReconciliationMismatch, KindBrokerOnlyExposure, KindKillSwitch, KindCircuitBreaker, KindPreCAS, KindCASActive, KindPostCAS, KindCASRestricted, KindValuationPartial, KindValuationStale, KindValuationUnavailable, KindFinancialSnapshot, KindDailyPnLWarning, KindEndOfDay:
		return true
	}
	return false
}

type DeliveryState string

const (
	DeliveryPending    DeliveryState = "PENDING"
	DeliveryDelivering DeliveryState = "DELIVERING"
	DeliveryDelivered  DeliveryState = "DELIVERED"
	DeliveryRetryWait  DeliveryState = "RETRY_WAIT"
	DeliveryFailed     DeliveryState = "FAILED"
	DeliverySuppressed DeliveryState = "SUPPRESSED"
	DeliveryCoalesced  DeliveryState = "COALESCED"
	DeliveryDropped    DeliveryState = "DROPPED"
)

type Delivery struct {
	NotificationID string        `json:"notification_id"`
	EventID        string        `json:"event_id"`
	Provider       string        `json:"provider"`
	State          DeliveryState `json:"state"`
	Attempts       int           `json:"attempts"`
	Reason         string        `json:"reason,omitempty"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

func NotificationID(event Event, provider, templateVersion string) string {
	sum := sha256.Sum256([]byte(event.ID + "|" + strings.TrimSpace(provider) + "|" + strings.TrimSpace(templateVersion)))
	return hex.EncodeToString(sum[:])
}

type RenderedMessage struct{ NotificationID, Text string }
type Receipt struct{ ProviderMessageID string }
type Sender interface {
	Send(context.Context, RenderedMessage) (Receipt, error)
	Status() ProviderStatus
}

type ProviderStatus struct {
	Provider         string    `json:"provider"`
	State            string    `json:"state"`
	LastSuccess      time.Time `json:"last_success,omitempty"`
	LastFailure      time.Time `json:"last_failure,omitempty"`
	FailureClass     string    `json:"failure_class,omitempty"`
	RateLimitedUntil time.Time `json:"rate_limited_until,omitempty"`
}
