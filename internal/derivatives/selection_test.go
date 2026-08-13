package derivatives

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/notification"
)

var testAt = time.Date(2026, 8, 11, 4, 30, 0, 0, time.UTC)

func TestResolveUsesForwardAndBoundedMappedOptionUniverse(t *testing.T) {
	master, future, option := fixtureMaster(t)
	selection, err := Resolve(master, testAt, price(t, 24_812_00), domain.OptionCall, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if selection.Future.Instrument.ID() != future.ID() || selection.Option.Instrument.ID() != option.ID() {
		t.Fatalf("wrong contracts: %#v", selection)
	}
	if selection.StrikeIntervalMinor != 5_000 || len(selection.Universe) != 5 || selection.Option.Mapping.Token != "11555842" {
		t.Fatalf("bad universe: %#v", selection)
	}
}

func TestResolveBANKNIFTYIndependentlyAndRejectsOtherUnderlyings(t *testing.T) {
	master, future, option := bankFixtureMaster(t)
	policy, err := PolicyFor("BANKNIFTY")
	if err != nil {
		t.Fatal(err)
	}
	selection, err := Resolve(master, testAt, price(t, 52_120_00), domain.OptionCall, policy)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Future.Instrument.ID() != future.ID() || selection.Option.Instrument.ID() != option.ID() || selection.StrikeIntervalMinor != 10_000 || len(selection.Universe) != 5 {
		t.Fatalf("bad BANKNIFTY selection: %#v", selection)
	}
	if selection.Future.Policy == FuturePolicyVersion || selection.Option.Policy == StrikePolicyVersion {
		t.Fatalf("NIFTY policy leaked: %#v", selection)
	}
	for _, unsupported := range []domain.UnderlyingID{"FINNIFTY", "NIFTYBEES", "RELIANCE"} {
		if _, err := PolicyFor(unsupported); !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("%s accepted: %v", unsupported, err)
		}
	}
}

func TestResolveFailsClosedForRolloverAmbiguityAndMissingMapping(t *testing.T) {
	master, _, _ := fixtureMaster(t)
	p := DefaultPolicy()
	p.MinimumExpiryDays = 10
	if _, err := Resolve(master, testAt, price(t, 24_800_00), domain.OptionCall, p); !errors.Is(err, ErrExpiryUnavailable) {
		t.Fatalf("got %v", err)
	}
	broken, _, _ := fixtureMasterWithoutSelectedMapping(t)
	if _, err := Resolve(broken, testAt, price(t, 24_800_00), domain.OptionCall, DefaultPolicy()); !errors.Is(err, ErrOptionUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestOptionQuoteIsSoleExecutionAuthority(t *testing.T) {
	_, _, option := fixtureMaster(t)
	optionQuote := quote(t, option, testAt.Add(-time.Second), 10_000, 9_900, 10_100, 65)
	buy := EvaluateExecutionQuote(option, &optionQuote, domain.SideBuy, testAt, DefaultPolicy())
	sell := EvaluateExecutionQuote(option, &optionQuote, domain.SideSell, testAt, DefaultPolicy())
	if !buy.Ready || buy.Price.MinorUnits() != 10_100 || buy.Source != "BEST_ASK" || sell.Price.MinorUnits() != 9_900 {
		t.Fatalf("bad touch policy: %#v %#v", buy, sell)
	}
	stale := EvaluateExecutionQuote(option, &optionQuote, domain.SideBuy, testAt.Add(10*time.Second), DefaultPolicy())
	if !errors.Is(stale.Reason, ErrQuoteStale) {
		t.Fatalf("got %v", stale.Reason)
	}
	wide := quote(t, option, testAt, 10_000, 5_000, 15_000, 65)
	if got := EvaluateExecutionQuote(option, &wide, domain.SideBuy, testAt, DefaultPolicy()); !errors.Is(got.Reason, ErrSpreadTooWide) {
		t.Fatalf("got %v", got.Reason)
	}
	offTick := quote(t, option, testAt, 10_001, 9_900, 10_101, 65)
	if got := EvaluateExecutionQuote(option, &offTick, domain.SideBuy, testAt, DefaultPolicy()); !errors.Is(got.Reason, ErrQuoteUnavailable) {
		t.Fatalf("off-tick price accepted: %v", got.Reason)
	}
}

func TestConnectedShadowAndPaperLifecycleReplayRestart(t *testing.T) {
	master, future, option := fixtureMaster(t)
	policy := DefaultPolicy()
	observer := &eventSink{}
	futureQuote := quote(t, future, testAt.Add(-time.Second), 24_812_00, 24_811_00, 24_813_00, 65)
	entryOption := quote(t, option, testAt.Add(-time.Second), 10_000, 9_900, 10_100, 65)
	entrySignal := Signal{ID: "bullish-1", At: testAt, Spot: price(t, 24_790_00), FastEMAScaled: 24_800_00_000_000, SlowEMAScaled: 24_780_00_000_000, Direction: "LONG"}
	controls := Controls{Session: "NORMAL_TRADING"}
	shadow, _ := NewMachine(ModeShadow, master, policy, allowRisk{}, observer)
	shadowState, err := shadow.Enter(entrySignal, Market{Future: futureQuote, Option: entryOption}, controls)
	if err != nil {
		t.Fatal(err)
	}
	if len(shadowState.Fills) != 0 || shadowState.Position.Open {
		t.Fatal("SHADOW mutated execution state")
	}
	paper, _ := NewMachine(ModePaper, master, policy, allowRisk{}, observer)
	entered, err := paper.Enter(entrySignal, Market{Future: futureQuote, Option: entryOption}, controls)
	if err != nil {
		t.Fatal(err)
	}
	if len(entered.Fills) != 1 || entered.Fills[0].PriceMinor != 10_100 || entered.Position.Quantity != 65 || entered.Position.CostBasisMinor != 656_500 {
		t.Fatalf("bad entry: %#v", entered)
	}
	if _, err = paper.Enter(entrySignal, Market{Future: futureQuote, Option: entryOption}, controls); !errors.Is(err, ErrDuplicate) && !errors.Is(err, ErrPositionOpen) {
		t.Fatalf("duplicate got %v", err)
	}
	mark := quote(t, option, testAt.Add(time.Second), 10_500, 10_400, 10_600, 65)
	marked, err := paper.Mark(mark, testAt.Add(2*time.Second))
	if err != nil || marked.Position.UnrealizedPnLMinor != 26_000 {
		t.Fatalf("mark: %#v %v", marked, err)
	}
	restarted, _ := NewMachine(ModePaper, master, policy, allowRisk{}, observer)
	if err = restarted.Restore(marked); err != nil {
		t.Fatal(err)
	}
	exitAt := testAt.Add(3 * time.Second)
	exitFuture := quote(t, future, exitAt.Add(-time.Second), 24_700_00, 24_699_00, 24_701_00, 65)
	exitOption := quote(t, option, exitAt.Add(-time.Second), 10_500, 10_400, 10_600, 65)
	exited, err := restarted.Exit(Signal{ID: "bearish-1", At: exitAt, Spot: price(t, 24_680_00), FastEMAScaled: 24_670_00_000_000, SlowEMAScaled: 24_690_00_000_000, Direction: "EXIT"}, Market{Future: exitFuture, Option: exitOption}, controls)
	if err != nil {
		t.Fatal(err)
	}
	if exited.Position.Open || exited.Position.RealizedPnLMinor != 19_500 || len(exited.Fills) != 2 {
		t.Fatalf("bad exit: %#v", exited)
	}
	replay, _ := NewMachine(ModePaper, master, policy, allowRisk{}, nil)
	_, _ = replay.Enter(entrySignal, Market{Future: futureQuote, Option: entryOption}, controls)
	_, _ = replay.Mark(mark, testAt.Add(2*time.Second))
	replayExit, _ := replay.Exit(Signal{ID: "bearish-1", At: exitAt, Spot: price(t, 24_680_00), FastEMAScaled: 24_670_00_000_000, SlowEMAScaled: 24_690_00_000_000, Direction: "EXIT"}, Market{Future: exitFuture, Option: exitOption}, controls)
	if replayExit.Checkpoint != exited.Checkpoint {
		t.Fatalf("replay mismatch %s %s", replayExit.Checkpoint, exited.Checkpoint)
	}
	t.Logf("entry_fill=%s exit_fill=%s checkpoint=%s option=%s", entered.Fills[0].ID, exited.Fills[1].ID, exited.Checkpoint, exited.Selection.OptionID)
	if len(observer.events) < 4 {
		t.Fatalf("missing outbound evidence: %d", len(observer.events))
	}
}

func TestConnectedAdversarialContainment(t *testing.T) {
	master, future, option := fixtureMaster(t)
	futureQuote := quote(t, future, testAt, 24_800_00, 24_799_00, 24_801_00, 65)
	optionQuote := quote(t, option, testAt, 10_000, 9_900, 10_100, 65)
	signal := Signal{ID: "s", At: testAt, Spot: price(t, 24_790_00), Direction: "LONG"}
	tests := []struct {
		name     string
		market   Market
		controls Controls
		risk     RiskGate
		want     error
	}{
		{"future missing", Market{Option: optionQuote}, Controls{Session: "NORMAL_TRADING"}, allowRisk{}, ErrFutureUnavailable},
		{"option missing", Market{Future: futureQuote}, Controls{Session: "NORMAL_TRADING"}, allowRisk{}, ErrQuoteUnavailable},
		{"CAS", Market{Future: futureQuote, Option: optionQuote}, Controls{Session: "NORMAL_TRADING", CASRestricted: true}, allowRisk{}, ErrCASRestricted},
		{"session", Market{Future: futureQuote, Option: optionQuote}, Controls{Session: "MARKET_CLOSED"}, allowRisk{}, ErrSessionNotAllowed},
		{"stop", Market{Future: futureQuote, Option: optionQuote}, Controls{Session: "NORMAL_TRADING", StopNewExposure: true}, allowRisk{}, ErrStopNewExposure},
		{"risk", Market{Future: futureQuote, Option: optionQuote}, Controls{Session: "NORMAL_TRADING"}, rejectRisk{}, ErrRiskBlocked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := NewMachine(ModePaper, master, DefaultPolicy(), tc.risk, nil)
			before := m.Snapshot()
			after, err := m.Enter(signal, tc.market, tc.controls)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v", err)
			}
			if len(after.Fills) != len(before.Fills) || after.Position.Open {
				t.Fatal("blocked scenario created exposure")
			}
		})
	}
}

func TestExitRemainsAvailableDuringStopAndEOD(t *testing.T) {
	master, future, option := fixtureMaster(t)
	machine, _ := NewMachine(ModePaper, master, DefaultPolicy(), allowRisk{}, panicObserver{})
	futureQuote := quote(t, future, testAt, 24_800_00, 24_799_00, 24_801_00, 65)
	optionQuote := quote(t, option, testAt, 10_000, 9_900, 10_100, 65)
	_, err := machine.Enter(Signal{ID: "entry", At: testAt, Spot: price(t, 24_790_00), Direction: "LONG"}, Market{Future: futureQuote, Option: optionQuote}, Controls{Session: "NORMAL_TRADING"})
	if err != nil {
		t.Fatal(err)
	}
	exitAt := testAt.Add(time.Second)
	exitFuture := quote(t, future, exitAt, 24_790_00, 24_789_00, 24_791_00, 65)
	exitOption := quote(t, option, exitAt, 10_000, 9_900, 10_100, 65)
	state, err := machine.Exit(Signal{ID: "eod", At: exitAt, Spot: price(t, 24_780_00), Direction: "EOD_CLOSE"}, Market{Future: exitFuture, Option: exitOption}, Controls{Session: "EOD_CLOSE", StopNewExposure: true})
	if err != nil || state.Position.Open {
		t.Fatalf("safe exit blocked: %v %#v", err, state.Position)
	}
}

func TestCheckpointCorruptionAndLateQuotesFailClosed(t *testing.T) {
	master, future, option := fixtureMaster(t)
	machine, _ := NewMachine(ModePaper, master, DefaultPolicy(), allowRisk{}, nil)
	state := machine.Snapshot()
	state.Checkpoint = "corrupt"
	if err := machine.Restore(state); !errors.Is(err, ErrNotReady) {
		t.Fatalf("corrupt checkpoint accepted: %v", err)
	}
	futureQuote := quote(t, future, testAt.Add(time.Second), 24_800_00, 24_799_00, 24_801_00, 65)
	optionQuote := quote(t, option, testAt.Add(time.Second), 10_000, 9_900, 10_100, 65)
	_, err := machine.Enter(Signal{ID: "late", At: testAt, Spot: price(t, 24_790_00), Direction: "LONG"}, Market{Future: futureQuote, Option: optionQuote}, Controls{Session: "NORMAL_TRADING"})
	if !errors.Is(err, ErrFutureUnavailable) {
		t.Fatalf("late future accepted: %v", err)
	}
}

type allowRisk struct{}

func (allowRisk) Decide(in RiskInput) RiskResult {
	return RiskResult{Outcome: "APPROVED", Reason: "PHASE3_ALL_RULES_PASSED", DecisionID: hash("approved", in.ProposalID)}
}

type rejectRisk struct{}

func (rejectRisk) Decide(in RiskInput) RiskResult {
	return RiskResult{Outcome: "REJECTED", Reason: "STOP_NEW_EXPOSURE", DecisionID: hash("rejected", in.ProposalID)}
}

type eventSink struct{ events []notification.Event }

func (s *eventSink) Observe(e notification.Event) { s.events = append(s.events, e) }

type panicObserver struct{}

func (panicObserver) Observe(notification.Event) { panic("telegram unavailable") }

func fixtureMaster(t *testing.T) (instrumentmaster.Master, domain.Instrument, domain.Instrument) {
	return buildMaster(t, true)
}
func fixtureMasterWithoutSelectedMapping(t *testing.T) (instrumentmaster.Master, domain.Instrument, domain.Instrument) {
	return buildMaster(t, false)
}

func bankFixtureMaster(t *testing.T) (instrumentmaster.Master, domain.Instrument, domain.Instrument) {
	t.Helper()
	underlying, _ := domain.NewUnderlyingID("BANKNIFTY")
	expiry, _ := domain.NewCivilDate(2026, 8, 18)
	futureExpiry, _ := domain.NewCivilDate(2026, 8, 25)
	lot, _ := domain.NewQuantity(15)
	future, err := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentFutures, UnderlyingID: underlying, Type: domain.InstrumentFuture, ExchangeSymbol: "BANKNIFTY26AUGFUT", Derivative: &domain.DerivativeSpec{Expiry: futureExpiry, OptionType: domain.OptionNone}, LotSize: lot, TickSize: price(t, 10), Currency: "INR"})
	if err != nil {
		t.Fatal(err)
	}
	instruments := []domain.Instrument{future}
	mappings := []domain.ProviderInstrumentRef{mapping(future, "bank-future-token")}
	var selected domain.Instrument
	for strike := int64(51_900_00); strike <= 52_300_00; strike += 10_000 {
		for _, kind := range []domain.OptionType{domain.OptionCall, domain.OptionPut} {
			suffix := map[domain.OptionType]string{domain.OptionCall: "CE", domain.OptionPut: "PE"}[kind]
			symbol := "BANKNIFTY26818" + fmtStrike(strike) + suffix
			instrument, makeErr := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentOptions, UnderlyingID: underlying, Type: domain.InstrumentOption, ExchangeSymbol: symbol, Derivative: &domain.DerivativeSpec{Expiry: expiry, Strike: price(t, strike), OptionType: kind}, LotSize: lot, TickSize: price(t, 5), Currency: "INR"})
			if makeErr != nil {
				t.Fatal(makeErr)
			}
			instruments = append(instruments, instrument)
			mappings = append(mappings, mapping(instrument, hash("bank-token", symbol)[:8]))
			if strike == 52_100_00 && kind == domain.OptionCall {
				selected = instrument
			}
		}
	}
	master, err := instrumentmaster.New(testAt.Add(-time.Hour), instruments, mappings)
	if err != nil {
		t.Fatal(err)
	}
	return master, future, selected
}
func buildMaster(t *testing.T, includeSelected bool) (instrumentmaster.Master, domain.Instrument, domain.Instrument) {
	t.Helper()
	underlying, _ := domain.NewUnderlyingID("NIFTY")
	expiry, _ := domain.NewCivilDate(2026, 8, 18)
	futureExpiry, _ := domain.NewCivilDate(2026, 8, 25)
	lot, _ := domain.NewQuantity(65)
	futTick := price(t, 10)
	optTick := price(t, 5)
	future, err := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentFutures, UnderlyingID: underlying, Type: domain.InstrumentFuture, ExchangeSymbol: "NIFTY26AUGFUT", Derivative: &domain.DerivativeSpec{Expiry: futureExpiry, OptionType: domain.OptionNone}, LotSize: lot, TickSize: futTick, Currency: "INR"})
	if err != nil {
		t.Fatal(err)
	}
	instruments := []domain.Instrument{future}
	mappings := []domain.ProviderInstrumentRef{mapping(future, "14866434")}
	var selected domain.Instrument
	for strike := int64(24_700_00); strike <= 24_900_00; strike += 5_000 {
		for _, kind := range []domain.OptionType{domain.OptionCall, domain.OptionPut} {
			symbol := "NIFTY26818" + fmtStrike(strike) + map[domain.OptionType]string{domain.OptionCall: "CE", domain.OptionPut: "PE"}[kind]
			instrument, makeErr := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentOptions, UnderlyingID: underlying, Type: domain.InstrumentOption, ExchangeSymbol: symbol, Derivative: &domain.DerivativeSpec{Expiry: expiry, Strike: price(t, strike), OptionType: kind}, LotSize: lot, TickSize: optTick, Currency: "INR"})
			if makeErr != nil {
				t.Fatal(makeErr)
			}
			instruments = append(instruments, instrument)
			if strike == 24_800_00 && kind == domain.OptionCall {
				selected = instrument
			}
			if includeSelected || instrument.ID() != selected.ID() {
				token := hash("token", symbol)[:8]
				if strike == 24_800_00 && kind == domain.OptionCall {
					token = "11555842"
				}
				mappings = append(mappings, mapping(instrument, token))
			}
		}
	}
	master, err := instrumentmaster.New(testAt.Add(-time.Hour), instruments, mappings)
	if err != nil {
		t.Fatal(err)
	}
	return master, future, selected
}
func mapping(i domain.Instrument, token string) domain.ProviderInstrumentRef {
	return domain.ProviderInstrumentRef{Provider: "zerodha", Token: token, TradingSymbol: i.Symbol(), InstrumentID: i.ID(), ValidFrom: testAt.Add(-24 * time.Hour), ValidUntil: testAt.Add(24 * time.Hour)}
}
func price(t *testing.T, v int64) domain.Price {
	t.Helper()
	p, err := domain.NewPrice(v, "INR")
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func quote(t *testing.T, i domain.Instrument, at time.Time, ltp, bid, ask, qty int64) marketmodel.QuoteEvent {
	t.Helper()
	b, a := marketmodel.BookLevel{Price: price(t, bid), Quantity: qty}, marketmodel.BookLevel{Price: price(t, ask), Quantity: qty}
	q, err := marketmodel.NewQuoteEvent(marketmodel.QuoteSpec{InstrumentID: i.ID(), LastPrice: price(t, ltp), BestBid: &b, BestAsk: &a, Volume: 1000, ExchangeTime: at, IngestedAt: at.Add(time.Millisecond), Provenance: marketmodel.Provenance{Provider: "zerodha", ProviderToken: "fixture", MasterVersion: "fixture/v1"}})
	if err != nil {
		t.Fatal(err)
	}
	return q
}
func fmtStrike(v int64) string { return fmt.Sprintf("%d", v/100) }
