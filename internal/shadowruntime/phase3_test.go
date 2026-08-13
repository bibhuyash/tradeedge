package shadowruntime

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/derivatives"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	portfolioconfig "github.com/bibhuyash/tradeedge/internal/portfolio/config"
	riskconfig "github.com/bibhuyash/tradeedge/internal/risk/config"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	"github.com/bibhuyash/tradeedge/internal/risk/rules"
)

func TestPhase3GatewayUsesReleasedRulesAndReturnsBeforeMutation(t *testing.T) {
	at := time.Date(2026, 8, 11, 4, 30, 0, 0, time.UTC)
	master, future, option := phase3Master(t, at)
	portfolioRaw, err := os.ReadFile("../../configs/validation/portfolio.paper.json")
	if err != nil {
		t.Fatal(err)
	}
	portfolio, err := portfolioconfig.Decode(portfolioRaw)
	if err != nil {
		t.Fatal(err)
	}
	descriptors := map[riskmodel.RiskRuleID]riskmodel.RiskRuleDescriptor{}
	for _, rule := range rules.ProductionCatalog() {
		descriptors[rule.Descriptor().ID] = rule.Descriptor()
	}
	riskRaw, err := os.ReadFile("../../configs/validation/risk.paper.json")
	if err != nil {
		t.Fatal(err)
	}
	riskConfiguration, err := riskconfig.Decode(riskRaw, descriptors, portfolio.AllocationPolicy().Limits.ExposureGroups)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := NewPhase3Gateway(context.Background(), Phase3Config{Master: master, PortfolioConfiguration: portfolio, RiskConfiguration: riskConfiguration, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Shutdown(context.Background())
	spotPrice, _ := domain.NewPrice(2_481_200, "INR")
	futurePrice, _ := domain.NewPrice(2_481_200, "INR")
	optionPrice, _ := domain.NewPrice(10_100, "INR")
	eventID, _ := marketmodel.NewEventID("phase8-m4-released-risk")
	futureMapping, _ := master.ResolveInstrument("zerodha", future.ID(), at)
	optionMapping, _ := master.ResolveInstrument("zerodha", option.ID(), at)
	futureContract := derivatives.Contract{Instrument: future, Mapping: futureMapping}
	optionContract := derivatives.Contract{Instrument: option, Mapping: optionMapping}
	proposal, err := derivatives.NewOptionProposal(derivatives.ProposalInput{SignalID: "phase8-m4-signal", SignalEventID: eventID, At: at, Spot: spotPrice, Future: futureContract, FuturePrice: futurePrice, Option: optionContract, OptionPrice: optionPrice, Side: domain.SideBuy, FastEMAScaled: 2_482_000_000_000, SlowEMAScaled: 2_480_000_000_000, QuantityLots: 1, SizingBPS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := gateway.Evaluate(context.Background(), derivatives.ConnectedRequest{Mode: derivatives.ConnectedShadow, Proposal: proposal, MasterVersion: master.Version(), At: at, Session: "NORMAL_TRADING", Selection: derivatives.Selection{Future: futureContract, Option: optionContract, Universe: []derivatives.Contract{optionContract}, ReferencePrice: futurePrice}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome() != riskmodel.DecisionApproved || decision.ID().IsZero() {
		t.Fatalf("released Phase 3 result = %#v", decision.Spec())
	}
	// The gateway exposes only a risk decision. It has no execution, OMS,
	// broker, fill, position, or accounting result to mutate.
}

func phase3Master(t *testing.T, at time.Time) (instrumentmaster.Master, domain.Instrument, domain.Instrument) {
	t.Helper()
	underlying, _ := domain.NewUnderlyingID("NIFTY")
	futureExpiry, _ := domain.NewCivilDate(2026, 8, 25)
	optionExpiry, _ := domain.NewCivilDate(2026, 8, 18)
	lot, _ := domain.NewQuantity(65)
	tick, _ := domain.NewPrice(5, "INR")
	strike, _ := domain.NewPrice(2_480_000, "INR")
	future, err := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentFutures, UnderlyingID: underlying, Type: domain.InstrumentFuture, ExchangeSymbol: "NIFTY26AUGFUT", Derivative: &domain.DerivativeSpec{Expiry: futureExpiry, OptionType: domain.OptionNone}, LotSize: lot, TickSize: tick, Currency: "INR"})
	if err != nil {
		t.Fatal(err)
	}
	option, err := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentOptions, UnderlyingID: underlying, Type: domain.InstrumentOption, ExchangeSymbol: "NIFTY2681824800CE", Derivative: &domain.DerivativeSpec{Expiry: optionExpiry, Strike: strike, OptionType: domain.OptionCall}, LotSize: lot, TickSize: tick, Currency: "INR"})
	if err != nil {
		t.Fatal(err)
	}
	mappings := []domain.ProviderInstrumentRef{{Provider: "zerodha", Token: "1", TradingSymbol: future.Symbol(), InstrumentID: future.ID(), ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour)}, {Provider: "zerodha", Token: "2", TradingSymbol: option.Symbol(), InstrumentID: option.ID(), ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour)}}
	master, err := instrumentmaster.New(at.Add(-time.Hour), []domain.Instrument{future, option}, mappings)
	if err != nil {
		t.Fatal(err)
	}
	return master, future, option
}
