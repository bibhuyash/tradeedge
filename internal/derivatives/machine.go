package derivatives

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/notification"
)

const MachineSchemaVersion = "phase8-m2-connected-option-machine/v1"

var (
	ErrNotReady          = errors.New("derivatives machine not ready")
	ErrDuplicate         = errors.New("duplicate directional signal")
	ErrPositionOpen      = errors.New("option position already open")
	ErrPositionMissing   = errors.New("option position is not open")
	ErrRiskBlocked       = errors.New("central risk blocked proposal")
	ErrCASRestricted     = errors.New("CAS restricted")
	ErrSessionNotAllowed = errors.New("session not allowed")
	ErrStopNewExposure   = errors.New("new exposure stopped")
)

type Mode string

const (
	ModeShadow Mode = "SHADOW"
	ModePaper  Mode = "PAPER"
)

type RiskInput struct {
	ProposalID   string
	InstrumentID domain.InstrumentID
	Underlying   domain.UnderlyingID
	Quantity     int64
	Premium      domain.Price
	Increasing   bool
}
type RiskResult struct {
	Outcome string `json:"outcome"`
	Reason string `json:"reason"`
	DecisionID string `json:"decision_id"`
}
type RiskGate interface{ Decide(RiskInput) RiskResult }
type Observer interface{ Observe(notification.Event) }

type Signal struct {
	ID            string       `json:"id"`
	At            time.Time    `json:"at"`
	Spot          domain.Price `json:"spot"`
	FastEMAScaled int64        `json:"fast_ema_scaled"`
	SlowEMAScaled int64        `json:"slow_ema_scaled"`
	Direction     string       `json:"direction"`
}
func (s Signal) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID            string    `json:"id"`
		At            time.Time `json:"at"`
		SpotMinor     int64     `json:"spot_minor"`
		Currency      string    `json:"currency"`
		FastEMAScaled int64     `json:"fast_ema_scaled"`
		SlowEMAScaled int64     `json:"slow_ema_scaled"`
		Direction     string    `json:"direction"`
	}{s.ID, s.At, s.Spot.MinorUnits(), s.Spot.Currency().String(), s.FastEMAScaled, s.SlowEMAScaled, s.Direction})
}

type Controls struct {
	Session                        string
	CASRestricted, StopNewExposure bool
}
type Market struct {
	Future marketmodel.QuoteEvent
	Option marketmodel.QuoteEvent
}

type Fill struct {
	ID           string              `json:"id"`
	ProposalID   string              `json:"proposal_id"`
	Side         string              `json:"side"`
	PriceSource  string              `json:"price_source"`
	InstrumentID domain.InstrumentID `json:"instrument_id"`
	Quantity     int64               `json:"quantity"`
	PriceMinor   int64               `json:"price_minor"`
	At           time.Time           `json:"at"`
}
type Position struct {
	InstrumentID       domain.InstrumentID `json:"instrument_id"`
	Quantity           int64               `json:"quantity"`
	EntryPriceMinor    int64               `json:"entry_price_minor"`
	CurrentMarkMinor   int64               `json:"current_mark_minor"`
	CostBasisMinor     int64               `json:"cost_basis_minor"`
	UnrealizedPnLMinor int64               `json:"unrealized_pnl_minor"`
	RealizedPnLMinor   int64               `json:"realized_pnl_minor"`
	Open               bool                `json:"open"`
}
type Snapshot struct {
	SchemaVersion string        `json:"schema_version"`
	Mode          Mode          `json:"mode"`
	MasterVersion string        `json:"master_version"`
	Selection     SelectionView `json:"selection"`
	LatestSignal  Signal        `json:"latest_signal"`
	Risk          RiskResult    `json:"risk"`
	Fills         []Fill        `json:"fills"`
	Position      Position      `json:"position"`
	Checkpoint    string        `json:"checkpoint"`
}
type SelectionView struct {
	FutureID      string `json:"future_id"`
	FutureSymbol  string `json:"future_symbol"`
	OptionID      string `json:"option_id"`
	OptionSymbol  string `json:"option_symbol"`
	Expiry        string `json:"expiry"`
	OptionType    string `json:"option_type"`
	Policy        string `json:"policy"`
	StrikeMinor   int64  `json:"strike_minor"`
	ReferenceMinor int64 `json:"reference_minor"`
	LotSize       int64  `json:"lot_size"`
	TickSizeMinor int64  `json:"tick_size_minor"`
}

type Machine struct {
	mu       sync.Mutex
	mode     Mode
	master   instrumentmaster.Master
	policy   Policy
	risk     RiskGate
	observer Observer
	seen     map[string]string
	snapshot Snapshot
}

func NewMachine(mode Mode, master instrumentmaster.Master, policy Policy, risk RiskGate, observer Observer) (*Machine, error) {
	if (mode != ModePaper && mode != ModeShadow) || master.Version() == "" || policy.validate() != nil || risk == nil {
		return nil, ErrInvalidPolicy
	}
	m := &Machine{mode: mode, master: master, policy: policy, risk: risk, observer: observer, seen: map[string]string{}}
	m.snapshot = Snapshot{SchemaVersion: MachineSchemaVersion, Mode: mode, MasterVersion: string(master.Version())}
	m.checkpoint()
	return m, nil
}

func (m *Machine) Enter(signal Signal, market Market, controls Controls) (Snapshot, error) {
	return m.apply(signal, market, controls, false)
}
func (m *Machine) Exit(signal Signal, market Market, controls Controls) (Snapshot, error) {
	return m.apply(signal, market, controls, true)
}

func (m *Machine) apply(signal Signal, market Market, controls Controls, exit bool) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if signal.ID == "" || signal.At.IsZero() || signal.Spot.MinorUnits() <= 0 {
		return m.snapshot, ErrNotReady
	}
	if controls.CASRestricted {
		return m.snapshot, ErrCASRestricted
	}
	if controls.Session != "NORMAL_TRADING" && !(exit && controls.Session == "EOD_CLOSE") {
		return m.snapshot, ErrSessionNotAllowed
	}
	if !exit && controls.StopNewExposure {
		return m.snapshot, ErrStopNewExposure
	}
	if !exit && m.snapshot.Position.Open {
		return m.snapshot, ErrPositionOpen
	}
	if exit && !m.snapshot.Position.Open {
		return m.snapshot, ErrPositionMissing
	}
	if prior, ok := m.seen[signal.ID]; ok {
		if prior == digestSignal(signal) {
			return m.snapshot, ErrDuplicate
		}
		return m.snapshot, ErrNotReady
	}
	if market.Future.ID().IsZero() || market.Future.ExchangeTime().After(signal.At) || signal.At.Sub(market.Future.ExchangeTime()) > m.policy.MaximumQuoteAge {
		return m.snapshot, ErrFutureUnavailable
	}
	selection, err := Resolve(m.master, signal.At, market.Future.LastPrice(), domain.OptionCall, m.policy)
	if err != nil {
		return m.snapshot, err
	}
	if market.Future.InstrumentID() != selection.Future.Instrument.ID() {
		return m.snapshot, ErrFutureUnavailable
	}
	if exit && selection.Option.Instrument.ID() != m.snapshot.Position.InstrumentID {
		// Never roll or migrate an open position implicitly. Resolve its identity
		// directly and use only that contract's quote for the exit.
		open, ok := m.master.Instrument(m.snapshot.Position.InstrumentID)
		if !ok {
			return m.snapshot, ErrOptionUnavailable
		}
		mapping, mapErr := m.master.ResolveInstrument(m.policy.Provider, open.ID(), signal.At)
		if mapErr != nil {
			return m.snapshot, ErrOptionUnavailable
		}
		selection.Option = Contract{Instrument: open, Mapping: mapping, Policy: StrikePolicyVersion, Reason: "existing open option identity retained"}
	}
	side := domain.SideBuy
	if exit {
		side = domain.SideSell
	}
	quoteDecision := EvaluateExecutionQuote(selection.Option.Instrument, &market.Option, side, signal.At, m.policy)
	if !quoteDecision.Ready {
		return m.snapshot, quoteDecision.Reason
	}
	quantity := selection.Option.Instrument.LotSize().Int64()
	if exit {
		quantity = m.snapshot.Position.Quantity
	}
	proposalID := hash("proposal", signal.ID, selection.Option.Instrument.ID().String(), fmt.Sprint(quantity), string(side), fmt.Sprint(quoteDecision.Price.MinorUnits()))
	risk := m.risk.Decide(RiskInput{ProposalID: proposalID, InstrumentID: selection.Option.Instrument.ID(), Underlying: selection.Option.Instrument.UnderlyingID(), Quantity: quantity, Premium: quoteDecision.Price, Increasing: !exit})
	if risk.DecisionID == "" {
		risk.DecisionID = hash("risk", proposalID, risk.Outcome, risk.Reason)
	}
	m.snapshot.LatestSignal = signal
	m.snapshot.Risk = risk
	m.snapshot.Selection = view(selection)
	m.seen[signal.ID] = digestSignal(signal)
	m.emit(signal, selection, notification.KindProposalGenerated, notification.SeverityInfo, proposalID, quantity, quoteDecision.Price.MinorUnits(), risk.Outcome)
	if risk.Outcome != "APPROVED" {
		m.emit(signal, selection, notification.KindRiskRejected, notification.SeverityWarning, risk.DecisionID, quantity, quoteDecision.Price.MinorUnits(), risk.Reason)
		m.checkpoint()
		return m.snapshot, ErrRiskBlocked
	}
	if m.mode == ModeShadow {
		m.emit(signal, selection, notification.KindShadowTrade, notification.SeverityInfo, proposalID, quantity, quoteDecision.Price.MinorUnits(), "NO_BROKER_MUTATION")
		m.checkpoint()
		return m.snapshot, nil
	}
	fill := Fill{ID: hash("fill", proposalID), ProposalID: proposalID, Side: string(side), InstrumentID: selection.Option.Instrument.ID(), Quantity: quantity, PriceMinor: quoteDecision.Price.MinorUnits(), PriceSource: quoteDecision.Source, At: signal.At}
	if !exit {
		basis, ok := multiply(quantity, fill.PriceMinor)
		if !ok {
			return m.snapshot, ErrNotReady
		}
		m.snapshot.Position = Position{InstrumentID: fill.InstrumentID, Quantity: quantity, EntryPriceMinor: fill.PriceMinor, CurrentMarkMinor: fill.PriceMinor, CostBasisMinor: basis, Open: true}
	} else {
		proceeds, ok := multiply(quantity, fill.PriceMinor)
		if !ok {
			return m.snapshot, ErrNotReady
		}
		m.snapshot.Position.CurrentMarkMinor = fill.PriceMinor
		m.snapshot.Position.UnrealizedPnLMinor = 0
		m.snapshot.Position.RealizedPnLMinor = proceeds - m.snapshot.Position.CostBasisMinor
		m.snapshot.Position.Open = false
	}
	m.snapshot.Fills = append(m.snapshot.Fills, fill)
	m.emit(signal, selection, notification.KindPaperFill, notification.SeverityInfo, fill.ID, quantity, fill.PriceMinor, quoteDecision.Source)
	m.checkpoint()
	return m.snapshot, nil
}

func (m *Machine) Mark(quote marketmodel.QuoteEvent, now time.Time) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := &m.snapshot.Position
	if !p.Open || quote.InstrumentID() != p.InstrumentID || now.Before(quote.ExchangeTime()) || now.Sub(quote.ExchangeTime()) > m.policy.MaximumQuoteAge {
		return m.snapshot, ErrQuoteStale
	}
	value, ok := multiply(p.Quantity, quote.LastPrice().MinorUnits())
	if !ok {
		return m.snapshot, ErrNotReady
	}
	p.CurrentMarkMinor = quote.LastPrice().MinorUnits()
	p.UnrealizedPnLMinor = value - p.CostBasisMinor
	m.checkpoint()
	return m.snapshot, nil
}
func (m *Machine) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneSnapshot(m.snapshot)
}
func (m *Machine) Restore(snapshot Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if snapshot.SchemaVersion != MachineSchemaVersion || snapshot.MasterVersion != string(m.master.Version()) || snapshot.Checkpoint != checksum(snapshot) {
		return ErrNotReady
	}
	m.snapshot = cloneSnapshot(snapshot)
	return nil
}

func (m *Machine) checkpoint() {
	m.snapshot.Checkpoint = ""
	m.snapshot.Checkpoint = checksum(m.snapshot)
}
func checksum(v Snapshot) string {
	return hash("checkpoint", v.SchemaVersion, string(v.Mode), v.MasterVersion, v.Selection.OptionID, v.LatestSignal.ID, v.Risk.DecisionID, fmt.Sprint(v.Position), fmt.Sprint(v.Fills))
}
func digestSignal(s Signal) string {
	return hash(s.ID, s.At.UTC().Format(time.RFC3339Nano), fmt.Sprint(s.Spot.MinorUnits()), fmt.Sprint(s.FastEMAScaled), fmt.Sprint(s.SlowEMAScaled), s.Direction)
}
func hash(values ...string) string {
	h := sha256.New()
	for _, v := range values {
		h.Write([]byte(v))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func multiply(a, b int64) (int64, bool) {
	if a <= 0 || b <= 0 || a > math.MaxInt64/b {
		return 0, false
	}
	return a * b, true
}
func cloneSnapshot(v Snapshot) Snapshot { v.Fills = append([]Fill(nil), v.Fills...); return v }
func view(s Selection) SelectionView {
	i := s.Option.Instrument
	return SelectionView{FutureID: s.Future.Instrument.ID().String(), FutureSymbol: s.Future.Instrument.Symbol(), OptionID: i.ID().String(), OptionSymbol: i.Symbol(), Expiry: i.Expiry().String(), OptionType: string(i.OptionType()), Policy: s.Option.Policy, StrikeMinor: i.Strike().MinorUnits(), ReferenceMinor: s.ReferencePrice.MinorUnits(), LotSize: i.LotSize().Int64(), TickSizeMinor: i.TickSize().MinorUnits()}
}
func (m *Machine) emit(signal Signal, s Selection, kind notification.Kind, severity notification.Severity, source string, qty, price int64, reason string) {
	if m.observer == nil {
		return
	}
	event, err := notification.NewEvent(notification.EventSpec{SourceID: source, TradingDate: civil(signal.At), Mode: string(m.mode), OccurredAt: signal.At, Category: category(kind), Kind: kind, Severity: severity, Details: notification.Details{StrategyID: "nifty-ema-crossover-paper", InstrumentID: s.Option.Instrument.ID().String(), ReferenceID: source, Quantity: qty, PriceMinor: price, Currency: "INR", Reason: reason, Subject: fmt.Sprintf("NIFTY spot=%d future=%s:%d option=%s EMA20=%d EMA50=%d", signal.Spot.MinorUnits(), s.Future.Instrument.Symbol(), s.ReferencePrice.MinorUnits(), s.Option.Instrument.Symbol(), signal.FastEMAScaled, signal.SlowEMAScaled)}})
	if err == nil {
		func() {
			defer func() { _ = recover() }()
			m.observer.Observe(event)
		}()
	}
}
func category(kind notification.Kind) notification.Category {
	if kind == notification.KindRiskRejected {
		return notification.CategoryRisk
	}
	if kind == notification.KindProposalGenerated {
		return notification.CategoryProposal
	}
	return notification.CategoryExecution
}
