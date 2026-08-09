package memory

import (
	"context"
	"encoding/json"
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

func TestFinancialPublicationRestoreAndLastComplete(t *testing.T) {
	at := accountingfixture.BaseTime.Add(time.Second)
	fill, _ := accountingfixture.Fill(880, domain.SideBuy, 10, 100, at, at)
	result, _ := accountingengine.Apply(nil, fill)
	position := result.Snapshot
	first := publication(t, position, 1, accountingmodel.StateChecksum{}, at, readiness.StateReady)
	store := NewFinancialStore(10)
	receipt, err := store.Publish(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	second := publication(t, position, 2, receipt.CheckpointChecksum, at.Add(time.Minute), readiness.StateSessionClosed)
	if _, err = store.Publish(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	current, _, _ := store.Current(context.Background(), position.Spec().PortfolioID)
	complete, err := store.LastComplete(context.Background(), position.Spec().PortfolioID)
	if err != nil || current.Status != valuation.StatusStale || complete.Status != valuation.StatusComplete || complete.Revision != 1 {
		t.Fatalf("current=%s complete=%s/%d err=%v", current.Status, complete.Status, complete.Revision, err)
	}
	restored := NewFinancialStore(10)
	if err = restored.Restore(context.Background(), []valuation.Publication{first, second}); err != nil {
		t.Fatal(err)
	}
	restoredCurrent, _, _ := restored.Current(context.Background(), position.Spec().PortfolioID)
	if restoredCurrent.Checksum != current.Checksum {
		t.Fatal("restoration changed current snapshot")
	}
}
func publication(t *testing.T, position accountingmodel.PositionSnapshot, revision uint64, parent accountingmodel.StateChecksum, at time.Time, state readiness.State) valuation.Publication {
	t.Helper()
	price, _ := domain.NewPrice(120, "INR")
	quote, _ := marketmodel.NewQuoteEvent(marketmodel.QuoteSpec{InstrumentID: position.Spec().InstrumentID, LastPrice: price, Volume: 1, ExchangeTime: at, IngestedAt: at, Provenance: marketmodel.Provenance{Provider: "memory-test", ProviderToken: "token", MasterVersion: "v1"}})
	markSum, _ := accountingmodel.NewStateChecksum("mark", []byte(at.String()))
	mark, _ := valuation.NewMarkPrice(quote, "v1", markSum, state, readiness.ReasonNone)
	value, err := valuation.EvaluatePosition(position, &mark, at, valuation.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := valuation.Aggregate(position.Spec().PortfolioID, revision, []valuation.PositionValuation{value}, at)
	if err != nil {
		t.Fatal(err)
	}
	positionManifest, _ := accountingmodel.NewStateChecksum("positions", []byte(position.Checksum().String()))
	markManifest, _ := accountingmodel.NewStateChecksum("marks", []byte(markSum.String()))
	raw, _ := json.Marshal(struct{ Parent, Snapshot, Positions, Marks string }{parent.String(), snapshot.Checksum.String(), positionManifest.String(), markManifest.String()})
	checkpoint, _ := accountingmodel.NewStateChecksum("portfolio-financial-checkpoint/v1", raw)
	return valuation.Publication{ExpectedRevision: revision - 1, ExpectedCheckpoint: parent, PositionManifest: positionManifest, MarkManifest: markManifest, Valuations: []valuation.PositionValuation{value}, Snapshot: snapshot, CheckpointChecksum: checkpoint}
}
