package derivatives

import (
	"errors"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

const ProposalBridgeVersion = "phase8-m2-option-proposal-bridge/v1"

type ProposalInput struct {
	SignalID       string
	SignalEventID  marketmodel.EventID
	At             time.Time
	Spot           domain.Price
	Future         Contract
	FuturePrice    domain.Price
	Option         Contract
	OptionPrice    domain.Price
	Side           domain.Side
	FastEMAScaled  int64
	SlowEMAScaled  int64
	QuantityLots   uint32
	SizingBPS      int32
	ExistingOption domain.InstrumentID
}

// NewOptionProposal is the provider-neutral bridge between deterministic
// derivatives selection and the released strategy/portfolio/risk contracts.
// The proposal reference price is always the selected option's own accepted
// quote. Provider tokens never enter the proposal.
func NewOptionProposal(input ProposalInput) (strategymodel.TradeProposal, error) {
	if input.SignalID == "" || input.SignalEventID.IsZero() || input.At.IsZero() ||
		input.Spot.MinorUnits() <= 0 || input.Future.Instrument.Type() != domain.InstrumentFuture ||
		input.Option.Instrument.Type() != domain.InstrumentOption || input.FuturePrice.MinorUnits() <= 0 ||
		input.OptionPrice.MinorUnits() <= 0 || input.QuantityLots != 1 || input.SizingBPS <= 0 ||
		input.SizingBPS > 1000 || (input.Side != domain.SideBuy && input.Side != domain.SideSell) {
		return strategymodel.TradeProposal{}, ErrInvalidPolicy
	}
	if input.Side == domain.SideSell && (input.ExistingOption.IsZero() || input.ExistingOption != input.Option.Instrument.ID()) {
		return strategymodel.TradeProposal{}, errors.New("exit must retain authoritative open option identity")
	}
	definitionID, _ := strategymodel.NewDefinitionID("nifty-ema-crossover-paper")
	manifest := strategymodel.VersionManifest{DefinitionID: definitionID, ImplementationVersion: "nifty-ema-crossover/v1", InputContractVersion: ProposalBridgeVersion, ConfigurationSchemaVersion: "nifty-ema-crossover-config/v1", StateSchemaVersion: "nifty-ema-crossover-state/v1", ResultSchemaVersion: "strategy-result/v1", ProposalSchemaVersion: "proposal/v1"}
	versionID, err := strategymodel.NewVersionID(manifest)
	if err != nil {
		return strategymodel.TradeProposal{}, err
	}
	configuration, err := strategymodel.NewStrategyConfiguration("nifty-ema-crossover-config/v1", []byte(`{"phase8_m2":"connected-option"}`))
	if err != nil {
		return strategymodel.TradeProposal{}, err
	}
	instanceID, _ := domain.NewStrategyID("nifty-ema-crossover-paper")
	instanceRevision, err := strategymodel.NewInstanceRevisionID(instanceID, versionID, configuration.Hash(), 1)
	if err != nil {
		return strategymodel.TradeProposal{}, err
	}
	evaluationID, _ := strategymodel.NewEvaluationID(ProposalBridgeVersion + "|" + input.SignalID)
	frameID, _ := strategymodel.NewFrameID(ProposalBridgeVersion + "|" + input.SignalID)
	rationale := "EMA_BULLISH_CROSSOVER_LONG_CALL"
	if input.Side == domain.SideSell {
		rationale = "EMA_BEARISH_EXIT_OPEN_CALL"
	}
	draft, err := strategymodel.NewProposalDraft(strategymodel.ProposalDraft{
		SchemaVersion: "proposal/v1",
		Legs:          []strategymodel.ProposalLeg{{InstrumentID: input.Option.Instrument.ID(), Side: input.Side, Ratio: input.QuantityLots, ReferencePrice: input.OptionPrice, MaxDeviationBPS: 500}},
		Sizing:        strategymodel.SizingIntent{Kind: strategymodel.SizingStrategyBudgetBPS, ValueBPS: input.SizingBPS},
		ValidFrom:     input.At, ExpiresAt: input.At.Add(5 * time.Second), RationaleCode: rationale,
		Explanation: "EMA reference direction with NIFTY future context and selected-option execution authority",
		Evidence: []strategymodel.Evidence{
			{Code: "NIFTY_SPOT", SourceEventIDs: []marketmodel.EventID{input.SignalEventID}, Value: input.Spot.MinorUnits(), Unit: "INR_MINOR", Explanation: "signal authority only"},
			{Code: "NIFTY_FUTURE", SourceEventIDs: []marketmodel.EventID{input.SignalEventID}, Value: input.FuturePrice.MinorUnits(), Unit: "INR_MINOR", Explanation: "forward selection context only"},
			{Code: "OPTION_MARKET", SourceEventIDs: []marketmodel.EventID{input.SignalEventID}, Value: input.OptionPrice.MinorUnits(), Unit: "INR_MINOR", Explanation: "sole execution and valuation authority"},
			{Code: "FAST_EMA", SourceEventIDs: []marketmodel.EventID{input.SignalEventID}, Value: input.FastEMAScaled, Unit: "PRICE_MINOR_UNITS_X1E6", Explanation: "reference signal"},
			{Code: "SLOW_EMA", SourceEventIDs: []marketmodel.EventID{input.SignalEventID}, Value: input.SlowEMAScaled, Unit: "PRICE_MINOR_UNITS_X1E6", Explanation: "reference signal"},
		},
		RiskHints: []strategymodel.RiskHint{{Code: "QUANTITY_LOTS", Value: 1, Unit: "LOTS"}}, ExitPolicyReference: "ema-bearish-crossover-and-eod/v1",
	})
	if err != nil {
		return strategymodel.TradeProposal{}, err
	}
	return strategymodel.NewTradeProposal(strategymodel.ProposalMetadata{DefinitionID: definitionID, VersionID: versionID, InstanceID: instanceID, InstanceRevisionID: instanceRevision, EvaluationID: evaluationID, FrameID: frameID, GeneratedAt: input.At, SourceEventIDs: []marketmodel.EventID{input.SignalEventID}, RequiredInstrumentIDs: []domain.InstrumentID{input.Option.Instrument.ID()}}, draft)
}
