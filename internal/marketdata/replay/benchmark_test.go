package replay

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/storage"
)

func BenchmarkReplay1000Events(b *testing.B) {
	base := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	repository := storage.NewMemoryRepository()
	writer, _ := repository.Create(context.Background(), storage.DraftManifest{
		MasterVersion: "benchmark", CalendarVersion: "calendar-v1",
		Source: "memory", OrderingVersion: "v1", CreatedAt: base,
	})
	for index := 0; index < 1000; index++ {
		id, _ := domain.InstrumentIDFromCanonicalKey(fmt.Sprintf("instrument-%03d", index%250))
		price, _ := domain.NewPrice(int64(10000+index), "INR")
		event, _ := model.NewQuoteEvent(model.QuoteSpec{
			InstrumentID: id, LastPrice: price,
			ExchangeTime: base.Add(time.Duration(index) * time.Millisecond),
			IngestedAt:   base.Add(time.Duration(index)*time.Millisecond + time.Microsecond),
			Provenance: model.Provenance{
				Provider: "benchmark", ProviderToken: fmt.Sprintf("%d", index%250),
				MasterVersion: "benchmark", SourceSequence: uint64(index), HasSequence: true,
			},
		})
		_ = writer.Append(context.Background(), event)
	}
	manifest, _ := writer.Commit(context.Background())
	reader, _ := repository.Open(context.Background(), manifest.ID)
	b.ResetTimer()
	for range b.N {
		clock := NewManualClock(base)
		engine := NewEngine(clock, nil)
		if err := engine.Replay(context.Background(), reader, Request{Rate: MaximumRate()},
			func(context.Context, model.Event) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}
