package quality

import (
	"context"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

type Clock interface {
	Now() time.Time
}

type Snapshot struct {
	Accepted       uint64
	ByQuality      map[model.QualityCode]uint64
	LastAcceptedAt map[domain.InstrumentID]time.Time
}

type Tracker struct {
	mu         sync.RWMutex
	clock      Clock
	calendar   model.MarketCalendar
	staleAfter time.Duration
	accepted   uint64
	byQuality  map[model.QualityCode]uint64
	last       map[domain.InstrumentID]time.Time
}

func NewTracker(clock Clock, calendar model.MarketCalendar, staleAfter time.Duration) *Tracker {
	return &Tracker{
		clock: clock, calendar: calendar, staleAfter: staleAfter,
		byQuality: make(map[model.QualityCode]uint64),
		last:      make(map[domain.InstrumentID]time.Time),
	}
}

func (t *Tracker) Accepted(event model.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.accepted++
	t.last[event.InstrumentID()] = event.ExchangeTime()
}

func (t *Tracker) Quality(record model.QualityRecord) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byQuality[record.Code]++
}

func (t *Tracker) State(
	ctx context.Context,
	id domain.InstrumentID,
	exchange domain.Exchange,
) (model.DataState, error) {
	now := t.clock.Now()
	if t.calendar != nil {
		_, active, err := t.calendar.SessionAt(ctx, exchange, now)
		if err != nil {
			return model.DataNoData, err
		}
		if !active {
			return model.DataSessionClosed, nil
		}
	}
	t.mu.RLock()
	last, found := t.last[id]
	t.mu.RUnlock()
	if !found {
		return model.DataNoData, nil
	}
	if now.Sub(last) > t.staleAfter {
		return model.DataStale, nil
	}
	return model.DataCurrent, nil
}

func (t *Tracker) Snapshot() Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	snapshot := Snapshot{
		Accepted:       t.accepted,
		ByQuality:      make(map[model.QualityCode]uint64, len(t.byQuality)),
		LastAcceptedAt: make(map[domain.InstrumentID]time.Time, len(t.last)),
	}
	for code, count := range t.byQuality {
		snapshot.ByQuality[code] = count
	}
	for id, at := range t.last {
		snapshot.LastAcceptedAt[id] = at
	}
	return snapshot
}
