package derivatives

import (
	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"testing"
)

func TestOptionProposalUsesOnlyOptionReferencePrice(t *testing.T) {
	master, future, option := fixtureMaster(t)
	selection, err := Resolve(master, testAt, price(t, 24_812_00), domain.OptionCall, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	eventID, _ := marketmodel.NewEventID("connected-entry")
	proposal, err := NewOptionProposal(ProposalInput{SignalID: "entry", SignalEventID: eventID, At: testAt, Spot: price(t, 24_790_00), Future: selection.Future, FuturePrice: price(t, 24_812_00), Option: selection.Option, OptionPrice: price(t, 10_100), Side: domain.SideBuy, FastEMAScaled: 24800000000000, SlowEMAScaled: 24780000000000, QuantityLots: 1, SizingBPS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	leg := proposal.Draft().Legs[0]
	if leg.InstrumentID != option.ID() || leg.ReferencePrice.MinorUnits() != 10_100 || leg.ReferencePrice.MinorUnits() == 24_790_00 || leg.ReferencePrice.MinorUnits() == 24_812_00 {
		t.Fatalf("authority crossed: %#v", leg)
	}
	exitInput := ProposalInput{SignalID: "exit", SignalEventID: eventID, At: testAt, Spot: price(t, 24_700_00), Future: selection.Future, FuturePrice: price(t, 24_720_00), Option: selection.Option, OptionPrice: price(t, 10_400), Side: domain.SideSell, FastEMAScaled: 24700000000000, SlowEMAScaled: 24720000000000, QuantityLots: 1, SizingBPS: 1000}
	if _, err = NewOptionProposal(exitInput); err == nil {
		t.Fatal("exit without open identity passed")
	}
	exitInput.ExistingOption = option.ID()
	if _, err = NewOptionProposal(exitInput); err != nil {
		t.Fatal(err)
	}
	_ = future
}
