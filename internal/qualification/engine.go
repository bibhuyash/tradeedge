package qualification

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/notification"
)

type Observer interface{ Observe(notification.Event) }

type Engine struct {
	mu       sync.RWMutex
	policy   Policy
	series   map[Underlying]*Series
	revision uint64
	observer Observer
}

func New(policy Policy, observer Observer) (*Engine, error) {
	if policy.validate() != nil {
		return nil, ErrInvalid
	}
	series := map[Underlying]*Series{}
	for _, underlying := range []Underlying{NIFTY, BANKNIFTY} {
		series[underlying] = &Series{StrategyID: StrategyID, StrategyVersion: StrategyVersion, Underlying: underlying, State: StateReference}
	}
	return &Engine{policy: policy, series: series, observer: observer}, nil
}

func (e *Engine) RecordSignal(input SignalInput) (SignalRecord, error) {
	if err := validateSignal(input); err != nil {
		return SignalRecord{}, err
	}
	regime, err := ClassifyRegime(input.RegimeInput)
	if err != nil {
		return SignalRecord{}, err
	}
	entry, err := selectPrice(input.OptionQuote, true)
	if err != nil {
		return SignalRecord{}, err
	}
	id := identity(SchemaVersion, input.StrategyID, input.StrategyVersion, string(input.Underlying), input.SignalID, input.SignalTime.UTC().Format(time.RFC3339Nano), input.OptionID, strconv.FormatInt(entry.PriceMinor, 10), string(input.Risk))
	record := SignalRecord{QualificationID: id, StrategyID: input.StrategyID, StrategyVersion: input.StrategyVersion, Underlying: input.Underlying, SignalID: input.SignalID, SignalTime: input.SignalTime.UTC(), MarketSession: input.MarketSession, CASState: input.CASState, SpotMinor: input.SpotMinor, SpotTime: input.SpotTime.UTC(), FutureID: input.FutureID, FutureExpiry: input.FutureExpiry, FutureMinor: input.FutureMinor, BasisMinor: input.FutureMinor - input.SpotMinor, FutureTime: input.FutureTime.UTC(), OptionID: input.OptionID, OptionExpiry: input.OptionExpiry, StrikeMinor: input.StrikeMinor, OptionType: input.OptionType, OptionQuote: input.OptionQuote, EMA20Scaled: input.EMA20Scaled, EMA50Scaled: input.EMA50Scaled, WarmupComplete: input.WarmupComplete, Fresh: input.Fresh, Direction: input.Direction, Risk: input.Risk, RiskDecisionID: input.RiskDecisionID, RiskReason: input.RiskReason, Mode: "SHADOW", Regime: regime, Entry: entry, Quality: entry.Quality}
	if entry.Quality == QualityPartial {
		record.QualityReasons = []UnavailableReason{ReasonLTPOnly}
	}
	for _, horizon := range e.policy.Horizons {
		record.Horizons = append(record.Horizons, HorizonMeasurement{HorizonSeconds: int64(horizon / time.Second), DueAt: input.SignalTime.Add(horizon).UTC(), UnavailableReason: ReasonHorizonUnavailable})
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	series := e.series[input.Underlying]
	for _, existing := range series.Records {
		if existing.SignalID == record.SignalID && existing.SignalTime.Equal(record.SignalTime) {
			left, right := existing, record
			left.Horizons, right.Horizons = nil, nil
			leftJSON, _ := json.Marshal(left)
			rightJSON, _ := json.Marshal(right)
			if string(leftJSON) != string(rightJSON) {
				return SignalRecord{}, ErrConflict
			}
			return existing, ErrDuplicate
		}
	}
	if input.Risk == RiskApproved && series.Open != nil {
		return SignalRecord{}, ErrOpenPosition
	}
	series.Records = append(series.Records, record)
	series.State = StateShadowCollecting
	if input.Risk == RiskRejected {
		series.State = StateInsufficientSample
		e.revision++
		e.emit(record, notification.KindRiskRejected, "risk rejected shadow signal", input.RiskReason)
		return record, nil
	}
	series.Open = &ShadowQualificationPosition{QualificationID: id, Underlying: input.Underlying, OptionID: input.OptionID, Quantity: input.Quantity, EntryMinor: entry.PriceMinor, EntryTime: input.SignalTime.UTC(), CurrentMarkMinor: entry.PriceMinor}
	e.revision++
	e.emit(record, notification.KindShadowQualification, "SHADOW qualification recorded; Broker Order: NONE", "SHADOW_COLLECTING")
	return record, nil
}

func validateSignal(input SignalInput) error {
	if input.StrategyID != StrategyID || input.StrategyVersion != StrategyVersion || !input.Underlying.valid() || strings.TrimSpace(input.SignalID) == "" || input.SignalTime.IsZero() || input.MarketSession != "NORMAL_TRADING" || input.CASState != "PERMITTED" || input.SpotMinor <= 0 || input.SpotTime.IsZero() || input.FutureID == "" || input.FutureExpiry == "" || input.FutureMinor <= 0 || input.FutureTime.IsZero() || input.OptionID == "" || input.OptionExpiry == "" || input.StrikeMinor <= 0 || (input.OptionType != "CALL" && input.OptionType != "PUT") || input.OptionQuote.InstrumentID != input.OptionID || input.EMA20Scaled <= 0 || input.EMA50Scaled <= 0 || !input.WarmupComplete || !input.Fresh || input.Direction != DirectionLong || (input.Risk != RiskApproved && input.Risk != RiskRejected) || input.Quantity <= 0 {
		return ErrInvalid
	}
	if input.SpotTime.After(input.SignalTime) || input.FutureTime.After(input.SignalTime) || input.OptionQuote.ObservedAt.After(input.SignalTime) {
		return ErrInvalid
	}
	return nil
}

type Observation struct {
	Underlying             Underlying
	QualificationID        string
	ObservedAt             time.Time
	SpotMinor, FutureMinor int64
	OptionQuote            Quote
	Quality                Quality
	UnavailableReason      UnavailableReason
}

func (e *Engine) Observe(input Observation) error {
	if !input.Underlying.valid() || input.QualificationID == "" || input.ObservedAt.IsZero() || input.SpotMinor <= 0 || input.FutureMinor <= 0 || input.Quality == QualityInvalid {
		return ErrInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	series := e.series[input.Underlying]
	if series.Open == nil || series.Open.QualificationID != input.QualificationID {
		return ErrNoPosition
	}
	position := series.Open
	if input.OptionQuote.InstrumentID != position.OptionID || input.ObservedAt.Before(position.EntryTime) || input.OptionQuote.ObservedAt.After(input.ObservedAt) {
		return ErrInvalid
	}
	observationID := identity(input.QualificationID, input.ObservedAt.UTC().Format(time.RFC3339Nano), strconv.FormatInt(input.SpotMinor, 10), strconv.FormatInt(input.FutureMinor, 10), strconv.FormatInt(input.OptionQuote.BidMinor, 10), strconv.FormatInt(input.OptionQuote.AskMinor, 10), strconv.FormatInt(input.OptionQuote.LTPMinor, 10), string(input.Quality))
	if !position.LastObservationAt.IsZero() && !input.ObservedAt.After(position.LastObservationAt) {
		if input.ObservedAt.Equal(position.LastObservationAt) && observationID == position.LastObservationID {
			return ErrDuplicate
		}
		return ErrConflict
	}
	mark, err := selectPrice(input.OptionQuote, false)
	if err != nil {
		return err
	}
	change := mark.PriceMinor - position.EntryMinor
	if change > position.MFEChangeMinor {
		position.MFEChangeMinor, position.MFEAt = change, input.ObservedAt.UTC()
	}
	if change < position.MAEChangeMinor {
		position.MAEChangeMinor, position.MAEAt = change, input.ObservedAt.UTC()
	}
	position.CurrentMarkMinor = mark.PriceMinor
	position.LastObservationAt, position.LastObservationID = input.ObservedAt.UTC(), observationID
	for recordIndex := range series.Records {
		record := &series.Records[recordIndex]
		if record.QualificationID != input.QualificationID {
			continue
		}
		for index := range record.Horizons {
			horizon := &record.Horizons[index]
			if horizon.Available || !horizon.ObservedAt.IsZero() || input.ObservedAt.Before(horizon.DueAt) {
				continue
			}
			if input.ObservedAt.Sub(horizon.DueAt) > e.policy.MaximumObservationLateness {
				horizon.ObservedAt, horizon.UnavailableReason = input.ObservedAt.UTC(), ReasonHorizonUnavailable
				continue
			}
			horizon.Available, horizon.UnavailableReason, horizon.ObservedAt = true, ReasonNone, input.ObservedAt.UTC()
			horizon.SpotMovementMinor = input.SpotMinor - record.SpotMinor
			horizon.FutureMovementMinor = input.FutureMinor - record.FutureMinor
			horizon.OptionMarkMinor, horizon.OptionChangeMinor = mark.PriceMinor, change
			horizon.OptionChangeBPS = change * 10_000 / position.EntryMinor
			horizon.HypotheticalPnLMinor = change * position.Quantity
			horizon.PriceQuality = mark.Quality
		}
	}
	e.revision++
	return nil
}

func (e *Engine) MarkHorizonUnavailable(underlying Underlying, qualificationID string, horizon time.Duration, observedAt time.Time, reason UnavailableReason) error {
	if !underlying.valid() || qualificationID == "" || horizon <= 0 || observedAt.IsZero() || (reason != ReasonSessionEnded && reason != ReasonRestartGap && reason != ReasonHorizonUnavailable) {
		return ErrInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	series := e.series[underlying]
	for recordIndex := range series.Records {
		record := &series.Records[recordIndex]
		if record.QualificationID != qualificationID {
			continue
		}
		for index := range record.Horizons {
			value := &record.Horizons[index]
			if time.Duration(value.HorizonSeconds)*time.Second == horizon && !value.Available && value.ObservedAt.IsZero() {
				value.ObservedAt, value.UnavailableReason = observedAt.UTC(), reason
				e.revision++
				return nil
			}
		}
	}
	return ErrNotFound
}

type ExitInput struct {
	Underlying                Underlying
	QualificationID, OptionID string
	SignalTime                time.Time
	OptionQuote               Quote
}

func (e *Engine) Close(input ExitInput) (ShadowTrade, error) {
	if !input.Underlying.valid() || input.QualificationID == "" || input.OptionID == "" || input.SignalTime.IsZero() || input.OptionQuote.InstrumentID != input.OptionID {
		return ShadowTrade{}, ErrInvalid
	}
	exit, err := selectPrice(input.OptionQuote, false)
	if err != nil {
		return ShadowTrade{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	series := e.series[input.Underlying]
	if series.Open == nil || series.Open.QualificationID != input.QualificationID {
		return ShadowTrade{}, ErrNoPosition
	}
	position := series.Open
	if position.OptionID != input.OptionID || !input.SignalTime.After(position.EntryTime) || input.OptionQuote.ObservedAt.After(input.SignalTime) {
		return ShadowTrade{}, ErrInvalid
	}
	exitChange := exit.PriceMinor - position.EntryMinor
	if exitChange > position.MFEChangeMinor {
		position.MFEChangeMinor, position.MFEAt = exitChange, input.SignalTime.UTC()
	}
	if exitChange < position.MAEChangeMinor {
		position.MAEChangeMinor, position.MAEAt = exitChange, input.SignalTime.UTC()
	}
	var regime Regime
	for _, record := range series.Records {
		if record.QualificationID == input.QualificationID {
			regime = record.Regime
			break
		}
	}
	trade := ShadowTrade{QualificationID: input.QualificationID, Underlying: input.Underlying, OptionID: input.OptionID, Quantity: position.Quantity, EntryMinor: position.EntryMinor, ExitMinor: exit.PriceMinor, EntryTime: position.EntryTime, ExitTime: input.SignalTime.UTC(), HoldingSeconds: int64(input.SignalTime.Sub(position.EntryTime) / time.Second), GrossPnLMinor: (exit.PriceMinor - position.EntryMinor) * position.Quantity, MFEChangeMinor: position.MFEChangeMinor, MAEChangeMinor: position.MAEChangeMinor, MFEAt: position.MFEAt, MAEAt: position.MAEAt, Regime: regime}
	if e.policy.Cost.Configured {
		trade.NetPnLAvailable = true
		trade.NetPnLMinor = trade.GrossPnLMinor - e.policy.Cost.totalMinor()
	}
	series.Trades = append(series.Trades, trade)
	series.Open = nil
	series.State = e.qualificationState(*series)
	e.revision++
	var record SignalRecord
	for _, value := range series.Records {
		if value.QualificationID == input.QualificationID {
			record = value
			break
		}
	}
	e.emit(record, notification.KindShadowQualificationResult, fmt.Sprintf("SHADOW result gross_pnl_minor=%d MFE=%d MAE=%d; Broker Order: NONE", trade.GrossPnLMinor, trade.MFEChangeMinor, trade.MAEChangeMinor), "NOT_ALPHA_QUALIFIED")
	return trade, nil
}

type BlockInput struct {
	Underlying Underlying
	Reason     UnavailableReason
}

func (e *Engine) RecordBlock(input BlockInput) error {
	if !input.Underlying.valid() {
		return ErrInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	blocks := &e.series[input.Underlying].Blocks
	switch input.Reason {
	case ReasonStaleSpot, ReasonStaleFuture, ReasonStaleOption:
		blocks.StaleDataBlocks++
	case ReasonMappingBlocked:
		blocks.MappingBlocks++
	case ReasonCASBlocked:
		blocks.CASBlocks++
	case ReasonSessionBlocked:
		blocks.SessionBlocks++
	case ReasonControlBlocked:
		blocks.ControlBlocks++
	default:
		blocks.DataQualityFailures++
	}
	e.revision++
	return nil
}

func (e *Engine) qualificationState(series Series) QualificationState {
	if uint64(len(series.Trades)) < e.policy.MinimumCompletedTrades || completedSessions(series.Trades) < e.policy.MinimumSessions {
		return StateInsufficientSample
	}
	return StateEligibleForReview
}

// ApplyReview is the only transition to QUALIFIED or REJECTED. It grants no
// execution or capital authority and is unavailable before sample eligibility.
func (e *Engine) ApplyReview(input ReviewDecision) error {
	if !input.Underlying.valid() || strings.TrimSpace(input.Operator) == "" || strings.TrimSpace(input.Reference) == "" || input.At.IsZero() {
		return ErrInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	series := e.series[input.Underlying]
	if series.State != StateEligibleForReview {
		return ErrInvalid
	}
	if input.Approved {
		series.State = StateQualified
	} else {
		series.State = StateRejected
	}
	input.At = input.At.UTC()
	series.Reviews = append(series.Reviews, input)
	e.revision++
	return nil
}

func (e *Engine) Snapshot() Snapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	value := Snapshot{Revision: e.revision, Policy: e.policy}
	for _, underlying := range []Underlying{NIFTY, BANKNIFTY} {
		value.Series = append(value.Series, cloneSeries(*e.series[underlying]))
	}
	value, _ = finalizeSnapshot(value)
	return value
}

func (e *Engine) Restore(snapshot Snapshot) error {
	if snapshot.Verify() != nil || snapshot.Policy.validate() != nil {
		return ErrCorrupt
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(snapshot.Series) != 2 {
		return ErrCorrupt
	}
	restored := map[Underlying]*Series{}
	for _, value := range snapshot.Series {
		if !value.Underlying.valid() || restored[value.Underlying] != nil || value.StrategyID != StrategyID || value.StrategyVersion != StrategyVersion {
			return ErrCorrupt
		}
		copy := cloneSeries(value)
		restored[value.Underlying] = &copy
	}
	if restored[NIFTY] == nil || restored[BANKNIFTY] == nil {
		return ErrCorrupt
	}
	e.policy, e.series, e.revision = snapshot.Policy, restored, snapshot.Revision
	return nil
}

func (e *Engine) Scorecards() []Scorecard {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return []Scorecard{e.scorecard(*e.series[NIFTY]), e.scorecard(*e.series[BANKNIFTY])}
}

func (e *Engine) Strategy(strategy string, underlying Underlying) (Series, Scorecard, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if strategy != StrategyID || !underlying.valid() {
		return Series{}, Scorecard{}, ErrNotFound
	}
	value := cloneSeries(*e.series[underlying])
	return value, e.scorecard(value), nil
}

func (e *Engine) RecentSignals(limit int) []SignalRecord {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var values []SignalRecord
	for _, series := range e.series {
		values = append(values, series.Records...)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].SignalTime.Equal(values[j].SignalTime) {
			return values[i].QualificationID < values[j].QualificationID
		}
		return values[i].SignalTime.After(values[j].SignalTime)
	})
	if len(values) > limit {
		values = values[:limit]
	}
	return values
}

func (e *Engine) scorecard(series Series) Scorecard {
	score := Scorecard{StrategyID: series.StrategyID, StrategyVersion: series.StrategyVersion, Underlying: series.Underlying, State: series.State, Signals: uint64(len(series.Records)), CompletedTrades: uint64(len(series.Trades)), BlockCounters: series.Blocks, MinimumCompletedTrades: e.policy.MinimumCompletedTrades, MinimumSessions: e.policy.MinimumSessions, DirectionalOutcomeBPS: map[string]int64{}}
	if series.Open != nil {
		score.OpenObservations = 1
	}
	for _, record := range series.Records {
		if record.Risk == RiskApproved {
			score.AcceptedSignals++
		} else {
			score.RiskRejectedSignals++
		}
		for _, horizon := range record.Horizons {
			if horizon.Available && horizon.OptionChangeMinor > 0 {
				score.DirectionalOutcomeBPS[horizonLabel(horizon.HorizonSeconds)]++
			}
		}
	}
	var pnls []int64
	var wins, losses int64
	var holding int64
	var peak, running, drawdown int64
	for _, trade := range series.Trades {
		pnl := trade.GrossPnLMinor
		pnls = append(pnls, pnl)
		score.GrossPnLMinor += pnl
		holding += trade.HoldingSeconds
		if pnl > 0 {
			score.Wins++
			wins += pnl
		} else if pnl < 0 {
			score.Losses++
			losses += -pnl
		} else {
			score.Flat++
		}
		if trade.MFEChangeMinor > score.MaximumFavorableExcursionMinor {
			score.MaximumFavorableExcursionMinor = trade.MFEChangeMinor
		}
		if trade.MAEChangeMinor < score.MaximumAdverseExcursionMinor {
			score.MaximumAdverseExcursionMinor = trade.MAEChangeMinor
		}
		running += pnl
		if running > peak {
			peak = running
		}
		if peak-running > drawdown {
			drawdown = peak - running
		}
		if trade.NetPnLAvailable {
			score.NetPnLAvailable = true
			score.NetPnLMinor += trade.NetPnLMinor
		} else {
			score.NetPnLAvailable = false
		}
	}
	if len(pnls) > 0 {
		score.WinRateBPS = int64(score.Wins) * 10_000 / int64(len(pnls))
		score.AveragePnLMinor = score.GrossPnLMinor / int64(len(pnls))
		score.AverageHoldingSeconds = holding / int64(len(pnls))
		sort.Slice(pnls, func(i, j int) bool { return pnls[i] < pnls[j] })
		mid := len(pnls) / 2
		if len(pnls)%2 == 1 {
			score.MedianPnLMinor = pnls[mid]
		} else {
			score.MedianPnLMinor = (pnls[mid-1] + pnls[mid]) / 2
		}
	}
	if score.Wins > 0 {
		score.AverageWinnerMinor = wins / int64(score.Wins)
	}
	if score.Losses > 0 {
		score.AverageLoserMinor = -(losses / int64(score.Losses))
		score.ProfitFactorAvailable = true
		score.ProfitFactorBPS = wins * 10_000 / losses
	}
	score.MaximumObservedDrawdownMinor = drawdown
	score.CompletedSessions = completedSessions(series.Trades)
	for _, seconds := range []int64{60, 300, 900, 1800} {
		label := horizonLabel(seconds)
		available := int64(0)
		positive := score.DirectionalOutcomeBPS[label]
		for _, r := range series.Records {
			for _, h := range r.Horizons {
				if h.HorizonSeconds == seconds && h.Available {
					available++
				}
			}
		}
		if available > 0 {
			score.DirectionalOutcomeBPS[label] = positive * 10_000 / available
		}
	}
	return score
}

func completedSessions(trades []ShadowTrade) uint64 {
	seen := map[string]bool{}
	for _, t := range trades {
		seen[t.EntryTime.UTC().Format("2006-01-02")] = true
	}
	return uint64(len(seen))
}
func horizonLabel(seconds int64) string { return "+" + strconv.FormatInt(seconds/60, 10) + "m" }
func cloneSeries(value Series) Series {
	raw, _ := json.Marshal(value)
	var copy Series
	_ = json.Unmarshal(raw, &copy)
	return copy
}

func (e *Engine) emit(record SignalRecord, kind notification.Kind, subject, state string) {
	if e.observer == nil || record.QualificationID == "" {
		return
	}
	event, err := notification.NewEvent(notification.EventSpec{SourceID: record.QualificationID + "|" + string(kind), TradingDate: record.SignalTime.UTC().Format("2006-01-02"), Mode: "SHADOW", OccurredAt: record.SignalTime, Category: notification.CategoryStrategy, Kind: kind, Severity: notification.SeverityInfo, Details: notification.Details{Subject: subject, State: state, InstrumentID: record.OptionID, StrategyID: record.StrategyID, ReferenceID: record.QualificationID, PriceMinor: record.Entry.PriceMinor, Currency: "INR"}})
	if err != nil {
		return
	}
	func() { defer func() { _ = recover() }(); e.observer.Observe(event) }()
}
