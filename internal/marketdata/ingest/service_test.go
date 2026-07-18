package ingest

import (
	"context"
	"testing"
	"time"

	memorysource "github.com/bibhuyash/tradeedge/internal/adapters/marketdata/memory"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	"github.com/bibhuyash/tradeedge/internal/marketdata"
	"github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/quality"
	"github.com/bibhuyash/tradeedge/internal/marketdata/storage"
)

func TestIngestSuppressesDuplicatesAndOrdersDeterministically(t *testing.T) {
	resolver, master, instrument := testResolver(t)
	base := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	firstAt := base.Add(time.Second)
	secondAt := base.Add(2 * time.Second)
	observations := []marketdata.Observation{
		quoteObservation(secondAt, 10200),
		quoteObservation(firstAt, 10100),
		quoteObservation(firstAt, 10000),
		quoteObservation(firstAt, 10100),
	}
	repository := storage.NewMemoryRepository()
	writer, err := repository.Create(context.Background(), storage.DraftManifest{
		MasterVersion: string(master.Version()), CalendarVersion: "calendar-v1", Source: "test",
		OrderingVersion: "exchange-sequence-event-id/v1", CreatedAt: base,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	service := Service{
		Normalizer:      Normalizer{Resolver: resolver},
		AllowedLateness: 2 * time.Second,
		BufferCapacity:  100,
	}
	if err := service.Ingest(context.Background(), memorysource.Source{Observations: observations},
		marketdata.SourceQuery{Mode: marketdata.SourceHistorical}, writer); err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	manifest, err := writer.Commit(context.Background())
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if manifest.EventCount != 3 || manifest.QualityCount != 1 {
		t.Fatalf("manifest counts = %d events, %d quality", manifest.EventCount, manifest.QualityCount)
	}
	reader, _ := repository.Open(context.Background(), manifest.ID)
	var events []model.Event
	if err := reader.Scan(context.Background(), storage.EventQuery{}, func(_ context.Context, event model.Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(events) != 3 || events[0].InstrumentID() != instrument.ID() {
		t.Fatalf("events = %#v", events)
	}
	for index := 1; index < len(events); index++ {
		if model.EventLess(events[index], events[index-1]) {
			t.Fatalf("events are out of order at %d", index)
		}
	}
	if !events[0].ExchangeTime().Equal(firstAt) || !events[1].ExchangeTime().Equal(firstAt) {
		t.Fatalf("equal-timestamp events were not retained: %#v", events)
	}
}

func TestIngestQuarantinesEventsOlderThanWatermark(t *testing.T) {
	resolver, master, _ := testResolver(t)
	base := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	repository := storage.NewMemoryRepository()
	writer, _ := repository.Create(context.Background(), storage.DraftManifest{
		MasterVersion: string(master.Version()), CalendarVersion: "calendar-v1", Source: "test", OrderingVersion: "v1", CreatedAt: base,
	})
	service := Service{Normalizer: Normalizer{Resolver: resolver}, AllowedLateness: time.Second, BufferCapacity: 10}
	source := memorysource.Source{Observations: []marketdata.Observation{
		quoteObservation(base.Add(3*time.Second), 10000),
		quoteObservation(base, 9900),
	}}
	if err := service.Ingest(context.Background(), source, marketdata.SourceQuery{}, writer); err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	manifest, _ := writer.Commit(context.Background())
	if manifest.EventCount != 1 || manifest.QualityCount != 1 {
		t.Fatalf("manifest counts = %#v", manifest)
	}
}

func TestIngestQuarantinesCurrencyAndTickMismatches(t *testing.T) {
	resolver, master, _ := testResolver(t)
	base := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	repository := storage.NewMemoryRepository()
	writer, _ := repository.Create(context.Background(), storage.DraftManifest{
		MasterVersion: string(master.Version()), CalendarVersion: "calendar-v1", Source: "test", OrderingVersion: "v1", CreatedAt: base,
	})
	wrongCurrency := quoteObservation(base, 10000)
	wrongCurrency.Currency = "USD"
	wrongTick := quoteObservation(base.Add(time.Second), 10001)
	service := Service{Normalizer: Normalizer{Resolver: resolver}, AllowedLateness: time.Second, BufferCapacity: 10}
	if err := service.Ingest(context.Background(), memorysource.Source{Observations: []marketdata.Observation{
		wrongCurrency, wrongTick,
	}}, marketdata.SourceQuery{}, writer); err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	manifest, _ := writer.Commit(context.Background())
	if manifest.EventCount != 0 || manifest.QualityCount != 2 {
		t.Fatalf("manifest counts = %#v", manifest)
	}
}

func TestIngestRecordsMissingCandleIntervals(t *testing.T) {
	resolver, master, instrument := testResolver(t)
	base := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	location, _ := time.LoadLocation("Asia/Kolkata")
	tradingDate, _ := domain.NewCivilDate(2026, time.July, 18)
	schedule, err := calendar.New(calendar.Spec{
		Source:   calendar.Source{Name: "test", PublishedAt: base.Add(-time.Hour)},
		Timezone: "Asia/Kolkata", EffectiveFrom: tradingDate, EffectiveTo: tradingDate,
		Days: []calendar.TradingDay{{
			Exchange: domain.ExchangeNSE, Date: tradingDate, Status: calendar.DayTrading,
			Sessions: []calendar.Session{{
				Open: base.In(location), Close: base.Add(3 * time.Minute).In(location),
				Kind: calendar.SessionSpecial,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("calendar.New() error = %v", err)
	}
	rangeEnd := base.Add(3*time.Minute + time.Nanosecond)
	gaps, err := quality.NewGapDetector(schedule, base, rangeEnd, []quality.GapRequirement{{
		Provider: "fixture", InstrumentID: instrument.ID(), Exchange: domain.ExchangeNSE,
		Interval: model.Interval1Minute,
	}})
	if err != nil {
		t.Fatalf("quality.NewGapDetector() error = %v", err)
	}
	repository := storage.NewMemoryRepository()
	writer, _ := repository.Create(context.Background(), storage.DraftManifest{
		MasterVersion: string(master.Version()), CalendarVersion: string(schedule.Version()), Source: "test", OrderingVersion: "v1", CreatedAt: base,
	})
	candle := func(start time.Time) marketdata.Observation {
		return marketdata.Observation{
			Kind: marketdata.ObservationCandle, Provider: "fixture", ProviderToken: "1",
			ExchangeTime: start.Add(time.Minute), IngestedAt: start.Add(time.Minute + time.Second),
			OpenTime: start, CloseTime: start.Add(time.Minute), Interval: model.Interval1Minute,
			OpenMinor: 10000, HighMinor: 10100, LowMinor: 9900, CloseMinor: 10050,
			Volume: 10, EventCount: 2, Currency: "INR",
		}
	}
	service := Service{
		Normalizer:      Normalizer{Resolver: resolver, Calendar: schedule},
		AllowedLateness: time.Minute, BufferCapacity: 10, Completeness: gaps,
	}
	if err := service.Ingest(context.Background(), memorysource.Source{Observations: []marketdata.Observation{
		candle(base), candle(base.Add(2 * time.Minute)),
	}}, marketdata.SourceQuery{Start: base, End: rangeEnd}, writer); err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	manifest, _ := writer.Commit(context.Background())
	if manifest.EventCount != 2 || manifest.QualityCount != 1 {
		t.Fatalf("manifest counts = %#v", manifest)
	}
}

func testResolver(t *testing.T) (instrumentmaster.Resolver, instrumentmaster.Master, domain.Instrument) {
	t.Helper()
	underlying, _ := domain.NewUnderlyingID("NIFTY")
	lot, _ := domain.NewQuantity(1)
	tick, _ := domain.NewPrice(5, "INR")
	currency, _ := domain.NewCurrency("INR")
	instrument, _ := domain.NewInstrument(domain.InstrumentSpec{
		Exchange: domain.ExchangeNSE, Segment: domain.SegmentIndex, UnderlyingID: underlying,
		Type: domain.InstrumentIndex, ExchangeSymbol: "NIFTY", LotSize: lot, TickSize: tick, Currency: currency,
	})
	base := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	master, err := instrumentmaster.New(base, []domain.Instrument{instrument}, []domain.ProviderInstrumentRef{{
		Provider: "fixture", Token: "1", TradingSymbol: "NIFTY",
		InstrumentID: instrument.ID(), ValidFrom: base, ValidUntil: base.Add(24 * time.Hour),
	}})
	if err != nil {
		t.Fatalf("instrumentmaster.New() error = %v", err)
	}
	repository := instrumentmaster.NewMemoryRepository()
	_ = repository.Put(context.Background(), master)
	return instrumentmaster.Resolver{Repository: repository}, master, instrument
}

func quoteObservation(at time.Time, minor int64) marketdata.Observation {
	return marketdata.Observation{
		Kind: marketdata.ObservationQuote, Provider: "fixture", ProviderToken: "1",
		ExchangeTime: at, IngestedAt: at.Add(time.Millisecond), LastMinor: minor,
		Volume: 1, Currency: "INR",
	}
}
