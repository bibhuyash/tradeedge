package shadowruntime

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/derivatives"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/notification"
	"github.com/bibhuyash/tradeedge/internal/qualification"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
)

const (
	MaximumSubscriptions = 16
	MinimumRemapInterval = 5 * time.Minute
)

type RiskGateway interface {
	Evaluate(context.Context, derivatives.ConnectedRequest) (riskmodel.PortfolioRiskDecision, error)
}

type RuntimeConfig struct {
	Master            instrumentmaster.Master
	SpotIDs           map[qualification.Underlying]domain.InstrumentID
	Policies          map[qualification.Underlying]derivatives.Policy
	Qualification     *qualification.Engine
	Risk              RiskGateway
	Observer          interface{ Observe(notification.Event) }
	TradingDate       string
	StartedAt         time.Time
	TelegramAvailable bool
}

type UnderlyingStatus struct {
	Underlying     qualification.Underlying `json:"underlying"`
	MarketData     string                   `json:"market_data"`
	Future         string                   `json:"future"`
	OptionUniverse string                   `json:"option_universe"`
	Strategy       string                   `json:"strategy"`
	WarmupSamples  int                      `json:"warmup_samples"`
	WarmupRequired int                      `json:"warmup_required"`
	SelectedFuture string                   `json:"selected_future,omitempty"`
	SelectedOption string                   `json:"selected_option,omitempty"`
	LastRisk       string                   `json:"last_risk,omitempty"`
	LastRiskReason string                   `json:"last_risk_reason,omitempty"`
	LastSignalID   string                   `json:"last_signal_id,omitempty"`
}

type Snapshot struct {
	SchemaVersion string                                 `json:"schema_version"`
	Revision      uint64                                 `json:"revision"`
	TradingDate   string                                 `json:"trading_date"`
	Candles       []CandleSeriesSnapshot                 `json:"candles"`
	EMA           []EMAState                             `json:"ema"`
	Qualification qualification.Snapshot                 `json:"qualification"`
	Sessions      SessionTracker                         `json:"sessions"`
	Status        []UnderlyingStatus                     `json:"status"`
	LastRemapAt   map[qualification.Underlying]time.Time `json:"last_remap_at"`
	Checksum      string                                 `json:"checksum"`
}

type Runtime struct {
	mu            sync.RWMutex
	master        instrumentmaster.Master
	spots         map[qualification.Underlying]domain.InstrumentID
	policies      map[qualification.Underlying]derivatives.Policy
	aggregator    *CandleAggregator
	ema           map[qualification.Underlying]EMAState
	qualification *qualification.Engine
	risk          RiskGateway
	observer      interface{ Observe(notification.Event) }
	sessions      *SessionTracker
	latest        map[domain.InstrumentID]marketmodel.QuoteEvent
	history       map[domain.InstrumentID][]marketmodel.QuoteEvent
	status        map[qualification.Underlying]UnderlyingStatus
	lastRemap     map[qualification.Underlying]time.Time
	revision      uint64
	tradingDate   string
}

func New(config RuntimeConfig) (*Runtime, error) {
	if config.Master.Version() == "" || len(config.SpotIDs) != 2 || len(config.Policies) != 2 || config.Qualification == nil || config.Risk == nil || config.StartedAt.IsZero() || config.TradingDate == "" {
		return nil, ErrInvalid
	}
	aggregator, err := NewCandleAggregator(config.SpotIDs)
	if err != nil {
		return nil, err
	}
	sessions, err := NewSessionTracker(config.TradingDate, config.StartedAt, config.Qualification.Scorecards(), config.TelegramAvailable)
	if err != nil {
		return nil, err
	}
	r := &Runtime{master: config.Master, spots: config.SpotIDs, policies: config.Policies, aggregator: aggregator, qualification: config.Qualification, risk: config.Risk, observer: config.Observer, sessions: sessions, latest: map[domain.InstrumentID]marketmodel.QuoteEvent{}, history: map[domain.InstrumentID][]marketmodel.QuoteEvent{}, ema: map[qualification.Underlying]EMAState{}, status: map[qualification.Underlying]UnderlyingStatus{}, lastRemap: map[qualification.Underlying]time.Time{}, revision: 1, tradingDate: config.TradingDate}
	for _, u := range []qualification.Underlying{qualification.NIFTY, qualification.BANKNIFTY} {
		if config.Policies[u].Underlying != domain.UnderlyingID(u) {
			return nil, ErrInvalid
		}
		r.ema[u] = EMAState{Underlying: u}
		r.status[u] = UnderlyingStatus{Underlying: u, MarketData: "WARMING", Future: "WARMING", OptionUniverse: "WARMING", Strategy: "WARMING", WarmupRequired: WarmupRequired}
	}
	return r, nil
}

func (r *Runtime) Accepted(event marketmodel.Event) {
	quote, ok := event.(marketmodel.QuoteEvent)
	if !ok {
		return
	}
	_ = r.Process(context.Background(), quote, "NORMAL_TRADING", false, false)
}
func (r *Runtime) Quality(record marketmodel.QualityRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	underlying := qualification.Underlying("")
	if instrument, found := r.master.Instrument(record.InstrumentID); found {
		underlying = qualification.Underlying(instrument.UnderlyingID())
	}
	if underlying != qualification.NIFTY && underlying != qualification.BANKNIFTY {
		return
	}
	card := r.sessions.Current[underlying]
	card.DataQualityFailures++
	if record.Code == marketmodel.QualityMissing || record.Code == marketmodel.QualityLate {
		card.DataGaps++
	}
	r.sessions.Current[underlying] = card
	if record.Code == marketmodel.QualityMissing || record.Code == marketmodel.QualityLate {
		r.sessions.AddReason(underlying, ReasonMarketDataGap)
	}
	r.revision++
}

func (r *Runtime) Process(ctx context.Context, quote marketmodel.QuoteEvent, session string, casRestricted, stopNew bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	instrument, found := r.master.Instrument(quote.InstrumentID())
	if !found {
		return ErrInvalid
	}
	if current, ok := r.latest[quote.InstrumentID()]; ok {
		if quote.ID() == current.ID() {
			return ErrDuplicate
		}
		if !quote.ExchangeTime().After(current.ExchangeTime()) {
			return ErrOutOfOrder
		}
	}
	r.latest[quote.InstrumentID()] = quote
	r.history[quote.InstrumentID()] = append(r.history[quote.InstrumentID()], quote)
	if len(r.history[quote.InstrumentID()]) > MaximumCandles {
		r.history[quote.InstrumentID()] = append([]marketmodel.QuoteEvent(nil), r.history[quote.InstrumentID()][len(r.history[quote.InstrumentID()])-MaximumCandles:]...)
	}
	r.revision++
	underlying := qualification.Underlying(instrument.UnderlyingID())
	if underlying != qualification.NIFTY && underlying != qualification.BANKNIFTY {
		return ErrInvalid
	}
	r.observeOpen(underlying, quote)
	if quote.InstrumentID() != r.spots[underlying] {
		r.refreshStatus(underlying, quote.ExchangeTime())
		return nil
	}
	completed, err := r.aggregator.Accept(underlying, quote)
	if err != nil {
		if errors.Is(err, ErrDuplicate) || errors.Is(err, ErrOutOfOrder) {
			return err
		}
		return err
	}
	if completed == nil {
		r.refreshStatus(underlying, quote.ExchangeTime())
		return nil
	}
	series := r.aggregator.series[underlying]
	state, signal, err := EvaluateEMA(r.ema[underlying], series.Completed)
	if err != nil {
		return err
	}
	r.ema[underlying] = state
	r.refreshStatus(underlying, quote.ExchangeTime())
	if signal == nil {
		return nil
	}
	if casRestricted {
		_ = r.qualification.RecordBlock(qualification.BlockInput{Underlying: underlying, Reason: qualification.ReasonCASBlocked})
		r.sessions.AddReason(underlying, ReasonCASRestriction)
		return nil
	}
	if stopNew && signal.Direction == qualification.DirectionLong {
		_ = r.qualification.RecordBlock(qualification.BlockInput{Underlying: underlying, Reason: qualification.ReasonControlBlocked})
		return nil
	}
	return r.processSignal(ctx, *signal, session, casRestricted, stopNew, series.Completed)
}

func (r *Runtime) processSignal(ctx context.Context, signal EMASignal, session string, casRestricted, stopNew bool, candles []CandlePoint) error {
	u := signal.Underlying
	at := time.Unix(0, signal.AtUnixNano).UTC()
	policy := r.policies[u]
	future, err := derivatives.ResolveFuture(r.master, at, policy)
	if err != nil {
		_ = r.qualification.RecordBlock(qualification.BlockInput{Underlying: u, Reason: qualification.ReasonMappingBlocked})
		r.sessions.AddReason(u, ReasonMappingFailure)
		return ErrNotReady
	}
	futureQuote, ok := r.quoteAtOrBefore(future.Instrument.ID(), at)
	if !ok || at.Sub(futureQuote.ExchangeTime()) > policy.MaximumQuoteAge {
		_ = r.qualification.RecordBlock(qualification.BlockInput{Underlying: u, Reason: qualification.ReasonStaleFuture})
		return ErrNotReady
	}
	selection, err := derivatives.Resolve(r.master, at, futureQuote.LastPrice(), domain.OptionCall, policy)
	if err != nil {
		_ = r.qualification.RecordBlock(qualification.BlockInput{Underlying: u, Reason: qualification.ReasonMappingBlocked})
		return ErrNotReady
	}
	option := selection.Option
	if signal.Direction == qualification.DirectionExit {
		series, _, _ := r.qualification.Strategy(qualification.StrategyID, u)
		if series.Open == nil {
			return nil
		}
		openInstrument, found := instrumentByString(r.master, series.Open.OptionID)
		if !found {
			return ErrNotReady
		}
		mapping, mapErr := r.master.ResolveInstrument(policy.Provider, openInstrument.ID(), at)
		if mapErr != nil {
			return ErrNotReady
		}
		option = derivatives.Contract{Instrument: openInstrument, Mapping: mapping, Reason: "retained qualification option", Policy: "same-option-exit/v1"}
		selection.Option = option
	}
	optionQuote, ok := r.quoteAtOrBefore(option.Instrument.ID(), at)
	if !ok {
		return ErrNotReady
	}
	side := domain.SideBuy
	if signal.Direction == qualification.DirectionExit {
		side = domain.SideSell
	}
	priceDecision := derivatives.EvaluateExecutionQuote(option.Instrument, &optionQuote, side, at, policy)
	if !priceDecision.Ready {
		return ErrNotReady
	}
	eventID, _ := marketmodel.NewEventID(signal.SignalID)
	proposal, err := derivatives.NewOptionProposal(derivatives.ProposalInput{SignalID: signal.SignalID, SignalEventID: eventID, At: at, Spot: mustPrice(signal.SpotMinor), Future: future, FuturePrice: futureQuote.LastPrice(), Option: option, OptionPrice: priceDecision.Price, Side: side, FastEMAScaled: signal.EMA20Scaled, SlowEMAScaled: signal.EMA50Scaled, QuantityLots: 1, SizingBPS: 1000, ExistingOption: func() domain.InstrumentID {
		if side == domain.SideSell {
			return option.Instrument.ID()
		}
		return domain.InstrumentID{}
	}()})
	if err != nil {
		return err
	}
	request := derivatives.ConnectedRequest{Mode: derivatives.ConnectedShadow, Proposal: proposal, MasterVersion: r.master.Version(), At: at, Session: session, CASRestricted: casRestricted, StopNewExposure: stopNew, Selection: selection}
	decision, err := r.risk.Evaluate(ctx, request)
	if err != nil {
		_ = r.qualification.RecordBlock(qualification.BlockInput{Underlying: u, Reason: qualification.ReasonControlBlocked})
		return err
	}
	status := r.status[u]
	status.LastRisk = string(decision.Outcome())
	status.LastRiskReason = string(decision.Spec().PrimaryReason)
	status.LastSignalID = signal.SignalID
	status.SelectedFuture = future.Instrument.ID().String()
	status.SelectedOption = option.Instrument.ID().String()
	r.status[u] = status
	if signal.Direction == qualification.DirectionExit {
		if decision.Outcome() != riskmodel.DecisionApproved {
			return nil
		}
		series, _, _ := r.qualification.Strategy(qualification.StrategyID, u)
		if series.Open == nil {
			return nil
		}
		_, err = r.qualification.Close(qualification.ExitInput{Underlying: u, QualificationID: series.Open.QualificationID, OptionID: series.Open.OptionID, SignalTime: at, OptionQuote: qualificationQuote(optionQuote)})
		return err
	}
	recent, baseline := ranges(candles)
	input := qualification.SignalInput{StrategyID: qualification.StrategyID, StrategyVersion: qualification.StrategyVersion, Underlying: u, SignalID: proposal.ID().String(), SignalTime: at, MarketSession: session, CASState: func() string {
		if casRestricted {
			return "RESTRICTED"
		}
		return "PERMITTED"
	}(), SpotMinor: signal.SpotMinor, SpotTime: at, FutureID: future.Instrument.ID().String(), FutureExpiry: future.Instrument.Expiry().String(), FutureMinor: futureQuote.LastPrice().MinorUnits(), FutureTime: futureQuote.ExchangeTime(), OptionID: option.Instrument.ID().String(), OptionExpiry: option.Instrument.Expiry().String(), StrikeMinor: option.Instrument.Strike().MinorUnits(), OptionType: string(option.Instrument.OptionType()), OptionQuote: qualificationQuote(optionQuote), EMA20Scaled: signal.EMA20Scaled, EMA50Scaled: signal.EMA50Scaled, WarmupComplete: true, Fresh: true, Direction: qualification.DirectionLong, Quantity: option.Instrument.LotSize().Int64(), RegimeInput: qualification.RegimeInput{SpotMinor: signal.SpotMinor, EMA20Scaled: signal.EMA20Scaled, EMA50Scaled: signal.EMA50Scaled, RecentRangeMinor: recent, BaselineRangeMinor: baseline}}
	input, err = qualification.WithReleasedRisk(input, decision)
	if err != nil {
		return err
	}
	record, err := r.qualification.RecordSignal(input)
	if err != nil && !errors.Is(err, qualification.ErrDuplicate) {
		return err
	}
	r.emit(notificationSpec{source: signal.SignalID, kind: "signal", at: at, underlying: u, details: notification.Details{
		Subject: string(signal.Direction), State: string(input.Risk), Underlying: string(u),
		SpotMinor: signal.SpotMinor, FutureInstrumentID: future.Instrument.ID().String(), FutureMinor: futureQuote.LastPrice().MinorUnits(),
		Expiry: option.Instrument.Expiry().String(), StrikeMinor: option.Instrument.Strike().MinorUnits(), OptionType: string(option.Instrument.OptionType()),
		BidMinor: input.OptionQuote.BidMinor, AskMinor: input.OptionQuote.AskMinor, LTPMinor: input.OptionQuote.LTPMinor,
		EMA20Scaled: signal.EMA20Scaled, EMA50Scaled: signal.EMA50Scaled, Regime: string(record.Regime.Trend) + "/" + string(record.Regime.Volatility),
	}})
	return nil
}

func (r *Runtime) observeOpen(underlying qualification.Underlying, quote marketmodel.QuoteEvent) {
	series, _, err := r.qualification.Strategy(qualification.StrategyID, underlying)
	if err != nil || series.Open == nil || series.Open.OptionID != quote.InstrumentID().String() {
		return
	}
	spot := r.latest[r.spots[underlying]]
	future, resolveErr := derivatives.ResolveFuture(r.master, quote.ExchangeTime(), r.policies[underlying])
	if resolveErr != nil {
		return
	}
	futureQuote := r.latest[future.Instrument.ID()]
	if spot.ID().IsZero() || futureQuote.ID().IsZero() {
		return
	}
	_ = r.qualification.Observe(qualification.Observation{Underlying: underlying, QualificationID: series.Open.QualificationID, ObservedAt: quote.ExchangeTime(), SpotMinor: spot.LastPrice().MinorUnits(), FutureMinor: futureQuote.LastPrice().MinorUnits(), OptionQuote: qualificationQuote(quote), Quality: qualification.QualityComplete})
}

func (r *Runtime) refreshStatus(u qualification.Underlying, at time.Time) {
	status := r.status[u]
	state := r.ema[u]
	status.WarmupSamples = state.Samples
	status.Strategy = "WARMING"
	if state.Samples >= WarmupRequired {
		status.Strategy = "READY"
	}
	spot := r.latest[r.spots[u]]
	if !spot.ID().IsZero() && at.Sub(spot.ExchangeTime()) <= r.policies[u].MaximumQuoteAge {
		firstReady := status.MarketData != "READY"
		status.MarketData = "READY"
		if firstReady {
			card := r.sessions.Current[u]
			card.MarketReadySeconds = int64(at.Sub(card.StartedAt) / time.Second)
			if card.MarketReadySeconds < 0 {
				card.MarketReadySeconds = 0
			}
			r.sessions.Current[u] = card
		}
	}
	future, err := derivatives.ResolveFuture(r.master, at, r.policies[u])
	if err == nil {
		if quote, ok := r.latest[future.Instrument.ID()]; ok && at.Sub(quote.ExchangeTime()) <= r.policies[u].MaximumQuoteAge {
			status.Future = "READY"
			status.SelectedFuture = future.Instrument.ID().String()
			selection, resolveErr := derivatives.Resolve(r.master, at, quote.LastPrice(), domain.OptionCall, r.policies[u])
			if resolveErr == nil {
				ready := true
				for _, contract := range selection.Universe {
					if q, exists := r.latest[contract.Instrument.ID()]; !exists || at.Sub(q.ExchangeTime()) > r.policies[u].MaximumQuoteAge {
						ready = false
						break
					}
				}
				if ready {
					status.OptionUniverse = "READY"
					status.SelectedOption = selection.Option.Instrument.ID().String()
				}
			}
		}
	}
	r.status[u] = status
}

func (r *Runtime) Status() []UnderlyingStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return []UnderlyingStatus{r.status[qualification.NIFTY], r.status[qualification.BANKNIFTY]}
}
func (r *Runtime) SessionScorecards() []SessionScorecard {
	r.mu.RLock()
	defer r.mu.RUnlock()
	values := append([]SessionScorecard(nil), r.sessions.Closed...)
	for _, u := range []qualification.Underlying{qualification.NIFTY, qualification.BANKNIFTY} {
		if current := r.sessions.Current[u]; current.EndedAt.IsZero() {
			values = append(values, current)
		}
	}
	return values
}
func (r *Runtime) MultiSessionScorecards() []MultiSessionScorecard {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions.Multi(r.qualification.Scorecards())
}

func qualificationQuote(value marketmodel.QuoteEvent) qualification.Quote {
	q := qualification.Quote{InstrumentID: value.InstrumentID().String(), LTPMinor: value.LastPrice().MinorUnits(), ObservedAt: value.ExchangeTime()}
	if bid := value.BestBid(); bid != nil {
		q.BidMinor = bid.Price.MinorUnits()
	}
	if ask := value.BestAsk(); ask != nil {
		q.AskMinor = ask.Price.MinorUnits()
	}
	return q
}
func mustPrice(minor int64) domain.Price { value, _ := domain.NewPrice(minor, "INR"); return value }
func instrumentByString(master instrumentmaster.Master, value string) (domain.Instrument, bool) {
	for _, instrument := range master.Instruments() {
		if instrument.ID().String() == value {
			return instrument, true
		}
	}
	return domain.Instrument{}, false
}

func (r *Runtime) quoteAtOrBefore(id domain.InstrumentID, at time.Time) (marketmodel.QuoteEvent, bool) {
	values := r.history[id]
	for index := len(values) - 1; index >= 0; index-- {
		if !values[index].ExchangeTime().After(at) {
			return values[index], true
		}
	}
	return marketmodel.QuoteEvent{}, false
}
func ranges(values []CandlePoint) (int64, int64) {
	if len(values) < 20 {
		return 1, 1
	}
	rangeFor := func(slice []CandlePoint) int64 {
		var total int64
		for _, c := range slice {
			total += c.HighMinor - c.LowMinor
		}
		if total <= 0 {
			return 1
		}
		return total / int64(len(slice))
	}
	return rangeFor(values[len(values)-10:]), rangeFor(values[len(values)-20 : len(values)-10])
}

func (r *Runtime) SubscriptionIDs() []domain.InstrumentID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.SubscriptionIDsLocked()
}

func (r *Runtime) SubscriptionTokens(at time.Time) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if at.IsZero() {
		return nil, ErrInvalid
	}
	ids := r.SubscriptionIDsLocked()
	if len(ids) == 0 || len(ids) > MaximumSubscriptions {
		return nil, ErrInvalid
	}
	tokens := make([]string, 0, len(ids))
	for _, id := range ids {
		mapping, err := r.master.ResolveInstrument(r.policies[qualification.NIFTY].Provider, id, at)
		if err != nil {
			return nil, ErrNotReady
		}
		tokens = append(tokens, mapping.Token)
	}
	sort.Strings(tokens)
	return tokens, nil
}

func (r *Runtime) SubscriptionIDsLocked() []domain.InstrumentID {
	ids := make([]domain.InstrumentID, 0, len(r.master.Instruments()))
	for _, instrument := range r.master.Instruments() {
		u := qualification.Underlying(instrument.UnderlyingID())
		if u == qualification.NIFTY || u == qualification.BANKNIFTY {
			ids = append(ids, instrument.ID())
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}
func validSession(value string) bool {
	return value == "NORMAL_TRADING" || value == "EOD_CLOSE" || strings.HasPrefix(value, "CAS_")
}
