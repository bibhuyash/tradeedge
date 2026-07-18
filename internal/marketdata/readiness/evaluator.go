package readiness

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/telemetry"
)

type Clock interface {
	Now() time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

type observedEvent struct {
	exchange time.Time
	ingested time.Time
}

type missingWindow struct {
	open  time.Time
	close time.Time
}

type streamKey struct {
	provider     domain.Provider
	instrumentID domain.InstrumentID
	kind         model.EventKind
	interval     model.CandleInterval
}

type Evaluator struct {
	mu         sync.RWMutex
	clock      Clock
	calendar   calendar.Calendar
	policy     FreshnessPolicy
	watchlists []Watchlist
	observed   map[streamKey]observedEvent
	candles    map[streamKey]map[int64]observedEvent
	missing    map[streamKey]map[int64]missingWindow
	providers  map[domain.Provider]bool
	recorder   telemetry.Recorder
}

func New(
	clock Clock,
	schedule calendar.Calendar,
	policy FreshnessPolicy,
	watchlists []Watchlist,
) (*Evaluator, error) {
	return NewWithTelemetry(clock, schedule, policy, watchlists, telemetry.NopRecorder{})
}

func NewWithTelemetry(
	clock Clock,
	schedule calendar.Calendar,
	policy FreshnessPolicy,
	watchlists []Watchlist,
	recorder telemetry.Recorder,
) (*Evaluator, error) {
	if clock == nil {
		return nil, ErrInvalidPolicy
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(watchlists))
	for _, watchlist := range watchlists {
		if watchlist.ID == "" || watchlist.Version == "" || len(watchlist.Requirements) == 0 {
			return nil, ErrInvalidWatchlist
		}
		if _, exists := seen[watchlist.ID]; exists {
			return nil, ErrInvalidWatchlist
		}
		seen[watchlist.ID] = struct{}{}
	}
	if recorder == nil {
		recorder = telemetry.NopRecorder{}
	}
	return &Evaluator{
		clock: clock, calendar: schedule, policy: policy,
		watchlists: append([]Watchlist(nil), watchlists...),
		observed:   make(map[streamKey]observedEvent),
		candles:    make(map[streamKey]map[int64]observedEvent),
		missing:    make(map[streamKey]map[int64]missingWindow),
		providers:  make(map[domain.Provider]bool),
		recorder:   recorder,
	}, nil
}

func (e *Evaluator) Accepted(event model.Event) {
	if event == nil {
		return
	}
	key := streamKey{
		provider: event.Provenance().Provider, instrumentID: event.InstrumentID(), kind: event.Kind(),
	}
	if candle, ok := event.(model.CompletedCandleEvent); ok {
		key.interval = candle.Interval()
	}
	value := observedEvent{exchange: event.ExchangeTime(), ingested: event.IngestedAt()}
	e.mu.Lock()
	defer e.mu.Unlock()
	current, found := e.observed[key]
	if !found || value.exchange.After(current.exchange) ||
		(value.exchange.Equal(current.exchange) && value.ingested.After(current.ingested)) {
		e.observed[key] = value
	}
	if candle, ok := event.(model.CompletedCandleEvent); ok {
		if e.candles[key] == nil {
			e.candles[key] = make(map[int64]observedEvent)
		}
		e.candles[key][candle.CloseTime().UnixNano()] = value
		if e.missing[key] != nil {
			delete(e.missing[key], candle.CloseTime().UnixNano())
		}
	}
	e.providers[key.provider] = true
}

func (e *Evaluator) Quality(record model.QualityRecord) {}

func (e *Evaluator) SetProviderAvailable(provider domain.Provider, available bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.providers[provider] = available
}

func (e *Evaluator) MarkMissing(
	provider domain.Provider,
	instrumentID domain.InstrumentID,
	interval model.CandleInterval,
	open time.Time,
	closeTime time.Time,
) {
	key := streamKey{
		provider: provider, instrumentID: instrumentID,
		kind: model.EventKindCandle, interval: interval,
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.missing[key] == nil {
		e.missing[key] = make(map[int64]missingWindow)
	}
	e.missing[key][closeTime.UnixNano()] = missingWindow{open: open, close: closeTime}
}

func (e *Evaluator) Snapshot(ctx context.Context) Snapshot {
	snapshot := e.snapshot(ctx)
	e.recordSnapshot(snapshot)
	return snapshot
}

func (e *Evaluator) snapshot(ctx context.Context) Snapshot {
	now := e.clock.Now().UTC()
	if len(e.watchlists) == 0 {
		return Snapshot{
			EvaluatedAt: now, PolicyVersion: e.policy.Version,
			State: StateDisabled, Reasons: []ReasonCode{ReasonMarketDataDisabled},
		}
	}
	if e.calendar == nil {
		return Snapshot{
			EvaluatedAt: now, PolicyVersion: e.policy.Version,
			State: StateUnknown, Reasons: []ReasonCode{ReasonCalendarUnavailable},
		}
	}
	snapshot := Snapshot{
		EvaluatedAt: now, CalendarVersion: string(e.calendar.Version()), PolicyVersion: e.policy.Version,
	}
	watchlistDiagnostics := make(map[string][]Diagnostic, len(e.watchlists))
	providerDiagnostics := make(map[domain.Provider][]Diagnostic)
	for _, watchlist := range e.watchlists {
		for _, requirement := range watchlist.Requirements {
			diagnostic := e.evaluateRequirement(ctx, now, watchlist.ID, requirement)
			snapshot.Diagnostics = append(snapshot.Diagnostics, diagnostic)
			watchlistDiagnostics[watchlist.ID] = append(watchlistDiagnostics[watchlist.ID], diagnostic)
			providerDiagnostics[requirement.Provider] = append(providerDiagnostics[requirement.Provider], diagnostic)
		}
	}
	for id, diagnostics := range watchlistDiagnostics {
		snapshot.Watchlists = append(snapshot.Watchlists, aggregateScope(id, diagnostics))
	}
	for provider, diagnostics := range providerDiagnostics {
		snapshot.Providers = append(snapshot.Providers, aggregateScope(string(provider), diagnostics))
	}
	sort.Slice(snapshot.Watchlists, func(i, j int) bool { return snapshot.Watchlists[i].ID < snapshot.Watchlists[j].ID })
	sort.Slice(snapshot.Providers, func(i, j int) bool { return snapshot.Providers[i].ID < snapshot.Providers[j].ID })
	var globalDiagnostics []Diagnostic
	for _, diagnostic := range snapshot.Diagnostics {
		if diagnostic.Required {
			globalDiagnostics = append(globalDiagnostics, diagnostic)
		}
	}
	global := aggregateScope("global", globalDiagnostics)
	snapshot.State = global.State
	snapshot.Reasons = global.Reasons
	snapshot.TradingPermitted = snapshot.State == StateReady
	sort.Slice(snapshot.Diagnostics, func(i, j int) bool {
		if snapshot.Diagnostics[i].WatchlistID != snapshot.Diagnostics[j].WatchlistID {
			return snapshot.Diagnostics[i].WatchlistID < snapshot.Diagnostics[j].WatchlistID
		}
		return snapshot.Diagnostics[i].Instrument < snapshot.Diagnostics[j].Instrument
	})
	return snapshot
}

func (e *Evaluator) recordSnapshot(snapshot Snapshot) {
	coverage := 0.0
	for _, scope := range snapshot.Watchlists {
		coverage += scope.CoverageRatio
	}
	if len(snapshot.Watchlists) > 0 {
		coverage /= float64(len(snapshot.Watchlists))
	}
	e.recorder.Readiness("global", "", "", string(snapshot.State), firstReason(snapshot.Reasons),
		snapshot.State == StateReady, coverage)
	for _, scope := range snapshot.Watchlists {
		e.recorder.Readiness("watchlist", "", scope.ID, string(scope.State), firstReason(scope.Reasons),
			scope.State == StateReady, scope.CoverageRatio)
	}
	for _, scope := range snapshot.Providers {
		e.recorder.Readiness("provider", scope.ID, "", string(scope.State), firstReason(scope.Reasons),
			scope.State == StateReady, scope.CoverageRatio)
	}
}

func firstReason(reasons []ReasonCode) string {
	if len(reasons) == 0 {
		return string(ReasonNone)
	}
	return string(reasons[0])
}

func (e *Evaluator) evaluateRequirement(
	ctx context.Context,
	now time.Time,
	watchlistID string,
	requirement Requirement,
) Diagnostic {
	diagnostic := Diagnostic{
		WatchlistID: watchlistID, Provider: requirement.Provider,
		InstrumentID: requirement.InstrumentID, Instrument: requirement.InstrumentID.String(),
		Exchange: requirement.Exchange, Segment: requirement.Segment,
		EventKind: requirement.EventKind, Interval: requirement.Interval, Required: requirement.Required,
		State: StateUnknown, Reason: ReasonCalendarUnavailable,
	}
	e.mu.RLock()
	available, providerKnown := e.providers[requirement.Provider]
	e.mu.RUnlock()
	if providerKnown && !available {
		diagnostic.Reason = ReasonProviderUnavailable
		return diagnostic
	}
	location, err := time.LoadLocation(e.calendar.Timezone())
	if err != nil {
		return diagnostic
	}
	local := now.In(location)
	date, err := domain.NewCivilDate(local.Year(), local.Month(), local.Day())
	if err != nil {
		return diagnostic
	}
	day, err := e.calendar.Day(ctx, requirement.Exchange, date)
	if err != nil {
		if errors.Is(err, calendar.ErrCalendarOutOfRange) ||
			errors.Is(err, calendar.ErrTradingDayNotFound) {
			diagnostic.Reason = ReasonCalendarOutOfRange
		}
		return diagnostic
	}
	if day.Status == calendar.DayHoliday {
		diagnostic.State, diagnostic.Reason = StateSessionClosed, ReasonHoliday
		return diagnostic
	}
	session, active, phaseReason := sessionPhase(day, now)
	key := streamKey{
		provider: requirement.Provider, instrumentID: requirement.InstrumentID,
		kind: requirement.EventKind, interval: requirement.Interval,
	}
	e.mu.RLock()
	latest, found := e.observed[key]
	e.mu.RUnlock()
	if found {
		diagnostic.LastExchange = latest.exchange
		diagnostic.LastIngested = latest.ingested
	}
	if requirement.EventKind == model.EventKindQuote {
		if !active {
			diagnostic.State, diagnostic.Reason = StateSessionClosed, phaseReason
			return diagnostic
		}
		if now.Before(session.Open.Add(e.policy.Quote.Warmup)) {
			diagnostic.State, diagnostic.Reason = StateWarmingUp, ReasonWarmupActive
			return diagnostic
		}
		if !found {
			diagnostic.State, diagnostic.Reason = StateNoData, ReasonNoAcceptedEvent
			return diagnostic
		}
		return evaluateAges(diagnostic, now, latest, e.policy.Quote, e.policy.ClockSkewTolerance)
	}
	streamPolicy, policyFound := e.policy.Candles[requirement.Interval]
	if !policyFound {
		diagnostic.Reason = ReasonPolicyInvalid
		return diagnostic
	}
	windows, err := e.calendar.ExpectedWindows(ctx, requirement.Exchange, date, requirement.Interval)
	if err != nil {
		return diagnostic
	}
	var expected *calendar.Window
	for index := range windows {
		if !now.Before(windows[index].Close.Add(streamPolicy.CompletionGrace)) {
			value := windows[index]
			expected = &value
		}
	}
	if expected == nil {
		if active {
			diagnostic.State, diagnostic.Reason = StateWarmingUp, ReasonWarmupActive
		} else {
			diagnostic.State, diagnostic.Reason = StateSessionClosed, phaseReason
		}
		return diagnostic
	}
	e.mu.RLock()
	candle, candleFound := e.candles[key][expected.Close.UnixNano()]
	var missing missingWindow
	for _, candidate := range e.missing[key] {
		if candidate.open.Before(day.Sessions[0].Open) ||
			candidate.close.After(expected.Close) {
			continue
		}
		if missing.close.IsZero() || candidate.open.Before(missing.open) {
			missing = candidate
		}
	}
	e.mu.RUnlock()
	if !candleFound || !missing.close.IsZero() {
		diagnostic.State, diagnostic.Reason = StateIncomplete, ReasonMissingCandle
		diagnostic.MissingOpen, diagnostic.MissingClose = expected.Open, expected.Close
		if !missing.close.IsZero() {
			diagnostic.MissingOpen, diagnostic.MissingClose = missing.open, missing.close
		}
		return diagnostic
	}
	diagnostic.LastExchange, diagnostic.LastIngested = candle.exchange, candle.ingested
	if candle.ingested.Sub(candle.exchange) > streamPolicy.CompletionGrace {
		diagnostic.State, diagnostic.Reason = StateStale, ReasonTransportLagExceeded
		return diagnostic
	}
	if !active {
		diagnostic.State, diagnostic.Reason = StateSessionClosed, phaseReason
		return diagnostic
	}
	diagnostic.State, diagnostic.Reason = StateReady, ReasonNone
	return diagnostic
}

func evaluateAges(
	diagnostic Diagnostic,
	now time.Time,
	latest observedEvent,
	policy StreamPolicy,
	tolerance time.Duration,
) Diagnostic {
	exchangeAge := now.Sub(latest.exchange)
	ingestionAge := now.Sub(latest.ingested)
	transportLag := latest.ingested.Sub(latest.exchange)
	switch {
	case exchangeAge < -tolerance || ingestionAge < -tolerance || transportLag < -tolerance:
		diagnostic.State, diagnostic.Reason = StateStale, ReasonClockSkew
	case exchangeAge > policy.MaxExchangeAge:
		diagnostic.State, diagnostic.Reason = StateStale, ReasonExchangeTimeStale
	case ingestionAge > policy.MaxIngestionAge:
		diagnostic.State, diagnostic.Reason = StateStale, ReasonIngestionTimeStale
	case transportLag > policy.MaxTransportLag:
		diagnostic.State, diagnostic.Reason = StateStale, ReasonTransportLagExceeded
	default:
		diagnostic.State, diagnostic.Reason = StateReady, ReasonNone
	}
	return diagnostic
}

func sessionPhase(day calendar.TradingDay, now time.Time) (calendar.Session, bool, ReasonCode) {
	for index, session := range day.Sessions {
		if !now.Before(session.Open) && now.Before(session.Close) {
			return session, true, ReasonNone
		}
		if now.Before(session.Open) {
			if index == 0 {
				return session, false, ReasonBeforeOpen
			}
			return session, false, ReasonBetweenSessions
		}
	}
	return day.Sessions[len(day.Sessions)-1], false, ReasonAfterClose
}

func aggregateScope(id string, diagnostics []Diagnostic) ScopeSnapshot {
	result := ScopeSnapshot{ID: id, State: StateSessionClosed}
	reasons := make(map[ReasonCode]struct{})
	for _, diagnostic := range diagnostics {
		if !diagnostic.Required {
			continue
		}
		result.Required++
		if diagnostic.State == StateReady || diagnostic.State == StateSessionClosed {
			result.Covered++
		}
		if statePriority(diagnostic.State) > statePriority(result.State) {
			result.State = diagnostic.State
		}
		if diagnostic.Reason != ReasonNone {
			reasons[diagnostic.Reason] = struct{}{}
		}
	}
	if result.Required == 0 {
		result.State = StateUnknown
		reasons[ReasonCoverageIncomplete] = struct{}{}
	} else {
		result.CoverageRatio = float64(result.Covered) / float64(result.Required)
	}
	for reason := range reasons {
		result.Reasons = append(result.Reasons, reason)
	}
	sort.Slice(result.Reasons, func(i, j int) bool { return result.Reasons[i] < result.Reasons[j] })
	return result
}

func statePriority(state State) int {
	switch state {
	case StateUnknown:
		return 70
	case StateIncomplete:
		return 60
	case StateNoData:
		return 50
	case StateStale:
		return 40
	case StateWarmingUp:
		return 30
	case StateReady:
		return 20
	case StateSessionClosed:
		return 10
	case StateDisabled:
		return 0
	default:
		return 70
	}
}
