package tradingruntime

import (
	"context"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
)

type SessionState string

const (
	SessionStarting      SessionState = "STARTING"
	SessionPreMarket     SessionState = "PRE_MARKET"
	SessionWarmingUp     SessionState = "WARMING_UP"
	SessionReady         SessionState = "READY"
	SessionNormalTrading SessionState = "NORMAL_TRADING"
	SessionPreCAS        SessionState = "PRE_CAS"
	SessionCASActive     SessionState = "CAS_ACTIVE"
	SessionPostCAS       SessionState = "POST_CAS"
	SessionClosing       SessionState = "CLOSING"
	SessionClosed        SessionState = "CLOSED"
	SessionDegraded      SessionState = "DEGRADED"
	SessionHalted        SessionState = "HALTED"
	SessionDraining      SessionState = "DRAINING"
	SessionStopped       SessionState = "STOPPED"
)

type SessionCalendar interface {
	Day(context.Context, domain.Exchange, domain.CivilDate) (calendar.TradingDay, error)
	RegimeAt(context.Context, domain.Exchange, time.Time) (calendar.Regime, bool, error)
	Version() calendar.Version
	Timezone() string
}

type SessionTransition struct {
	From            SessionState    `json:"from"`
	To              SessionState    `json:"to"`
	Regime          calendar.Regime `json:"regime,omitempty"`
	At              time.Time       `json:"at"`
	CalendarVersion string          `json:"calendar_version"`
	Reason          string          `json:"reason"`
}

type SessionSnapshot struct {
	State           SessionState        `json:"state"`
	Regime          calendar.Regime     `json:"regime,omitempty"`
	CalendarVersion string              `json:"calendar_version"`
	UpdatedAt       time.Time           `json:"updated_at"`
	Transitions     []SessionTransition `json:"transitions,omitempty"`
}

type SessionCoordinator struct {
	calendar SessionCalendar
	exchange domain.Exchange
	mu       sync.Mutex
	snapshot SessionSnapshot
	maximum  int
}

func NewSessionCoordinator(source SessionCalendar, exchange domain.Exchange) (*SessionCoordinator, error) {
	if source == nil || exchange == "" || source.Timezone() != "Asia/Kolkata" {
		return nil, ErrInvalid
	}
	return &SessionCoordinator{calendar: source, exchange: exchange, maximum: 256, snapshot: SessionSnapshot{State: SessionStarting, CalendarVersion: string(source.Version())}}, nil
}

func (c *SessionCoordinator) Snapshot() SessionSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := c.snapshot
	result.Transitions = append([]SessionTransition(nil), result.Transitions...)
	return result
}

func (c *SessionCoordinator) Advance(ctx context.Context, at time.Time, ready bool) (SessionSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if at.IsZero() || ctx.Err() != nil {
		return c.snapshot, ErrInvalid
	}
	location, err := time.LoadLocation(c.calendar.Timezone())
	if err != nil {
		return c.snapshot, err
	}
	local := at.In(location)
	date, err := domain.NewCivilDate(local.Year(), local.Month(), local.Day())
	if err != nil {
		return c.snapshot, err
	}
	day, err := c.calendar.Day(ctx, c.exchange, date)
	if err != nil {
		return c.transitionLocked(SessionHalted, "CALENDAR_UNAVAILABLE", at, "")
	}
	desired, regime := classifySession(day, at, ready, c.snapshot.State, c.calendar, ctx, c.exchange)
	return c.transitionLocked(desired, "CALENDAR_EVALUATION", at, regime)
}

func classifySession(day calendar.TradingDay, at time.Time, ready bool, current SessionState, source SessionCalendar, ctx context.Context, exchange domain.Exchange) (SessionState, calendar.Regime) {
	if day.Status == calendar.DayHoliday || len(day.Sessions) == 0 {
		return SessionClosed, ""
	}
	first, last := day.Sessions[0].Open, day.Sessions[len(day.Sessions)-1].Close
	if at.Before(first) {
		return SessionPreMarket, ""
	}
	if !at.Before(last) {
		if current == SessionClosing || current == SessionClosed {
			return SessionClosed, ""
		}
		return SessionClosing, ""
	}
	regime, inside, err := source.RegimeAt(ctx, exchange, at)
	if err != nil || !inside {
		return SessionDegraded, ""
	}
	if !ready {
		return SessionWarmingUp, regime
	}
	if current == SessionStarting || current == SessionPreMarket || current == SessionWarmingUp || current == SessionDegraded {
		return SessionReady, regime
	}
	switch regime {
	case calendar.RegimePreCAS:
		return SessionPreCAS, regime
	case calendar.RegimeCAS:
		return SessionCASActive, regime
	case calendar.RegimePostCAS:
		return SessionPostCAS, regime
	default:
		return SessionNormalTrading, calendar.RegimeNormal
	}
}

func (c *SessionCoordinator) Degrade(at time.Time, reason string) (SessionSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transitionLocked(SessionDegraded, reason, at, c.snapshot.Regime)
}
func (c *SessionCoordinator) Halt(at time.Time, reason string) (SessionSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transitionLocked(SessionHalted, reason, at, c.snapshot.Regime)
}
func (c *SessionCoordinator) Drain(at time.Time) (SessionSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transitionLocked(SessionDraining, "SHUTDOWN", at, c.snapshot.Regime)
}
func (c *SessionCoordinator) Stop(at time.Time) (SessionSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transitionLocked(SessionStopped, "SHUTDOWN_COMPLETE", at, c.snapshot.Regime)
}

func (c *SessionCoordinator) transitionLocked(to SessionState, reason string, at time.Time, regime calendar.Regime) (SessionSnapshot, error) {
	from := c.snapshot.State
	if from == to {
		c.snapshot.UpdatedAt, c.snapshot.Regime = at.UTC(), regime
		return c.snapshot, nil
	}
	if !legalSessionTransition(from, to) {
		return c.snapshot, ErrInvalidTransition
	}
	transition := SessionTransition{From: from, To: to, Regime: regime, At: at.UTC(), CalendarVersion: string(c.calendar.Version()), Reason: reason}
	c.snapshot.State, c.snapshot.Regime, c.snapshot.UpdatedAt = to, regime, at.UTC()
	c.snapshot.Transitions = append(c.snapshot.Transitions, transition)
	if len(c.snapshot.Transitions) > c.maximum {
		c.snapshot.Transitions = append([]SessionTransition(nil), c.snapshot.Transitions[len(c.snapshot.Transitions)-c.maximum:]...)
	}
	return c.snapshot, nil
}

func legalSessionTransition(from, to SessionState) bool {
	if to == SessionDraining {
		return from != SessionStopped
	}
	if to == SessionHalted || to == SessionDegraded {
		return from != SessionDraining && from != SessionStopped
	}
	allowed := map[SessionState]map[SessionState]bool{
		SessionStarting:      {SessionPreMarket: true, SessionWarmingUp: true, SessionReady: true, SessionClosed: true},
		SessionPreMarket:     {SessionWarmingUp: true, SessionReady: true, SessionClosed: true},
		SessionWarmingUp:     {SessionReady: true, SessionClosing: true},
		SessionReady:         {SessionNormalTrading: true, SessionPreCAS: true, SessionCASActive: true, SessionPostCAS: true, SessionClosing: true},
		SessionNormalTrading: {SessionPreCAS: true, SessionClosing: true, SessionWarmingUp: true},
		SessionPreCAS:        {SessionCASActive: true, SessionClosing: true, SessionWarmingUp: true},
		SessionCASActive:     {SessionPostCAS: true, SessionClosing: true, SessionWarmingUp: true},
		SessionPostCAS:       {SessionClosing: true, SessionWarmingUp: true},
		SessionClosing:       {SessionClosed: true}, SessionClosed: {SessionStopped: true},
		SessionDegraded: {SessionReady: true, SessionWarmingUp: true, SessionClosing: true, SessionClosed: true},
		SessionHalted:   {SessionWarmingUp: true, SessionReady: true}, SessionDraining: {SessionStopped: true},
	}
	return allowed[from][to]
}
