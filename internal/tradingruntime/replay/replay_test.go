package replay

import (
	"context"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/tradingruntime"
)

type deterministicRuntime struct{}

func (deterministicRuntime) Process(_ context.Context, event marketmodel.Event) (tradingruntime.EventReceipt, error) {
	return tradingruntime.EventReceipt{EventID: event.ID().String(), Outcome: tradingruntime.OutcomeCompleted}, nil
}
func (deterministicRuntime) Snapshot() tradingruntime.RuntimeSnapshot {
	return tradingruntime.RuntimeSnapshot{Mode: tradingruntime.ModePaper, State: tradingruntime.RuntimeStopped, Capacity: 4, Restored: true}
}

func TestIdenticalReplayInputsProduceIdenticalEvidence(t *testing.T) {
	at := time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC)
	instrument, _ := domain.InstrumentIDFromCanonicalKey("replay")
	price, _ := domain.NewPrice(100, "INR")
	event, err := marketmodel.NewQuoteEvent(marketmodel.QuoteSpec{InstrumentID: instrument, LastPrice: price, Volume: 1, ExchangeTime: at, IngestedAt: at, Provenance: marketmodel.Provenance{Provider: "fixture", ProviderToken: "token", MasterVersion: "v1"}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := Run(context.Background(), deterministicRuntime{}, []marketmodel.Event{event})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(context.Background(), deterministicRuntime{}, []marketmodel.Event{event})
	if err != nil {
		t.Fatal(err)
	}
	if first.Checksum == "" || first.Checksum != second.Checksum {
		t.Fatalf("checksums differ: %s %s", first.Checksum, second.Checksum)
	}
}
