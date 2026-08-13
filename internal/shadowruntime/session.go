package shadowruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/bibhuyash/tradeedge/internal/qualification"
)

type SessionQuality string

const (
	SessionCollecting SessionQuality = "COLLECTING"
	SessionComplete   SessionQuality = "COMPLETE"
	SessionPartial    SessionQuality = "PARTIAL"
	SessionInvalid    SessionQuality = "INVALID"
)

type SessionReason string

const (
	ReasonLateStartup       SessionReason = "LATE_STARTUP"
	ReasonEarlyShutdown     SessionReason = "EARLY_SHUTDOWN"
	ReasonZerodhaDisconnect SessionReason = "ZERODHA_DISCONNECT"
	ReasonMarketDataGap     SessionReason = "MARKET_DATA_GAP"
	ReasonCheckpointGap     SessionReason = "CHECKPOINT_GAP"
	ReasonTelegramOutage    SessionReason = "TELEGRAM_OUTAGE"
	ReasonMappingFailure    SessionReason = "MAPPING_FAILURE"
	ReasonCASRestriction    SessionReason = "CAS_RESTRICTION"
	ReasonOperatorShutdown  SessionReason = "OPERATOR_SHUTDOWN"
	ReasonNoCrossover       SessionReason = "NO_CROSSOVER"
)

type SessionScorecard struct {
	SchemaVersion                  string                   `json:"schema_version"`
	SessionID                      string                   `json:"session_id"`
	TradingDate                    string                   `json:"trading_date"`
	Underlying                     qualification.Underlying `json:"underlying"`
	StrategyID                     string                   `json:"strategy_id"`
	StrategyVersion                string                   `json:"strategy_version"`
	StartedAt                      time.Time                `json:"started_at"`
	EndedAt                        time.Time                `json:"ended_at,omitempty"`
	Quality                        SessionQuality           `json:"quality"`
	Reasons                        []SessionReason          `json:"reasons,omitempty"`
	MarketReadySeconds             int64                    `json:"market_ready_seconds"`
	DataGaps                       uint64                   `json:"data_gaps"`
	Signals                        uint64                   `json:"signals"`
	AcceptedSignals                uint64                   `json:"accepted_signals"`
	RiskRejectedSignals            uint64                   `json:"risk_rejected_signals"`
	CompletedShadowObservations    uint64                   `json:"completed_shadow_observations"`
	OpenObservations               uint64                   `json:"open_observations"`
	Wins                           uint64                   `json:"wins"`
	Losses                         uint64                   `json:"losses"`
	Flat                           uint64                   `json:"flat"`
	GrossPnLMinor                  int64                    `json:"gross_pnl_minor"`
	NetPnL                         string                   `json:"net_pnl"`
	MaximumFavorableExcursionMinor int64                    `json:"maximum_favorable_excursion_minor"`
	MaximumAdverseExcursionMinor   int64                    `json:"maximum_adverse_excursion_minor"`
	MaximumObservedDrawdownMinor   int64                    `json:"maximum_observed_drawdown_minor"`
	HorizonOutcomeBPS              map[string]int64         `json:"horizon_outcome_bps"`
	RegimeDistribution             map[string]uint64        `json:"regime_distribution"`
	DataQualityFailures            uint64                   `json:"data_quality_failures"`
	StaleBlocks                    uint64                   `json:"stale_blocks"`
	MappingBlocks                  uint64                   `json:"mapping_blocks"`
	CASBlocks                      uint64                   `json:"cas_blocks"`
	SessionBlocks                  uint64                   `json:"session_blocks"`
	RestartCount                   uint64                   `json:"restart_count"`
	TelegramAvailable              bool                     `json:"telegram_available"`
	Checksum                       string                   `json:"checksum"`
}

type SessionTracker struct {
	Current map[qualification.Underlying]SessionScorecard        `json:"current"`
	Closed  []SessionScorecard                                   `json:"closed"`
	Opening map[qualification.Underlying]qualification.Scorecard `json:"opening"`
}

func NewSessionTracker(date string, at time.Time, opening []qualification.Scorecard, telegram bool) (*SessionTracker, error) {
	if _, err := time.Parse("2006-01-02", date); err != nil || at.IsZero() || len(opening) != 2 {
		return nil, ErrInvalid
	}
	tracker := &SessionTracker{Current: map[qualification.Underlying]SessionScorecard{}, Opening: map[qualification.Underlying]qualification.Scorecard{}}
	for _, card := range opening {
		if card.Underlying != qualification.NIFTY && card.Underlying != qualification.BANKNIFTY {
			return nil, ErrInvalid
		}
		tracker.Opening[card.Underlying] = card
		tracker.Current[card.Underlying] = SessionScorecard{SchemaVersion: SchemaVersion, SessionID: identity(date, qualification.StrategyID, qualification.StrategyVersion, string(card.Underlying)), TradingDate: date, Underlying: card.Underlying, StrategyID: qualification.StrategyID, StrategyVersion: qualification.StrategyVersion, StartedAt: at.UTC(), Quality: SessionCollecting, NetPnL: "NOT_AVAILABLE", HorizonOutcomeBPS: map[string]int64{}, RegimeDistribution: map[string]uint64{}, TelegramAvailable: telegram}
	}
	return tracker, nil
}

func (t *SessionTracker) Restart() {
	for key, value := range t.Current {
		value.RestartCount++
		t.Current[key] = value
	}
}

func (t *SessionTracker) AddReason(underlying qualification.Underlying, reason SessionReason) {
	value, ok := t.Current[underlying]
	if !ok {
		return
	}
	for _, existing := range value.Reasons {
		if existing == reason {
			return
		}
	}
	value.Reasons = append(value.Reasons, reason)
	sort.Slice(value.Reasons, func(i, j int) bool { return value.Reasons[i] < value.Reasons[j] })
	t.Current[underlying] = value
}

func (t *SessionTracker) Close(at time.Time, cards []qualification.Scorecard, snapshot qualification.Snapshot, complete bool) error {
	if at.IsZero() || len(cards) != 2 {
		return ErrInvalid
	}
	byUnderlying := map[qualification.Underlying]qualification.Scorecard{}
	for _, card := range cards {
		byUnderlying[card.Underlying] = card
	}
	for _, underlying := range []qualification.Underlying{qualification.NIFTY, qualification.BANKNIFTY} {
		current, ok := t.Current[underlying]
		if !ok || !current.EndedAt.IsZero() {
			return ErrInvalid
		}
		opening, next := t.Opening[underlying], byUnderlying[underlying]
		current.EndedAt = at.UTC()
		current.Signals = next.Signals - opening.Signals
		current.AcceptedSignals = next.AcceptedSignals - opening.AcceptedSignals
		current.RiskRejectedSignals = next.RiskRejectedSignals - opening.RiskRejectedSignals
		current.CompletedShadowObservations = next.CompletedTrades - opening.CompletedTrades
		current.OpenObservations = next.OpenObservations
		current.Wins, current.Losses, current.Flat = next.Wins-opening.Wins, next.Losses-opening.Losses, next.Flat-opening.Flat
		current.GrossPnLMinor = next.GrossPnLMinor - opening.GrossPnLMinor
		current.MaximumFavorableExcursionMinor, current.MaximumAdverseExcursionMinor = next.MaximumFavorableExcursionMinor, next.MaximumAdverseExcursionMinor
		current.MaximumObservedDrawdownMinor = next.MaximumObservedDrawdownMinor
		current.DataQualityFailures = next.DataQualityFailures - opening.DataQualityFailures
		current.StaleBlocks = next.StaleDataBlocks - opening.StaleDataBlocks
		current.MappingBlocks = next.MappingBlocks - opening.MappingBlocks
		current.CASBlocks = next.CASBlocks - opening.CASBlocks
		current.SessionBlocks = next.SessionBlocks - opening.SessionBlocks
		current.HorizonOutcomeBPS = cloneIntMap(next.DirectionalOutcomeBPS)
		current.RegimeDistribution = regimeDistribution(snapshot, underlying, current.TradingDate)
		if current.Signals == 0 {
			current.Reasons = append(current.Reasons, ReasonNoCrossover)
		}
		current.Quality = SessionComplete
		if !complete || len(current.Reasons) > 1 || (len(current.Reasons) == 1 && current.Reasons[0] != ReasonNoCrossover) {
			current.Quality = SessionPartial
		}
		if hasInvalidReason(current.Reasons) {
			current.Quality = SessionInvalid
		}
		final, err := finalizeSession(current)
		if err != nil {
			return err
		}
		t.Current[underlying] = final
		t.Closed = append(t.Closed, final)
	}
	sort.Slice(t.Closed, func(i, j int) bool {
		if t.Closed[i].TradingDate == t.Closed[j].TradingDate {
			return t.Closed[i].Underlying < t.Closed[j].Underlying
		}
		return t.Closed[i].TradingDate < t.Closed[j].TradingDate
	})
	return nil
}

func hasInvalidReason(values []SessionReason) bool {
	for _, value := range values {
		if value == ReasonMappingFailure || value == ReasonCheckpointGap {
			return true
		}
	}
	return false
}

func regimeDistribution(snapshot qualification.Snapshot, underlying qualification.Underlying, date string) map[string]uint64 {
	result := map[string]uint64{}
	for _, series := range snapshot.Series {
		if series.Underlying == underlying {
			for _, record := range series.Records {
				if record.SignalTime.UTC().Format("2006-01-02") == date {
					result[string(record.Regime.Trend)+"/"+string(record.Regime.Volatility)]++
				}
			}
		}
	}
	return result
}

func finalizeSession(value SessionScorecard) (SessionScorecard, error) {
	value.Checksum = ""
	raw, err := json.Marshal(value)
	if err != nil {
		return SessionScorecard{}, err
	}
	sum := sha256.Sum256(raw)
	value.Checksum = hex.EncodeToString(sum[:])
	return value, nil
}

type MultiSessionScorecard struct {
	Underlying                                                                qualification.Underlying `json:"underlying"`
	SessionsObserved, CompleteSessions, PartialSessions, InvalidSessions      uint64
	Signals, CompletedShadowObservations, Wins, Losses, Flat                  uint64
	GrossPnLMinor, MFEChangeMinor, MAEChangeMinor, MaximumDrawdownMinor       int64
	RegimeDistribution                                                        map[string]uint64
	DataQualityFailures, StaleBlocks, MappingBlocks, CASBlocks, SessionBlocks uint64
	QualificationState                                                        qualification.QualificationState
}

func (t *SessionTracker) Multi(cards []qualification.Scorecard) []MultiSessionScorecard {
	states := map[qualification.Underlying]qualification.QualificationState{}
	for _, card := range cards {
		states[card.Underlying] = card.State
	}
	result := map[qualification.Underlying]*MultiSessionScorecard{}
	for _, u := range []qualification.Underlying{qualification.NIFTY, qualification.BANKNIFTY} {
		result[u] = &MultiSessionScorecard{Underlying: u, RegimeDistribution: map[string]uint64{}, QualificationState: states[u]}
	}
	for _, s := range t.Closed {
		v := result[s.Underlying]
		v.SessionsObserved++
		switch s.Quality {
		case SessionComplete:
			v.CompleteSessions++
		case SessionPartial:
			v.PartialSessions++
		case SessionInvalid:
			v.InvalidSessions++
		}
		v.Signals += s.Signals
		v.CompletedShadowObservations += s.CompletedShadowObservations
		v.Wins += s.Wins
		v.Losses += s.Losses
		v.Flat += s.Flat
		v.GrossPnLMinor += s.GrossPnLMinor
		if s.MaximumFavorableExcursionMinor > v.MFEChangeMinor {
			v.MFEChangeMinor = s.MaximumFavorableExcursionMinor
		}
		if s.MaximumAdverseExcursionMinor < v.MAEChangeMinor {
			v.MAEChangeMinor = s.MaximumAdverseExcursionMinor
		}
		if s.MaximumObservedDrawdownMinor > v.MaximumDrawdownMinor {
			v.MaximumDrawdownMinor = s.MaximumObservedDrawdownMinor
		}
		v.DataQualityFailures += s.DataQualityFailures
		v.StaleBlocks += s.StaleBlocks
		v.MappingBlocks += s.MappingBlocks
		v.CASBlocks += s.CASBlocks
		v.SessionBlocks += s.SessionBlocks
		for k, n := range s.RegimeDistribution {
			v.RegimeDistribution[k] += n
		}
	}
	return []MultiSessionScorecard{*result[qualification.NIFTY], *result[qualification.BANKNIFTY]}
}

func cloneIntMap(value map[string]int64) map[string]int64 {
	result := map[string]int64{}
	for k, v := range value {
		result[k] = v
	}
	return result
}

func identity(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
