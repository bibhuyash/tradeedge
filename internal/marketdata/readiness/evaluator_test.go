package readiness

import (
	"context"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

func TestEvaluatorQuoteLifecycleAndClosedSession(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Kolkata")
	date, _ := domain.NewCivilDate(2026, time.July, 20)
	open := time.Date(2026, 7, 20, 9, 15, 0, 0, location)
	closeTime := time.Date(2026, 7, 20, 15, 30, 0, 0, location)
	schedule := readinessSchedule(t, date, []calendar.Session{{
		Open: open, Close: closeTime, Kind: calendar.SessionRegular,
	}})
	id, _ := domain.InstrumentIDFromCanonicalKey("quote")
	watchlist, err := NewWatchlist("primary", []Requirement{{
		Provider: "fixture", InstrumentID: id, Exchange: domain.ExchangeNSE,
		Segment: domain.SegmentIndex, EventKind: model.EventKindQuote, Required: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: open.Add(10 * time.Second)}
	evaluator, err := New(clock, schedule, DefaultPolicy(), []Watchlist{watchlist})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := evaluator.Snapshot(context.Background()); snapshot.State != StateWarmingUp {
		t.Fatalf("warmup state = %s", snapshot.State)
	}
	clock.now = open.Add(time.Minute)
	if snapshot := evaluator.Snapshot(context.Background()); snapshot.State != StateNoData {
		t.Fatalf("no-data state = %s", snapshot.State)
	}
	evaluator.Accepted(quote(t, id, clock.now.Add(-time.Second), clock.now.Add(-500*time.Millisecond)))
	if snapshot := evaluator.Snapshot(context.Background()); snapshot.State != StateReady || !snapshot.TradingPermitted {
		t.Fatalf("ready snapshot = %#v", snapshot)
	}
	clock.now = clock.now.Add(10 * time.Second)
	if snapshot := evaluator.Snapshot(context.Background()); snapshot.State != StateStale ||
		snapshot.Reasons[0] != ReasonExchangeTimeStale {
		t.Fatalf("stale snapshot = %#v", snapshot)
	}
	clock.now = closeTime.Add(time.Minute)
	if snapshot := evaluator.Snapshot(context.Background()); snapshot.State != StateSessionClosed ||
		snapshot.TradingPermitted {
		t.Fatalf("closed snapshot = %#v", snapshot)
	}
}

func TestEvaluatorFailsClosedForMissingCandleAfterClose(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Kolkata")
	date, _ := domain.NewCivilDate(2026, time.July, 20)
	open := time.Date(2026, 7, 20, 9, 15, 0, 0, location)
	closeTime := open.Add(time.Minute)
	schedule := readinessSchedule(t, date, []calendar.Session{{
		Open: open, Close: closeTime, Kind: calendar.SessionSpecial,
	}})
	id, _ := domain.InstrumentIDFromCanonicalKey("candle")
	watchlist, _ := NewWatchlist("candles", []Requirement{{
		Provider: "fixture", InstrumentID: id, Exchange: domain.ExchangeNSE,
		Segment: domain.SegmentIndex, EventKind: model.EventKindCandle,
		Interval: model.Interval1Minute, Required: true,
	}})
	clock := &testClock{now: closeTime.Add(11 * time.Second)}
	evaluator, _ := New(clock, schedule, DefaultPolicy(), []Watchlist{watchlist})
	snapshot := evaluator.Snapshot(context.Background())
	if snapshot.State != StateIncomplete || snapshot.OperationallyReady() {
		t.Fatalf("missing candle snapshot = %#v", snapshot)
	}
}

func TestEarlierMissingCandleOverridesLaterAcceptedCandleAndClosedSession(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Kolkata")
	date, _ := domain.NewCivilDate(2026, time.July, 20)
	open := time.Date(2026, 7, 20, 9, 15, 0, 0, location)
	closeTime := open.Add(2 * time.Minute)
	schedule := readinessSchedule(t, date, []calendar.Session{{
		Open: open, Close: closeTime, Kind: calendar.SessionSpecial,
	}})
	id, _ := domain.InstrumentIDFromCanonicalKey("candle-gap")
	watchlist, _ := NewWatchlist("candles", []Requirement{{
		Provider: "fixture", InstrumentID: id, Exchange: domain.ExchangeNSE,
		Segment: domain.SegmentIndex, EventKind: model.EventKindCandle,
		Interval: model.Interval1Minute, Required: true,
	}})
	clock := &testClock{now: closeTime.Add(11 * time.Second)}
	evaluator, _ := New(clock, schedule, DefaultPolicy(), []Watchlist{watchlist})
	evaluator.MarkMissing("fixture", id, model.Interval1Minute, open, open.Add(time.Minute))
	evaluator.Accepted(candle(t, id, open.Add(time.Minute)))
	snapshot := evaluator.Snapshot(context.Background())
	if snapshot.State != StateIncomplete || snapshot.Diagnostics[0].MissingOpen != open {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestEvaluatorDisabledIsOperationalButNeverTradable(t *testing.T) {
	evaluator, err := New(&testClock{now: time.Now()}, nil, DefaultPolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := evaluator.Snapshot(context.Background())
	if snapshot.State != StateDisabled || !snapshot.OperationallyReady() || snapshot.TradingPermitted {
		t.Fatalf("disabled snapshot = %#v", snapshot)
	}
}

func TestEvaluatorDistinguishesFreshnessFailureReasons(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Kolkata")
	date, _ := domain.NewCivilDate(2026, time.July, 20)
	open := time.Date(2026, 7, 20, 9, 15, 0, 0, location)
	schedule := readinessSchedule(t, date, []calendar.Session{{
		Open: open, Close: open.Add(time.Hour), Kind: calendar.SessionRegular,
	}})
	id, _ := domain.InstrumentIDFromCanonicalKey("freshness")
	watchlist, _ := NewWatchlist("primary", []Requirement{{
		Provider: "fixture", InstrumentID: id, Exchange: domain.ExchangeNSE,
		Segment: domain.SegmentIndex, EventKind: model.EventKindQuote, Required: true,
	}})
	now := open.Add(time.Minute)
	tests := []struct {
		name     string
		policy   FreshnessPolicy
		exchange time.Time
		ingested time.Time
		want     ReasonCode
	}{
		{
			name: "exchange age", policy: DefaultPolicy(),
			exchange: now.Add(-6 * time.Second), ingested: now.Add(-5 * time.Second),
			want: ReasonExchangeTimeStale,
		},
		{
			name: "ingestion age", policy: func() FreshnessPolicy {
				value := DefaultPolicy()
				value.Quote.MaxExchangeAge = 10 * time.Second
				return value
			}(),
			exchange: now.Add(-7 * time.Second), ingested: now.Add(-6 * time.Second),
			want: ReasonIngestionTimeStale,
		},
		{
			name: "transport lag", policy: DefaultPolicy(),
			exchange: now.Add(-3 * time.Second), ingested: now.Add(-500 * time.Millisecond),
			want: ReasonTransportLagExceeded,
		},
		{
			name: "clock skew", policy: DefaultPolicy(),
			exchange: now.Add(2 * time.Second), ingested: now.Add(2 * time.Second),
			want: ReasonClockSkew,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := &testClock{now: now}
			evaluator, err := New(clock, schedule, test.policy, []Watchlist{watchlist})
			if err != nil {
				t.Fatal(err)
			}
			evaluator.Accepted(quote(t, id, test.exchange, test.ingested))
			snapshot := evaluator.Snapshot(context.Background())
			if snapshot.State != StateStale || len(snapshot.Reasons) == 0 || snapshot.Reasons[0] != test.want {
				t.Fatalf("snapshot = %#v, want %s", snapshot, test.want)
			}
		})
	}
}

func TestOptionalRequirementDoesNotBlockRequiredStream(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Kolkata")
	date, _ := domain.NewCivilDate(2026, time.July, 20)
	open := time.Date(2026, 7, 20, 9, 15, 0, 0, location)
	schedule := readinessSchedule(t, date, []calendar.Session{{
		Open: open, Close: open.Add(time.Hour), Kind: calendar.SessionRegular,
	}})
	requiredID, _ := domain.InstrumentIDFromCanonicalKey("required")
	optionalID, _ := domain.InstrumentIDFromCanonicalKey("optional")
	watchlist, _ := NewWatchlist("primary", []Requirement{
		{Provider: "fixture", InstrumentID: requiredID, Exchange: domain.ExchangeNSE,
			Segment: domain.SegmentIndex, EventKind: model.EventKindQuote, Required: true},
		{Provider: "fixture", InstrumentID: optionalID, Exchange: domain.ExchangeNSE,
			Segment: domain.SegmentIndex, EventKind: model.EventKindQuote, Required: false},
	})
	clock := &testClock{now: open.Add(time.Minute)}
	evaluator, _ := New(clock, schedule, DefaultPolicy(), []Watchlist{watchlist})
	evaluator.Accepted(quote(t, requiredID, clock.now.Add(-time.Second), clock.now.Add(-500*time.Millisecond)))
	snapshot := evaluator.Snapshot(context.Background())
	if snapshot.State != StateReady || !snapshot.TradingPermitted {
		t.Fatalf("optional stream blocked readiness: %#v", snapshot)
	}
}

func readinessSchedule(t *testing.T, date domain.CivilDate, sessions []calendar.Session) *calendar.Schedule {
	t.Helper()
	schedule, err := calendar.New(calendar.Spec{
		Source:   calendar.Source{Name: "fixture", PublishedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
		Timezone: "Asia/Kolkata", EffectiveFrom: date, EffectiveTo: date,
		Days: []calendar.TradingDay{{
			Exchange: domain.ExchangeNSE, Date: date, Status: calendar.DayTrading, Sessions: sessions,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return schedule
}

func quote(t *testing.T, id domain.InstrumentID, exchange, ingested time.Time) model.QuoteEvent {
	t.Helper()
	price, _ := domain.NewPrice(100, "INR")
	event, err := model.NewQuoteEvent(model.QuoteSpec{
		InstrumentID: id, LastPrice: price, ExchangeTime: exchange, IngestedAt: ingested,
		Provenance: model.Provenance{Provider: "fixture", ProviderToken: "1", MasterVersion: "v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func candle(t *testing.T, id domain.InstrumentID, open time.Time) model.CompletedCandleEvent {
	t.Helper()
	openPrice, _ := domain.NewPrice(100, "INR")
	high, _ := domain.NewPrice(110, "INR")
	low, _ := domain.NewPrice(90, "INR")
	closePrice, _ := domain.NewPrice(105, "INR")
	event, err := model.NewCompletedCandleEvent(model.CandleSpec{
		InstrumentID: id, Interval: model.Interval1Minute,
		OpenTime: open, CloseTime: open.Add(time.Minute),
		Open: openPrice, High: high, Low: low, Close: closePrice, Volume: 10,
		IngestedAt: open.Add(time.Minute + time.Second),
		Provenance: model.Provenance{Provider: "fixture", ProviderToken: "1", MasterVersion: "v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}
