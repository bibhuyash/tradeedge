// Package derivatives contains provider-neutral, deterministic NIFTY
// derivatives selection and market-readiness policy. Provider tokens are
// evidence returned from the instrument master; they are never canonical IDs.
package derivatives

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

const (
	FuturePolicyVersion    = "nearest-eligible-nifty-future/v1"
	ExpiryPolicyVersion    = "nearest-option-expiry-min-one-day/v1"
	StrikePolicyVersion    = "forward-atm-nearest-strike-half-up/v1"
	UniversePolicyVersion  = "bounded-atm-neighborhood/v1"
	ExecutionPolicyVersion = "option-touch-or-ltp-conservative/v1"
)

var (
	ErrInvalidPolicy         = errors.New("invalid derivatives policy")
	ErrFutureUnavailable     = errors.New("futures contract unavailable")
	ErrExpiryUnavailable     = errors.New("option expiry unavailable")
	ErrOptionUnavailable     = errors.New("option contract unavailable")
	ErrAmbiguousContract     = errors.New("ambiguous derivatives contract")
	ErrQuoteUnavailable      = errors.New("option quote unavailable")
	ErrQuoteStale            = errors.New("option quote stale")
	ErrSpreadTooWide         = errors.New("option spread too wide")
	ErrLiquidityInsufficient = errors.New("option liquidity insufficient")
)

type Policy struct {
	Underlying          domain.UnderlyingID
	Provider            domain.Provider
	MinimumExpiryDays   int
	MaximumExpiryDays   int
	StrikesEachSide     int
	MaximumSpreadBPS    int64
	MinimumBookQuantity int64
	MaximumQuoteAge     time.Duration
	AllowLTPFallback    bool
}

func DefaultPolicy() Policy {
	return Policy{Underlying: "NIFTY", Provider: "zerodha", MinimumExpiryDays: 1, MaximumExpiryDays: 14, StrikesEachSide: 2, MaximumSpreadBPS: 500, MinimumBookQuantity: 1, MaximumQuoteAge: 5 * time.Second, AllowLTPFallback: true}
}

func (p Policy) validate() error {
	if p.Underlying == "" || p.Provider == "" || p.MinimumExpiryDays < 0 || p.MaximumExpiryDays < p.MinimumExpiryDays || p.MaximumExpiryDays > 62 || p.StrikesEachSide < 0 || p.StrikesEachSide > 10 || p.MaximumSpreadBPS < 0 || p.MaximumSpreadBPS > 10_000 || p.MinimumBookQuantity <= 0 || p.MaximumQuoteAge <= 0 {
		return ErrInvalidPolicy
	}
	return nil
}

type Contract struct {
	Instrument domain.Instrument
	Mapping    domain.ProviderInstrumentRef
	Reason     string
	Policy     string
}

type Selection struct {
	Future              Contract
	Expiry              domain.CivilDate
	Universe            []Contract
	Option              Contract
	ReferencePrice      domain.Price
	StrikeIntervalMinor int64
}

func Resolve(master instrumentmaster.Master, at time.Time, reference domain.Price, optionType domain.OptionType, policy Policy) (Selection, error) {
	if master.Version() == "" || at.IsZero() || reference.IsZeroValue() || reference.MinorUnits() <= 0 || (optionType != domain.OptionCall && optionType != domain.OptionPut) || policy.validate() != nil {
		return Selection{}, ErrInvalidPolicy
	}
	future, err := resolveFuture(master, at, policy)
	if err != nil {
		return Selection{}, err
	}
	expiry, options, err := resolveExpiry(master, at, policy)
	if err != nil {
		return Selection{}, err
	}
	strikes := uniqueStrikes(options)
	if len(strikes) < 2 {
		return Selection{}, ErrOptionUnavailable
	}
	interval, ok := uniformInterval(strikes)
	if !ok {
		return Selection{}, ErrAmbiguousContract
	}
	target := roundHalfUp(reference.MinorUnits(), interval)
	center := sort.Search(len(strikes), func(i int) bool { return strikes[i] >= target })
	if center == len(strikes) || strikes[center] != target {
		return Selection{}, ErrOptionUnavailable
	}
	left, right := center-policy.StrikesEachSide, center+policy.StrikesEachSide
	if left < 0 {
		left = 0
	}
	if right >= len(strikes) {
		right = len(strikes) - 1
	}
	allowed := map[int64]bool{}
	for _, strike := range strikes[left : right+1] {
		allowed[strike] = true
	}
	var universe []Contract
	var selected Contract
	for _, instrument := range options {
		if !allowed[instrument.Strike().MinorUnits()] || instrument.OptionType() != optionType {
			continue
		}
		mapping, mapErr := master.ResolveInstrument(policy.Provider, instrument.ID(), at)
		if mapErr != nil {
			return Selection{}, ErrOptionUnavailable
		}
		contract := Contract{Instrument: instrument, Mapping: mapping, Reason: "bounded option universe around forward reference", Policy: UniversePolicyVersion}
		universe = append(universe, contract)
		if instrument.Strike().MinorUnits() == target {
			if !selected.Instrument.IsZero() {
				return Selection{}, ErrAmbiguousContract
			}
			selected = contract
		}
	}
	if selected.Instrument.IsZero() {
		return Selection{}, ErrOptionUnavailable
	}
	sort.Slice(universe, func(i, j int) bool {
		return universe[i].Instrument.Strike().MinorUnits() < universe[j].Instrument.Strike().MinorUnits()
	})
	selected.Policy, selected.Reason = StrikePolicyVersion, "ATM call selected from NIFTY future using nearest strike with half-up ties"
	return Selection{Future: future, Expiry: expiry, Universe: universe, Option: selected, ReferencePrice: reference, StrikeIntervalMinor: interval}, nil
}

func resolveFuture(master instrumentmaster.Master, at time.Time, policy Policy) (Contract, error) {
	eligible := matching(master, domain.InstrumentFuture, domain.OptionNone, policy.Underlying)
	cutoff := civil(at.AddDate(0, 0, policy.MinimumExpiryDays))
	var chosen domain.Instrument
	for _, item := range eligible {
		if item.Expiry().String() < cutoff {
			continue
		}
		if chosen.IsZero() || item.Expiry().String() < chosen.Expiry().String() {
			chosen = item
		} else if item.Expiry() == chosen.Expiry() {
			return Contract{}, ErrAmbiguousContract
		}
	}
	if chosen.IsZero() {
		return Contract{}, ErrFutureUnavailable
	}
	mapping, err := master.ResolveInstrument(policy.Provider, chosen.ID(), at)
	if err != nil {
		return Contract{}, ErrFutureUnavailable
	}
	return Contract{Instrument: chosen, Mapping: mapping, Policy: FuturePolicyVersion, Reason: "nearest uniquely mapped non-expired NIFTY future"}, nil
}

func resolveExpiry(master instrumentmaster.Master, at time.Time, policy Policy) (domain.CivilDate, []domain.Instrument, error) {
	minDate, maxDate := civil(at.AddDate(0, 0, policy.MinimumExpiryDays)), civil(at.AddDate(0, 0, policy.MaximumExpiryDays))
	all := matching(master, domain.InstrumentOption, "", policy.Underlying)
	expiries := map[string]bool{}
	for _, item := range all {
		if item.Expiry().String() >= minDate && item.Expiry().String() <= maxDate {
			expiries[item.Expiry().String()] = true
		}
	}
	if len(expiries) == 0 {
		return domain.CivilDate{}, nil, ErrExpiryUnavailable
	}
	keys := make([]string, 0, len(expiries))
	for key := range expiries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var selected []domain.Instrument
	for _, item := range all {
		if item.Expiry().String() == keys[0] {
			selected = append(selected, item)
		}
	}
	if len(selected) == 0 {
		return domain.CivilDate{}, nil, ErrExpiryUnavailable
	}
	return selected[0].Expiry(), selected, nil
}

func matching(master instrumentmaster.Master, kind domain.InstrumentType, option domain.OptionType, underlying domain.UnderlyingID) []domain.Instrument {
	var result []domain.Instrument
	for _, item := range master.Instruments() {
		if item.Type() == kind && item.UnderlyingID() == underlying && (option == "" || item.OptionType() == option) {
			result = append(result, item)
		}
	}
	return result
}

func uniqueStrikes(options []domain.Instrument) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, item := range options {
		v := item.Strike().MinorUnits()
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
func uniformInterval(values []int64) (int64, bool) {
	if len(values) < 2 {
		return 0, false
	}
	d := values[1] - values[0]
	if d <= 0 {
		return 0, false
	}
	for i := 2; i < len(values); i++ {
		if values[i]-values[i-1] != d {
			return 0, false
		}
	}
	return d, true
}
func roundHalfUp(value, interval int64) int64 {
	lower := value / interval * interval
	if value-lower >= interval-(value-lower) {
		return lower + interval
	}
	return lower
}
func civil(at time.Time) string { y, m, d := at.Date(); return fmt.Sprintf("%04d-%02d-%02d", y, m, d) }

type QuoteDecision struct {
	Ready  bool
	Price  domain.Price
	Source string
	Reason error
}

// EvaluateExecutionQuote uses only the selected option's accepted quote.
// BUY uses ask and SELL uses bid. LTP is an explicit bounded approximation
// only when book data is absent and the policy allows it.
func EvaluateExecutionQuote(option domain.Instrument, quote *marketmodel.QuoteEvent, side domain.Side, now time.Time, policy Policy) QuoteDecision {
	if option.Type() != domain.InstrumentOption || quote == nil || quote.InstrumentID() != option.ID() {
		return QuoteDecision{Reason: ErrQuoteUnavailable}
	}
	if now.Before(quote.ExchangeTime()) || now.Sub(quote.ExchangeTime()) > policy.MaximumQuoteAge {
		return QuoteDecision{Reason: ErrQuoteStale}
	}
	bid, ask := quote.BestBid(), quote.BestAsk()
	if bid != nil && ask != nil {
		if bid.Quantity < policy.MinimumBookQuantity || ask.Quantity < policy.MinimumBookQuantity {
			return QuoteDecision{Reason: ErrLiquidityInsufficient}
		}
		if ask.Price.MinorUnits() < bid.Price.MinorUnits() {
			return QuoteDecision{Reason: ErrQuoteUnavailable}
		}
		spread := ask.Price.MinorUnits() - bid.Price.MinorUnits()
		mid := ask.Price.MinorUnits() + bid.Price.MinorUnits()
		if mid <= 0 || spread*20_000 > mid*policy.MaximumSpreadBPS {
			return QuoteDecision{Reason: ErrSpreadTooWide}
		}
		if side == domain.SideBuy {
			if ask.Price.MinorUnits()%option.TickSize().MinorUnits() != 0 {
				return QuoteDecision{Reason: ErrQuoteUnavailable}
			}
			return QuoteDecision{Ready: true, Price: ask.Price, Source: "BEST_ASK"}
		}
		if side == domain.SideSell {
			if bid.Price.MinorUnits()%option.TickSize().MinorUnits() != 0 {
				return QuoteDecision{Reason: ErrQuoteUnavailable}
			}
			return QuoteDecision{Ready: true, Price: bid.Price, Source: "BEST_BID"}
		}
	}
	if policy.AllowLTPFallback && quote.LastPrice().MinorUnits() > 0 && quote.LastPrice().MinorUnits()%option.TickSize().MinorUnits() == 0 {
		return QuoteDecision{Ready: true, Price: quote.LastPrice(), Source: "LTP_APPROXIMATION"}
	}
	return QuoteDecision{Reason: ErrQuoteUnavailable}
}
