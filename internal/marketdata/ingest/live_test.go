package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	"github.com/bibhuyash/tradeedge/internal/marketdata"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
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
