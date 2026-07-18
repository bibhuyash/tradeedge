package model

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

var ErrInvalidSession = errors.New("invalid market session")

type MarketSession struct {
	Exchange    domain.Exchange
	TradingDate domain.CivilDate
	Open        time.Time
	Close       time.Time
	Version     string
}

func (s MarketSession) Validate() error {
	if s.Exchange == "" || s.TradingDate.IsZero() || !s.Open.Before(s.Close) || s.Version == "" {
		return ErrInvalidSession
	}
	return nil
}

func (s MarketSession) Contains(at time.Time) bool {
	return !at.Before(s.Open) && at.Before(s.Close)
}

type MarketCalendar interface {
	SessionAt(ctx context.Context, exchange domain.Exchange, at time.Time) (MarketSession, bool, error)
}

type StaticCalendar struct {
	mu       sync.RWMutex
	sessions []MarketSession
}

func NewStaticCalendar(sessions []MarketSession) (*StaticCalendar, error) {
	copied := append([]MarketSession(nil), sessions...)
	for _, session := range copied {
		if err := session.Validate(); err != nil {
			return nil, err
		}
	}
	return &StaticCalendar{sessions: copied}, nil
}

func (c *StaticCalendar) SessionAt(
	ctx context.Context,
	exchange domain.Exchange,
	at time.Time,
) (MarketSession, bool, error) {
	if err := ctx.Err(); err != nil {
		return MarketSession{}, false, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, session := range c.sessions {
		if session.Exchange == exchange &&
			!at.Before(session.Open) && at.Before(session.Close) {
			return session, true, nil
		}
	}
	return MarketSession{}, false, nil
}
