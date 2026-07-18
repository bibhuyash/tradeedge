package readiness

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

var (
	ErrInvalidWatchlist = errors.New("invalid market-data watchlist")
	ErrInvalidPolicy    = errors.New("invalid market-data freshness policy")
)

type State string

const (
	StateDisabled      State = "DISABLED"
	StateReady         State = "READY"
	StateWarmingUp     State = "WARMING_UP"
	StateNoData        State = "NO_DATA"
	StateStale         State = "STALE"
	StateIncomplete    State = "INCOMPLETE"
	StateSessionClosed State = "SESSION_CLOSED"
	StateUnknown       State = "UNKNOWN"
)

type ReasonCode string

const (
	ReasonNone                 ReasonCode = "NONE"
	ReasonMarketDataDisabled   ReasonCode = "MARKET_DATA_DISABLED"
	ReasonCalendarUnavailable  ReasonCode = "CALENDAR_UNAVAILABLE"
	ReasonCalendarOutOfRange   ReasonCode = "CALENDAR_OUT_OF_RANGE"
	ReasonHoliday              ReasonCode = "HOLIDAY"
	ReasonBeforeOpen           ReasonCode = "BEFORE_OPEN"
	ReasonBetweenSessions      ReasonCode = "BETWEEN_SESSIONS"
	ReasonAfterClose           ReasonCode = "AFTER_CLOSE"
	ReasonWarmupActive         ReasonCode = "WARMUP_ACTIVE"
	ReasonProviderUnavailable  ReasonCode = "PROVIDER_UNAVAILABLE"
	ReasonNoAcceptedEvent      ReasonCode = "NO_ACCEPTED_EVENT"
	ReasonExchangeTimeStale    ReasonCode = "EXCHANGE_TIME_STALE"
	ReasonIngestionTimeStale   ReasonCode = "INGESTION_TIME_STALE"
	ReasonTransportLagExceeded ReasonCode = "TRANSPORT_LAG_EXCEEDED"
	ReasonClockSkew            ReasonCode = "CLOCK_SKEW"
	ReasonMissingCandle        ReasonCode = "MISSING_CANDLE"
	ReasonCoverageIncomplete   ReasonCode = "COVERAGE_INCOMPLETE"
	ReasonPolicyInvalid        ReasonCode = "POLICY_INVALID"
)

type Requirement struct {
	Provider     domain.Provider
	InstrumentID domain.InstrumentID
	Exchange     domain.Exchange
	Segment      domain.Segment
	EventKind    model.EventKind
	Interval     model.CandleInterval
	Required     bool
}

func (r Requirement) validate() error {
	if r.Provider == "" || r.InstrumentID.IsZero() || r.Exchange == "" || r.Segment == "" {
		return ErrInvalidWatchlist
	}
	switch r.EventKind {
	case model.EventKindQuote:
		if r.Interval != "" {
			return ErrInvalidWatchlist
		}
	case model.EventKindCandle:
		if _, valid := r.Interval.Duration(); !valid {
			return ErrInvalidWatchlist
		}
	default:
		return ErrInvalidWatchlist
	}
	return nil
}

type Watchlist struct {
	ID           string
	Version      string
	Requirements []Requirement
}

func NewWatchlist(id string, requirements []Requirement) (Watchlist, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(requirements) == 0 || len(requirements) > 250 {
		return Watchlist{}, ErrInvalidWatchlist
	}
	copied := append([]Requirement(nil), requirements...)
	keys := make([]string, len(copied))
	seen := make(map[string]struct{}, len(copied))
	required := 0
	for index, requirement := range copied {
		if err := requirement.validate(); err != nil {
			return Watchlist{}, err
		}
		key := requirementKey(requirement)
		if _, exists := seen[key]; exists {
			return Watchlist{}, ErrInvalidWatchlist
		}
		seen[key] = struct{}{}
		keys[index] = key + fmt.Sprintf("|%t", requirement.Required)
		if requirement.Required {
			required++
		}
	}
	if required == 0 {
		return Watchlist{}, ErrInvalidWatchlist
	}
	sort.Strings(keys)
	digest := sha256.Sum256([]byte("v1|" + id + "|" + strings.Join(keys, "\n")))
	return Watchlist{
		ID: id, Version: hex.EncodeToString(digest[:]), Requirements: copied,
	}, nil
}

type StreamPolicy struct {
	ExpectedCadence time.Duration
	Warmup          time.Duration
	MaxExchangeAge  time.Duration
	MaxIngestionAge time.Duration
	MaxTransportLag time.Duration
	CompletionGrace time.Duration
}

type FreshnessPolicy struct {
	Version            string
	ClockSkewTolerance time.Duration
	Quote              StreamPolicy
	Candles            map[model.CandleInterval]StreamPolicy
}

func DefaultPolicy() FreshnessPolicy {
	return FreshnessPolicy{
		Version: "phase-1.1-defaults/v1", ClockSkewTolerance: time.Second,
		Quote: StreamPolicy{
			ExpectedCadence: 250 * time.Millisecond,
			Warmup:          30 * time.Second, MaxExchangeAge: 5 * time.Second,
			MaxIngestionAge: 5 * time.Second, MaxTransportLag: 2 * time.Second,
		},
		Candles: map[model.CandleInterval]StreamPolicy{
			model.Interval1Minute:   {CompletionGrace: 10 * time.Second},
			model.Interval5Minutes:  {CompletionGrace: 20 * time.Second},
			model.Interval15Minutes: {CompletionGrace: 30 * time.Second},
			model.Interval1Day:      {CompletionGrace: 5 * time.Minute},
		},
	}
}

func (p FreshnessPolicy) Validate() error {
	if strings.TrimSpace(p.Version) == "" || p.ClockSkewTolerance < 0 ||
		p.Quote.ExpectedCadence <= 0 || p.Quote.Warmup < 0 ||
		p.Quote.MaxExchangeAge <= 0 || p.Quote.MaxIngestionAge <= 0 ||
		p.Quote.MaxTransportLag <= 0 {
		return ErrInvalidPolicy
	}
	for _, interval := range []model.CandleInterval{
		model.Interval1Minute, model.Interval5Minutes, model.Interval15Minutes, model.Interval1Day,
	} {
		value, found := p.Candles[interval]
		if !found || value.CompletionGrace <= 0 {
			return ErrInvalidPolicy
		}
	}
	return nil
}

type Diagnostic struct {
	WatchlistID  string               `json:"watchlist_id"`
	Provider     domain.Provider      `json:"provider"`
	InstrumentID domain.InstrumentID  `json:"-"`
	Instrument   string               `json:"instrument_id"`
	Exchange     domain.Exchange      `json:"exchange"`
	Segment      domain.Segment       `json:"segment"`
	EventKind    model.EventKind      `json:"event_kind"`
	Interval     model.CandleInterval `json:"interval,omitempty"`
	Required     bool                 `json:"required"`
	State        State                `json:"state"`
	Reason       ReasonCode           `json:"reason"`
	LastExchange time.Time            `json:"last_exchange_time,omitempty"`
	LastIngested time.Time            `json:"last_ingested_at,omitempty"`
	MissingOpen  time.Time            `json:"missing_open,omitempty"`
	MissingClose time.Time            `json:"missing_close,omitempty"`
}

type ScopeSnapshot struct {
	ID            string       `json:"id"`
	State         State        `json:"state"`
	Reasons       []ReasonCode `json:"reasons"`
	Required      int          `json:"required"`
	Covered       int          `json:"covered"`
	CoverageRatio float64      `json:"coverage_ratio"`
}

type Snapshot struct {
	EvaluatedAt      time.Time       `json:"evaluated_at"`
	CalendarVersion  string          `json:"calendar_version,omitempty"`
	PolicyVersion    string          `json:"policy_version"`
	State            State           `json:"state"`
	Reasons          []ReasonCode    `json:"reasons"`
	TradingPermitted bool            `json:"trading_permitted"`
	Providers        []ScopeSnapshot `json:"providers"`
	Watchlists       []ScopeSnapshot `json:"watchlists"`
	Diagnostics      []Diagnostic    `json:"diagnostics,omitempty"`
}

func (s Snapshot) OperationallyReady() bool {
	return s.State == StateReady || s.State == StateSessionClosed || s.State == StateDisabled
}

func requirementKey(requirement Requirement) string {
	return fmt.Sprintf("%s|%s|%s|%s", requirement.Provider, requirement.InstrumentID,
		requirement.EventKind, requirement.Interval)
}
