package derivatives

import (
	"errors"
	"strings"
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
	underlying := strings.ToLower(string(input.Future.Instrument.UnderlyingID()))
	if underlying != "nifty" && underlying != "banknifty" || input.Option.Instrument.UnderlyingID() != input.Future.Instrument.UnderlyingID() {
		return strategymodel.TradeProposal{}, ErrInvalidPolicy
	}
	definitionID, _ := strategymodel.NewDefinitionID("ema-reference-v1")
	manifest := strategymodel.VersionManifest{DefinitionID: definitionID, ImplementationVersion: "ema-reference-v1", InputContractVersion: ProposalBridgeVersion, ConfigurationSchemaVersion: "ema-reference-config/v1", StateSchemaVersion: "ema-reference-state/v1", ResultSchemaVersion: "strategy-result/v1", ProposalSchemaVersion: "proposal/v1"}
	versionID, err := strategymodel.NewVersionID(manifest)
	if err != nil {
		return strategymodel.TradeProposal{}, err
	}
	configuration, err := strategymodel.NewStrategyConfiguration("ema-reference-config/v1", []byte(`{"phase8_m4":"live-read-only-shadow"}`))
	if err != nil {
		return strategymodel.TradeProposal{}, err
	}
	// Qualification state is partitioned by underlying. The Phase 3 portfolio
	// allocation deliberately remains one bounded reference-candidate budget.
	instanceID, _ := domain.NewStrategyID("ema-reference-v1")
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
		Explanation: "EMA reference direction with futures context and selected-option qualification authority",
		Evidence: []strategymodel.Evidence{
			{Code: strings.ToUpper(underlying) + "_SPOT", SourceEventIDs: []marketmodel.EventID{input.SignalEventID}, Value: input.Spot.MinorUnits(), Unit: "INR_MINOR", Explanation: "signal authority only"},
			{Code: strings.ToUpper(underlying) + "_FUTURE", SourceEventIDs: []marketmodel.EventID{input.SignalEventID}, Value: input.FuturePrice.MinorUnits(), Unit: "INR_MINOR", Explanation: "forward selection context only"},
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
