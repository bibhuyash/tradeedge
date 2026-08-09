package replay

import (
	"bytes"
	"context"
	accountingengine "github.com/bibhuyash/tradeedge/internal/accounting/engine"
	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	accountingfixture "github.com/bibhuyash/tradeedge/internal/accounting/testfixture"
	"github.com/bibhuyash/tradeedge/internal/accounting/valuation"
	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
	"testing"
	"time"
)

func TestReplayAndCheckpointContinuationAreEquivalent(t *testing.T) {
	at := accountingfixture.BaseTime.Add(time.Second)
	fill, _ := accountingfixture.Fill(991, domain.SideBuy, 10, 100, at, at)
	applied, _ := accountingengine.Apply(nil, fill)
	position := applied.Snapshot
	price, _ := domain.NewPrice(120, "INR")
	quote, _ := marketmodel.NewQuoteEvent(marketmodel.QuoteSpec{InstrumentID: position.Spec().InstrumentID, LastPrice: price, Volume: 1, ExchangeTime: at, IngestedAt: at, Provenance: marketmodel.Provenance{Provider: "replay", ProviderToken: "token", MasterVersion: "v1"}})
	sum, _ := accountingmodel.NewStateChecksum("replay-mark", []byte("v1"))
	mark, _ := valuation.NewMarkPrice(quote, "v1", sum, readiness.StateReady, readiness.ReasonNone)
	frame := Frame{PortfolioID: position.Spec().PortfolioID, Positions: []accountingmodel.PositionSnapshot{position}, Marks: map[accountingmodel.PositionID]valuation.MarkPrice{position.ID(): mark}, LogicalTime: at}
	engine, _ := New(valuation.DefaultPolicy())
	full, err := engine.Replay(context.Background(), 0, []Frame{frame, frame})
	if err != nil {
		t.Fatal(err)
	}
	continued, err := engine.Replay(context.Background(), 1, []Frame{frame})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(full[1].CanonicalJSON(), continued[0].CanonicalJSON()) {
		t.Fatal("checkpoint continuation changed canonical snapshot")
	}
}
