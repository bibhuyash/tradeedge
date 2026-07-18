package quality

import (
	"context"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

func TestTrackerDetectsNoDataAndStaleness(t *testing.T) {
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	tracker := NewTracker(clock, nil, 2*time.Second)
	id, _ := domain.InstrumentIDFromCanonicalKey("instrument")
	state, err := tracker.State(context.Background(), id, domain.ExchangeNSE)
	if err != nil || state != model.DataNoData {
		t.Fatalf("initial State() = %s, %v", state, err)
	}
	price, _ := domain.NewPrice(100, "INR")
	event, _ := model.NewQuoteEvent(model.QuoteSpec{
		InstrumentID: id, LastPrice: price, ExchangeTime: now, IngestedAt: now,
		Provenance: model.Provenance{Provider: "fixture", ProviderToken: "1", MasterVersion: "v1"},
	})
	tracker.Accepted(event)
	state, _ = tracker.State(context.Background(), id, domain.ExchangeNSE)
	if state != model.DataCurrent {
		t.Fatalf("current State() = %s", state)
	}
	clock.now = now.Add(3 * time.Second)
	state, _ = tracker.State(context.Background(), id, domain.ExchangeNSE)
	if state != model.DataStale {
		t.Fatalf("stale State() = %s", state)
	}
}
