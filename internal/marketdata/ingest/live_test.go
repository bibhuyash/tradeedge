package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	"github.com/bibhuyash/tradeedge/internal/marketdata"
	"github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
	"github.com/bibhuyash/tradeedge/internal/marketdata/latest"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	marketreadiness "github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
)

type liveConsumer struct{ events []model.Event }

func (c *liveConsumer) Process(_ context.Context, event model.Event) error {
	c.events = append(c.events, event)
	return nil
}

type liveObserver struct {
	accepted int
	quality  []model.QualityRecord
}

func (o *liveObserver) Accepted(model.Event)              { o.accepted++ }
func (o *liveObserver) Quality(value model.QualityRecord) { o.quality = append(o.quality, value) }

func TestLiveServiceCanonicalizesAndSuppressesDuplicate(t *testing.T) {
	instrument, err := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentIndex, UnderlyingID: "NIFTY", Type: domain.InstrumentIndex, ExchangeSymbol: "NIFTY 50", LotSize: mustQuantity(t, 1), TickSize: mustPrice(t, 5), Currency: "INR"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC)
	master, err := instrumentmaster.New(at.Add(-time.Hour), []domain.Instrument{instrument}, []domain.ProviderInstrumentRef{{Provider: "zerodha", Token: "256265", TradingSymbol: "NIFTY 50", InstrumentID: instrument.ID(), ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	repository := instrumentmaster.NewMemoryRepository()
	if err := repository.Put(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	consumer, observer := &liveConsumer{}, &liveObserver{}
	service, err := NewLiveService(Normalizer{Resolver: instrumentmaster.Resolver{Repository: repository}}, observer, consumer, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	observation := marketdata.Observation{Kind: marketdata.ObservationQuote, Provider: "zerodha", ProviderToken: "256265", ExchangeTime: at, IngestedAt: at.Add(time.Millisecond), LastMinor: 2450000, Currency: "INR"}
	if err := service.Accept(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	if err := service.Accept(context.Background(), observation); err != nil {
		t.Fatal(err)
	}
	if len(consumer.events) != 1 || observer.accepted != 1 || len(observer.quality) != 1 || observer.quality[0].Code != model.QualityDuplicate {
		t.Fatalf("events=%d accepted=%d quality=%#v", len(consumer.events), observer.accepted, observer.quality)
	}
}

type liveReadinessClock struct{ now time.Time }

func (c liveReadinessClock) Now() time.Time { return c.now }

func TestLiveServicePublishesObservationOnlyIndexesToReadiness(t *testing.T) {
	location, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	date, _ := domain.NewCivilDate(2026, time.August, 11)
	open := time.Date(2026, time.August, 11, 9, 15, 0, 0, location)
	now := open.Add(time.Minute)
	schedule, err := calendar.New(calendar.Spec{
		Source: calendar.Source{Name: "test", PublishedAt: open.Add(-time.Hour)}, Timezone: "Asia/Kolkata",
		EffectiveFrom: date, EffectiveTo: date,
		Days: []calendar.TradingDay{{Exchange: domain.ExchangeNSE, Date: date, Status: calendar.DayTrading,
			Sessions: []calendar.Session{{Open: open, Close: open.Add(6 * time.Hour), Kind: calendar.SessionRegular}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	nifty := observationOnlyIndex(t, "NIFTY", "NIFTY 50")
	bankNifty := observationOnlyIndex(t, "BANKNIFTY", "NIFTY BANK")
	mappings := []domain.ProviderInstrumentRef{
		{Provider: "zerodha", Token: "256265", TradingSymbol: "NIFTY 50", InstrumentID: nifty.ID(), ValidFrom: open.Add(-time.Hour), ValidUntil: open.Add(8 * time.Hour)},
		{Provider: "zerodha", Token: "260105", TradingSymbol: "NIFTY BANK", InstrumentID: bankNifty.ID(), ValidFrom: open.Add(-time.Hour), ValidUntil: open.Add(8 * time.Hour)},
	}
	watchlist, err := marketreadiness.NewWatchlist("day0-indexes", []marketreadiness.Requirement{
		{Provider: "zerodha", InstrumentID: nifty.ID(), Exchange: domain.ExchangeNSE, Segment: domain.SegmentIndex, EventKind: model.EventKindQuote, Required: true},
		{Provider: "zerodha", InstrumentID: bankNifty.ID(), Exchange: domain.ExchangeNSE, Segment: domain.SegmentIndex, EventKind: model.EventKindQuote, Required: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	evaluator, err := marketreadiness.New(liveReadinessClock{now: now}, schedule, marketreadiness.DefaultPolicy(), []marketreadiness.Watchlist{watchlist})
	if err != nil {
		t.Fatal(err)
	}
	master, err := instrumentmaster.New(now.Add(-time.Hour), []domain.Instrument{nifty, bankNifty}, mappings)
	if err != nil {
		t.Fatal(err)
	}
	latestStore, err := latest.New(master, watchlist)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := evaluator.Snapshot(context.Background()); snapshot.State != marketreadiness.StateNoData {
		t.Fatalf("initial readiness = %s, want NO_DATA", snapshot.State)
	}
	consumer := &liveConsumer{}
	service, err := NewLiveService(
		Normalizer{Resolver: resolverForInstruments(t, now, []domain.Instrument{nifty, bankNifty}, mappings), Calendar: schedule},
		ObserverGroup{evaluator, latestStore}, consumer, 0, 8,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Accept(context.Background(), quoteForToken("256265", now.Add(-time.Second), 2450123)); err != nil {
		t.Fatal(err)
	}
	if snapshot := evaluator.Snapshot(context.Background()); snapshot.State != marketreadiness.StateNoData {
		t.Fatalf("one-instrument readiness = %s, want NO_DATA", snapshot.State)
	}
	if err := service.Accept(context.Background(), quoteForToken("260105", now.Add(-500*time.Millisecond), 5400123)); err != nil {
		t.Fatal(err)
	}
	if snapshot := evaluator.Snapshot(context.Background()); snapshot.State != marketreadiness.StateReady || !snapshot.TradingPermitted {
		t.Fatalf("two-instrument readiness = %#v", snapshot)
	}
	if len(consumer.events) != 2 {
		t.Fatalf("accepted events = %d, want 2", len(consumer.events))
	}
	items := latestStore.Snapshot(context.Background())
	if len(items) != 2 || (items[0].Symbol != "NIFTY 50" && items[1].Symbol != "NIFTY 50") || (items[0].Symbol != "NIFTY BANK" && items[1].Symbol != "NIFTY BANK") {
		t.Fatalf("latest observations = %#v", items)
	}
	invalid := quoteForToken("256265", now, 2450999)
	invalid.Currency = ""
	if err := service.Accept(context.Background(), invalid); err != nil {
		t.Fatal(err)
	}
	if got := latestStore.Snapshot(context.Background()); len(got) != 2 {
		t.Fatalf("quarantined observation changed bounded latest state: %#v", got)
	}
}

func mustQuantity(t *testing.T, value int64) domain.Quantity {
	t.Helper()
	result, err := domain.NewQuantity(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func mustPrice(t *testing.T, value int64) domain.Price {
	t.Helper()
	result, err := domain.NewPrice(value, "INR")
	if err != nil {
		t.Fatal(err)
	}
	return result
}
