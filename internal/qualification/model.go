// Package qualification measures SHADOW opportunities without creating
// execution authority, OMS orders, fills, or authoritative accounting state.
package qualification

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

const (
	SchemaVersion              = "phase8-m3-shadow-qualification/v1"
	StrategyID                 = "EMA_REFERENCE_V1"
	StrategyVersion            = "1"
	RegimePolicyVersion        = "transparent-ema-range-regime/v1"
	ShadowPricePolicyVersion   = "option-executable-side-or-ltp/v1"
	CostPolicyVersion          = "qualification-cost-boundary/v1"
	QualificationPolicyVersion = "operator-reviewed-minimum-evidence/v1"
)

var (
	ErrInvalid      = errors.New("invalid shadow qualification value")
	ErrDuplicate    = errors.New("duplicate qualification evidence")
	ErrConflict     = errors.New("qualification identity conflict")
	ErrNotFound     = errors.New("qualification evidence not found")
	ErrBlocked      = errors.New("shadow qualification blocked")
	ErrCorrupt      = errors.New("qualification checkpoint corrupt")
	ErrOpenPosition = errors.New("shadow qualification position already open")
	ErrNoPosition   = errors.New("shadow qualification position not open")
)

type Underlying string

const (
	NIFTY     Underlying = "NIFTY"
	BANKNIFTY Underlying = "BANKNIFTY"
)

func (u Underlying) valid() bool { return u == NIFTY || u == BANKNIFTY }

type Direction string

const (
	DirectionLong Direction = "LONG"
	DirectionExit Direction = "EXIT"
)

type RiskOutcome string

const (
	RiskApproved RiskOutcome = "APPROVED"
	RiskRejected RiskOutcome = "REJECTED"
)

type Quality string

const (
	QualityComplete Quality = "COMPLETE"
	QualityPartial  Quality = "PARTIAL"
	QualityInvalid  Quality = "INVALID"
)

type UnavailableReason string

const (
	ReasonNone               UnavailableReason = "NONE"
	ReasonStaleSpot          UnavailableReason = "STALE_SPOT"
	ReasonStaleFuture        UnavailableReason = "STALE_FUTURE"
	ReasonStaleOption        UnavailableReason = "STALE_OPTION"
	ReasonMissingOption      UnavailableReason = "MISSING_OPTION_QUOTE"
	ReasonLTPOnly            UnavailableReason = "LTP_ONLY"
	ReasonHorizonUnavailable UnavailableReason = "HORIZON_UNAVAILABLE"
	ReasonSessionEnded       UnavailableReason = "SESSION_ENDED"
	ReasonRestartGap         UnavailableReason = "RESTART_GAP"
	ReasonMappingBlocked     UnavailableReason = "MAPPING_BLOCKED"
	ReasonCASBlocked         UnavailableReason = "CAS_BLOCKED"
	ReasonSessionBlocked     UnavailableReason = "SESSION_BLOCKED"
	ReasonRiskRejected       UnavailableReason = "RISK_REJECTED"
	ReasonControlBlocked     UnavailableReason = "CONTROL_BLOCKED"
)

type QualificationState string

const (
	StateReference          QualificationState = "REFERENCE"
	StateShadowCollecting   QualificationState = "SHADOW_COLLECTING"
	StateInsufficientSample QualificationState = "INSUFFICIENT_SAMPLE"
	StateEligibleForReview  QualificationState = "ELIGIBLE_FOR_REVIEW"
	StateQualified          QualificationState = "QUALIFIED"
	StateRejected           QualificationState = "REJECTED"
)

type TrendRegime string
type VolatilityRegime string

const (
	TrendTrending    TrendRegime      = "TRENDING"
	TrendRanging     TrendRegime      = "RANGING"
	VolatilityHigh   VolatilityRegime = "HIGH_VOLATILITY"
	VolatilityNormal VolatilityRegime = "NORMAL_VOLATILITY"
	VolatilityLow    VolatilityRegime = "LOW_VOLATILITY"
)

type Regime struct {
	PolicyVersion string           `json:"policy_version"`
	Trend         TrendRegime      `json:"trend"`
	Volatility    VolatilityRegime `json:"volatility"`
}

type RegimeInput struct {
	SpotMinor, EMA20Scaled, EMA50Scaled  int64
	RecentRangeMinor, BaselineRangeMinor int64
}

// ClassifyRegime uses decision-time EMA separation and completed-window range
// only. It never consumes observations after the signal timestamp.
func ClassifyRegime(input RegimeInput) (Regime, error) {
	if input.SpotMinor <= 0 || input.EMA20Scaled <= 0 || input.EMA50Scaled <= 0 || input.RecentRangeMinor <= 0 || input.BaselineRangeMinor <= 0 {
		return Regime{}, ErrInvalid
	}
	difference := input.EMA20Scaled - input.EMA50Scaled
	if difference < 0 {
		difference = -difference
	}
	trend := TrendRanging
	separationMinor := difference / 1_000_000
	thresholdMinor := input.SpotMinor / 500 // 20 basis points.
	if input.SpotMinor%500 != 0 {
		thresholdMinor++
	}
	if separationMinor >= thresholdMinor {
		trend = TrendTrending
	}
	volatility := VolatilityNormal
	if input.RecentRangeMinor >= input.BaselineRangeMinor+input.BaselineRangeMinor/2 {
		volatility = VolatilityHigh
	} else if input.RecentRangeMinor <= input.BaselineRangeMinor/2 {
		volatility = VolatilityLow
	}
	return Regime{RegimePolicyVersion, trend, volatility}, nil
}

type Quote struct {
	InstrumentID string    `json:"instrument_id"`
	BidMinor     int64     `json:"bid_minor,omitempty"`
	AskMinor     int64     `json:"ask_minor,omitempty"`
	LTPMinor     int64     `json:"ltp_minor"`
	ObservedAt   time.Time `json:"observed_at"`
}

type PriceReference struct {
	PolicyVersion string  `json:"policy_version"`
	PriceMinor    int64   `json:"price_minor"`
	Source        string  `json:"source"`
	Quality       Quality `json:"quality"`
}

func selectPrice(quote Quote, entry bool) (PriceReference, error) {
	if strings.TrimSpace(quote.InstrumentID) == "" || quote.LTPMinor <= 0 || quote.ObservedAt.IsZero() || quote.BidMinor < 0 || quote.AskMinor < 0 {
		return PriceReference{}, ErrInvalid
	}
	if quote.BidMinor > 0 && quote.AskMinor > 0 && quote.AskMinor >= quote.BidMinor {
		price, source := quote.BidMinor, "BEST_BID"
		if entry {
			price, source = quote.AskMinor, "BEST_ASK"
		}
		return PriceReference{ShadowPricePolicyVersion, price, source, QualityComplete}, nil
	}
	return PriceReference{ShadowPricePolicyVersion, quote.LTPMinor, "LTP_APPROXIMATION", QualityPartial}, nil
}

type SignalInput struct {
	StrategyID, StrategyVersion string
	Underlying                  Underlying
	SignalID                    string
	SignalTime                  time.Time
	MarketSession, CASState     string
	SpotMinor                   int64
	SpotTime                    time.Time
	FutureID, FutureExpiry      string
	FutureMinor                 int64
	FutureTime                  time.Time
	OptionID, OptionExpiry      string
	StrikeMinor                 int64
	OptionType                  string
	OptionQuote                 Quote
	EMA20Scaled, EMA50Scaled    int64
	WarmupComplete, Fresh       bool
	Direction                   Direction
	Risk                        RiskOutcome
	RiskDecisionID              string
	RiskReason                  string
	Quantity                    int64
	RegimeInput                 RegimeInput
}

type HorizonMeasurement struct {
	HorizonSeconds       int64             `json:"horizon_seconds"`
	DueAt                time.Time         `json:"due_at"`
	ObservedAt           time.Time         `json:"observed_at,omitempty"`
	Available            bool              `json:"available"`
	UnavailableReason    UnavailableReason `json:"unavailable_reason,omitempty"`
	SpotMovementMinor    int64             `json:"spot_movement_minor,omitempty"`
	FutureMovementMinor  int64             `json:"future_movement_minor,omitempty"`
	OptionMarkMinor      int64             `json:"option_mark_minor,omitempty"`
	OptionChangeMinor    int64             `json:"option_change_minor,omitempty"`
	OptionChangeBPS      int64             `json:"option_change_bps,omitempty"`
	HypotheticalPnLMinor int64             `json:"hypothetical_pnl_minor,omitempty"`
	PriceQuality         Quality           `json:"price_quality,omitempty"`
}

type SignalRecord struct {
	QualificationID, StrategyID, StrategyVersion string
	Underlying                                   Underlying
	SignalID                                     string
	SignalTime                                   time.Time
	MarketSession, CASState                      string
	SpotMinor                                    int64
	SpotTime                                     time.Time
	FutureID, FutureExpiry                       string
	FutureMinor, BasisMinor                      int64
	FutureTime                                   time.Time
	OptionID, OptionExpiry                       string
	StrikeMinor                                  int64
	OptionType                                   string
	OptionQuote                                  Quote
	EMA20Scaled, EMA50Scaled                     int64
	WarmupComplete, Fresh                        bool
	Direction                                    Direction
	Risk                                         RiskOutcome
	RiskDecisionID                               string
	RiskReason                                   string
	Mode                                         string
	Regime                                       Regime
	Entry                                        PriceReference
	Quality                                      Quality
	QualityReasons                               []UnavailableReason
	Horizons                                     []HorizonMeasurement
}

type ShadowQualificationPosition struct {
	QualificationID                string     `json:"qualification_id"`
	Underlying                     Underlying `json:"underlying"`
	OptionID                       string     `json:"option_id"`
	Quantity                       int64      `json:"quantity"`
	EntryMinor                     int64      `json:"entry_minor"`
	EntryTime                      time.Time  `json:"entry_time"`
	CurrentMarkMinor               int64      `json:"current_mark_minor"`
	MFEChangeMinor, MAEChangeMinor int64
	MFEAt, MAEAt                   time.Time
	LastObservationAt              time.Time
	LastObservationID              string
}

type ReviewDecision struct {
	Underlying Underlying `json:"underlying"`
	Approved   bool       `json:"approved"`
	Operator   string     `json:"operator"`
	Reference  string     `json:"reference"`
	At         time.Time  `json:"at"`
}

type ShadowTrade struct {
	QualificationID                string     `json:"qualification_id"`
	Underlying                     Underlying `json:"underlying"`
	OptionID                       string     `json:"option_id"`
	Quantity                       int64      `json:"quantity"`
	EntryMinor, ExitMinor          int64
	EntryTime, ExitTime            time.Time
	HoldingSeconds                 int64
	GrossPnLMinor                  int64
	NetPnLAvailable                bool
	NetPnLMinor                    int64 `json:"net_pnl_minor,omitempty"`
	MFEChangeMinor, MAEChangeMinor int64
	MFEAt, MAEAt                   time.Time
	Regime                         Regime
}

type BlockCounters struct {
	DataQualityFailures, StaleDataBlocks, MappingBlocks, CASBlocks, SessionBlocks, ControlBlocks uint64
}

type Scorecard struct {
	StrategyID, StrategyVersion                                                      string
	Underlying                                                                       Underlying
	State                                                                            QualificationState
	Signals, AcceptedSignals, RiskRejectedSignals, CompletedTrades, OpenObservations uint64
	Wins, Losses, Flat                                                               uint64
	WinRateBPS                                                                       int64
	GrossPnLMinor, AveragePnLMinor, MedianPnLMinor                                   int64
	AverageWinnerMinor, AverageLoserMinor                                            int64
	ProfitFactorBPS                                                                  int64
	ProfitFactorAvailable                                                            bool
	MaximumFavorableExcursionMinor, MaximumAdverseExcursionMinor                     int64
	MaximumObservedDrawdownMinor                                                     int64
	AverageHoldingSeconds                                                            int64
	DirectionalOutcomeBPS                                                            map[string]int64
	BlockCounters
	NetPnLAvailable        bool
	NetPnLMinor            int64
	CompletedSessions      uint64
	MinimumCompletedTrades uint64
	MinimumSessions        uint64
}

type CostPolicy struct {
	Version                    string `json:"version"`
	Source                     string `json:"source,omitempty"`
	Configured                 bool   `json:"configured"`
	BrokerageMinor             int64  `json:"brokerage_minor,omitempty"`
	ExchangeChargesMinor       int64  `json:"exchange_charges_minor,omitempty"`
	TaxesAndLeviesMinor        int64  `json:"taxes_and_levies_minor,omitempty"`
	GSTMinor                   int64  `json:"gst_minor,omitempty"`
	StampDutyMinor             int64  `json:"stamp_duty_minor,omitempty"`
	DeterministicSlippageMinor int64  `json:"deterministic_slippage_minor,omitempty"`
}

func (p CostPolicy) totalMinor() int64 {
	return p.BrokerageMinor + p.ExchangeChargesMinor + p.TaxesAndLeviesMinor + p.GSTMinor + p.StampDutyMinor + p.DeterministicSlippageMinor
}

type Policy struct {
	Version                                 string `json:"version"`
	MinimumCompletedTrades, MinimumSessions uint64
	Horizons                                []time.Duration
	MaximumObservationLateness              time.Duration
	Cost                                    CostPolicy
}

func DefaultPolicy() Policy {
	return Policy{QualificationPolicyVersion, 20, 5, []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}, time.Minute, CostPolicy{Version: CostPolicyVersion}}
}

func (p Policy) validate() error {
	if p.Version == "" || p.MinimumCompletedTrades == 0 || p.MinimumSessions == 0 || len(p.Horizons) != 4 || p.MaximumObservationLateness <= 0 || p.Cost.Version == "" || p.Cost.BrokerageMinor < 0 || p.Cost.ExchangeChargesMinor < 0 || p.Cost.TaxesAndLeviesMinor < 0 || p.Cost.GSTMinor < 0 || p.Cost.StampDutyMinor < 0 || p.Cost.DeterministicSlippageMinor < 0 || (p.Cost.Configured && strings.TrimSpace(p.Cost.Source) == "") {
		return ErrInvalid
	}
	previous := time.Duration(0)
	for _, horizon := range p.Horizons {
		if horizon <= previous {
			return ErrInvalid
		}
		previous = horizon
	}
	return nil
}

type Series struct {
	StrategyID, StrategyVersion string
	Underlying                  Underlying
	State                       QualificationState
	Records                     []SignalRecord
	Open                        *ShadowQualificationPosition
	Trades                      []ShadowTrade
	Blocks                      BlockCounters
	Reviews                     []ReviewDecision
}

type Snapshot struct {
	SchemaVersion string   `json:"schema_version"`
	Revision      uint64   `json:"revision"`
	Policy        Policy   `json:"policy"`
	Series        []Series `json:"series"`
	Checksum      string   `json:"checksum"`
}

func finalizeSnapshot(value Snapshot) (Snapshot, error) {
	value.SchemaVersion = SchemaVersion
	value.Checksum = ""
	sort.Slice(value.Series, func(i, j int) bool { return value.Series[i].Underlying < value.Series[j].Underlying })
	raw, err := json.Marshal(value)
	if err != nil {
		return Snapshot{}, err
	}
	sum := sha256.Sum256(raw)
	value.Checksum = hex.EncodeToString(sum[:])
	return value, nil
}

func (value Snapshot) Verify() error {
	expected := value.Checksum
	rebuilt, err := finalizeSnapshot(value)
	if err != nil || rebuilt.Checksum != expected || value.SchemaVersion != SchemaVersion {
		return ErrCorrupt
	}
	return nil
}

func identity(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
