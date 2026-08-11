package latest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
)

func TestStoreRetainsOnlyLatestAcceptedConfiguredQuote(t *testing.T) {
	at := time.Date(2026, 8, 11, 9, 30, 0, 0, time.FixedZone("IST", 19800))
	underlying, _ := domain.NewUnderlyingID("NIFTY")
	lot, _ := domain.NewQuantity(0)
	tick, _ := domain.NewPrice(0, "INR")
	instrument, err := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentIndex, UnderlyingID: underlying, Type: domain.InstrumentIndex, ExchangeSymbol: "NIFTY 50", LotSize: lot, TickSize: tick, Currency: "INR"})
	if err != nil {
		t.Fatal(err)
	}
	master, _ := instrumentmaster.New(at.Add(-time.Hour), []domain.Instrument{instrument}, []domain.ProviderInstrumentRef{{Provider: "zerodha", Token: "256265", TradingSymbol: "NIFTY 50", InstrumentID: instrument.ID(), ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour)}})
	watchlist, _ := readiness.NewWatchlist("day0", []readiness.Requirement{{Provider: "zerodha", InstrumentID: instrument.ID(), Exchange: domain.ExchangeNSE, Segment: domain.SegmentIndex, EventKind: model.EventKindQuote, Required: true}})
	store, err := New(master, watchlist)
	if err != nil {
		t.Fatal(err)
	}
	newQuote := quote(t, instrument.ID(), at.Add(time.Second), 2450100)
	store.Accepted(newQuote)
	store.Accepted(quote(t, instrument.ID(), at, 2450000))
	items := store.Snapshot(context.Background())
	if len(items) != 1 || items[0].Symbol != "NIFTY 50" || items[0].LatestPriceMinor != 2450100 || items[0].ProviderToken != "256265" {
		t.Fatalf("snapshot = %#v", items)
	}
}

func TestStoreConcurrentSnapshotsRemainBounded(t *testing.T) {
	at := time.Date(2026, 8, 11, 9, 30, 0, 0, time.UTC)
	underlying, _ := domain.NewUnderlyingID("NIFTY")
	lot, _ := domain.NewQuantity(0)
	tick, _ := domain.NewPrice(0, "INR")
	instrument, _ := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentIndex, UnderlyingID: underlying, Type: domain.InstrumentIndex, ExchangeSymbol: "NIFTY 50", LotSize: lot, TickSize: tick, Currency: "INR"})
	master, _ := instrumentmaster.New(at.Add(-time.Hour), []domain.Instrument{instrument}, []domain.ProviderInstrumentRef{{Provider: "zerodha", Token: "256265", TradingSymbol: "NIFTY 50", InstrumentID: instrument.ID(), ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour)}})
	watchlist, _ := readiness.NewWatchlist("day0", []readiness.Requirement{{Provider: "zerodha", InstrumentID: instrument.ID(), Exchange: domain.ExchangeNSE, Segment: domain.SegmentIndex, EventKind: model.EventKindQuote, Required: true}})
	store, _ := New(master, watchlist)
	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func(offset int) {
			defer group.Done()
			for index := 0; index < 100; index++ {
				store.Accepted(quote(t, instrument.ID(), at.Add(time.Duration(offset*100+index)*time.Millisecond), 2450000+int64(index)))
				_ = store.Snapshot(context.Background())
			}
		}(worker)
	}
	group.Wait()
	if got := len(store.Snapshot(context.Background())); got != 1 {
		t.Fatalf("bounded size = %d", got)
	}
}

func quote(t *testing.T, id domain.InstrumentID, at time.Time, price int64) model.QuoteEvent {
	t.Helper()
	value, err := domain.NewPrice(price, "INR")
	if err != nil {
		t.Fatal(err)
	}
	event, err := model.NewQuoteEvent(model.QuoteSpec{InstrumentID: id, LastPrice: value, ExchangeTime: at, IngestedAt: at.Add(time.Millisecond), Provenance: model.Provenance{Provider: "zerodha", ProviderToken: "256265", MasterVersion: "v1"}})
	if err != nil {
		t.Fatal(err)
	}
	return event
}
