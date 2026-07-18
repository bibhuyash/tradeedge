package model

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
)

func TestCanonicalConfigurationDeterminismAndValidation(t *testing.T) {
	t.Parallel()
	first, err := NewStrategyConfiguration("config/v1", []byte(`{"slow":20,"enabled":true,"fast":5}`))
	if err != nil {
		t.Fatalf("first configuration: %v", err)
	}
	second, err := NewStrategyConfiguration("config/v1", []byte(`{ "fast": 5, "slow": 20, "enabled": true }`))
	if err != nil {
		t.Fatalf("second configuration: %v", err)
	}
	if first.Hash() != second.Hash() ||
		!bytes.Equal(first.CanonicalJSON(), []byte(`{"enabled":true,"fast":5,"slow":20}`)) {
		t.Fatal("equivalent configurations must have identical canonical form and hash")
	}

	invalid := []string{
		`{"period":1.5}`,
		`{"period":1e3}`,
		`{"period":01}`,
		`{"period":-0}`,
		`{"period":1,"period":2}`,
		`[]`,
		`{"period":1} trailing`,
	}
	for _, raw := range invalid {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if _, err := NewStrategyConfiguration("config/v1", []byte(raw)); err == nil {
				t.Fatal("expected invalid canonical JSON to be rejected")
			}
		})
	}
}

func TestIdentityAndInstanceRevisionAreStable(t *testing.T) {
	t.Parallel()
	descriptor := testDescriptor(t, testInstrumentID(t, "NSE|INDEX|NIFTY"))
	configuration, err := NewStrategyConfiguration("config/v1", []byte(`{"fast":5,"slow":20}`))
	if err != nil {
		t.Fatal(err)
	}
	instanceID, _ := domain.NewStrategyID("nifty-crossover-paper")
	first, err := NewStrategyInstance(
		instanceID, descriptor, configuration, 1, LifecycleCandidate,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStrategyInstance(
		instanceID, descriptor, configuration, 1, LifecycleCandidate,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.RevisionID() != second.RevisionID() || first.VersionID() != descriptor.VersionID {
		t.Fatal("identical inputs must produce stable identities")
	}
	next, err := NewStrategyInstance(
		instanceID, descriptor, configuration, 2, LifecycleProbation,
	)
	if err != nil {
		t.Fatal(err)
	}
	if next.RevisionID() == first.RevisionID() {
		t.Fatal("a new generation must produce a new revision identity")
	}
}

func TestLifecycleEvaluationEligibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state     LifecycleState
		evaluates bool
	}{
		{LifecycleCandidate, true},
		{LifecycleProbation, true},
		{LifecycleActive, true},
		{LifecycleSuspended, false},
		{LifecycleRetired, false},
		{LifecycleState("UNKNOWN"), false},
	}
	for _, test := range tests {
		if got := test.state.Evaluates(); got != test.evaluates {
			t.Errorf("%s Evaluates()=%t, want %t", test.state, got, test.evaluates)
		}
	}
}

func TestCandleFrameDeterminismAndImmutability(t *testing.T) {
	t.Parallel()
	instrument := testInstrumentID(t, "NSE|INDEX|NIFTY")
	closeTime := time.Date(2026, 7, 17, 4, 1, 0, 0, time.UTC)
	subscription := InputSubscription{
		Role: "primary", InstrumentID: instrument, Interval: marketmodel.Interval1Minute,
		Required: true, Trigger: true, Lookback: 1, MaximumAge: time.Minute,
	}
	spec, err := NewSubscriptionSpec(SubscriptionExactCloseFrame, []InputSubscription{subscription})
	if err != nil {
		t.Fatal(err)
	}
	candle := testCandle(t, instrument, closeTime.Add(-time.Minute), closeTime, 10000)
	series, err := NewCandleSeries(subscription, []marketmodel.CompletedCandleEvent{candle})
	if err != nil {
		t.Fatal(err)
	}
	trigger, _ := NewTriggerID("dataset|event|primary")
	input := CandleFrameSpec{
		TriggerID: trigger, LogicalTime: closeTime, Subscription: spec,
		Series: []CandleSeries{series}, MasterVersion: "master/v1",
		CalendarVersion: "calendar/v1", DatasetRevision: "dataset/v1",
	}
	first, err := NewCandleFrame(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewCandleFrame(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() != second.ID() {
		t.Fatal("identical frames must have identical IDs")
	}
	copied := first.Series()
	copied[0].Candles = nil
	if len(first.Series()[0].Candles) != 1 {
		t.Fatal("frame series must be immutable through returned copies")
	}

	input.LogicalTime = closeTime.Add(time.Second)
	if _, err := NewCandleFrame(input); !errors.Is(err, ErrInvalidCandleFrame) {
		t.Fatalf("exact-close mismatch: got %v", err)
	}
}

func TestProposalValidationNormalizationAndStableIdentity(t *testing.T) {
	t.Parallel()
	firstInstrument := testInstrumentID(t, "NSE|OPTIONS|NIFTY|CALL")
	secondInstrument := testInstrumentID(t, "NSE|OPTIONS|NIFTY|PUT")
	eventID, _ := marketmodel.NewEventID("source-event")
	price, _ := domain.NewPrice(1250, "INR")
	confidence := int32(6500)
	now := time.Date(2026, 7, 17, 4, 1, 0, 0, time.UTC)
	draft := ProposalDraft{
		SchemaVersion: "proposal/v1",
		Legs: []ProposalLeg{
			{InstrumentID: secondInstrument, Side: domain.SideSell, Ratio: 4, ReferencePrice: price, MaxDeviationBPS: 100},
			{InstrumentID: firstInstrument, Side: domain.SideBuy, Ratio: 2, ReferencePrice: price, MaxDeviationBPS: 100},
		},
		Sizing:    SizingIntent{Kind: SizingStrategyBudgetBPS, ValueBPS: 500},
		ValidFrom: now, ExpiresAt: now.Add(5 * time.Minute),
		RationaleCode: "CROSSOVER_CONFIRMED", Explanation: "fixture evidence only",
		Evidence: []Evidence{{
			Code: "FAST_ABOVE_SLOW", SourceEventIDs: []marketmodel.EventID{eventID},
			Value: 25, Unit: "MINOR_UNITS", Explanation: "fast average exceeded slow average",
		}},
		ConfidenceBPS:       &confidence,
		RiskHints:           []RiskHint{{Code: "DEFINED_RISK", Value: 1, Unit: "BOOLEAN"}},
		ExitPolicyReference: "fixture-crossover-exit/v1",
	}
	validated, err := NewProposalDraft(draft)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Legs[0].Ratio != 1 || validated.Legs[1].Ratio != 2 {
		t.Fatalf("ratios were not normalized: %+v", validated.Legs)
	}
	metadata := testProposalMetadata(t, now, eventID, firstInstrument, secondInstrument)
	first, err := NewTradeProposal(metadata, draft)
	if err != nil {
		t.Fatal(err)
	}
	reversed := draft
	reversed.Legs = []ProposalLeg{draft.Legs[1], draft.Legs[0]}
	second, err := NewTradeProposal(metadata, reversed)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() != second.ID() || first.DeduplicationKey() != first.ID().String() {
		t.Fatal("equivalent advisory proposals must have stable deduplication identity")
	}
}

func TestProposalRejectsUnsafeOrMalformedIntent(t *testing.T) {
	t.Parallel()
	instrument := testInstrumentID(t, "NSE|OPTIONS|NIFTY|CALL")
	eventID, _ := marketmodel.NewEventID("source-event")
	price, _ := domain.NewPrice(1250, "INR")
	now := time.Date(2026, 7, 17, 4, 1, 0, 0, time.UTC)
	base := ProposalDraft{
		SchemaVersion: "proposal/v1",
		Legs: []ProposalLeg{{
			InstrumentID: instrument, Side: domain.SideBuy, Ratio: 1,
			ReferencePrice: price, MaxDeviationBPS: 100,
		}},
		Sizing:    SizingIntent{Kind: SizingStrategyBudgetBPS, ValueBPS: 500},
		ValidFrom: now, ExpiresAt: now.Add(time.Minute),
		RationaleCode: "TEST_REASON", Explanation: "test",
		Evidence: []Evidence{{
			Code: "TEST_EVIDENCE", SourceEventIDs: []marketmodel.EventID{eventID},
			Value: 1, Unit: "COUNT", Explanation: "fixture evidence",
		}},
		ExitPolicyReference: "fixture-exit/v1",
	}
	tests := []struct {
		name   string
		mutate func(*ProposalDraft)
	}{
		{"zero ratio", func(value *ProposalDraft) { value.Legs[0].Ratio = 0 }},
		{"invalid side", func(value *ProposalDraft) { value.Legs[0].Side = "HOLD" }},
		{"oversized sizing", func(value *ProposalDraft) { value.Sizing.ValueBPS = 10001 }},
		{"long expiry", func(value *ProposalDraft) { value.ExpiresAt = value.ValidFrom.Add(16 * time.Minute) }},
		{"missing evidence", func(value *ProposalDraft) { value.Evidence = nil }},
		{"missing exit policy", func(value *ProposalDraft) { value.ExitPolicyReference = "" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			value := base
			value.Legs = append([]ProposalLeg(nil), base.Legs...)
			test.mutate(&value)
			if _, err := NewProposalDraft(value); !errors.Is(err, ErrInvalidTradeProposal) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestEvaluationResultIsExclusiveAndCarriesNextState(t *testing.T) {
	t.Parallel()
	state, err := NewStrategyRuntimeState("state/v1", []byte(`{"evaluations":1}`))
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewNoActionResult(state, NoActionConditionsNotMet, "threshold not crossed")
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind() != ResultNoAction || result.NextState().Hash() != state.Hash() {
		t.Fatal("unexpected no-action result")
	}
	if _, ok := result.Proposal(); ok {
		t.Fatal("no-action result must not also expose a proposal")
	}
}

func TestEvaluationContextRequiresReadyMatchingFrame(t *testing.T) {
	t.Parallel()
	instrument := testInstrumentID(t, "NSE|INDEX|NIFTY")
	descriptor := testDescriptor(t, instrument)
	configuration, _ := NewStrategyConfiguration("config/v1", []byte(`{"fast":5}`))
	state, _ := NewStrategyRuntimeState("state/v1", []byte(`{"count":0}`))
	closeTime := time.Date(2026, 7, 17, 4, 1, 0, 0, time.UTC)
	subscription := descriptor.Subscriptions.Subscriptions()[0]
	candle := testCandle(t, instrument, closeTime.Add(-time.Minute), closeTime, 10000)
	series, _ := NewCandleSeries(subscription, []marketmodel.CompletedCandleEvent{candle})
	trigger, _ := NewTriggerID("trigger")
	frame, err := NewCandleFrame(CandleFrameSpec{
		TriggerID: trigger, LogicalTime: closeTime, Subscription: descriptor.Subscriptions,
		Series: []CandleSeries{series}, MasterVersion: "master/v1",
		CalendarVersion: "calendar/v1", DatasetRevision: "dataset/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	instanceID, _ := domain.NewStrategyID("instance-1")
	revision, _ := NewInstanceRevisionID(instanceID, descriptor.VersionID, configuration.Hash(), 1)
	evaluationID, _ := NewEvaluationID("evaluation")
	input := EvaluationContext{
		DefinitionID: descriptor.Manifest.DefinitionID, VersionID: descriptor.VersionID,
		InstanceID: instanceID, InstanceRevisionID: revision, InstanceGeneration: 1,
		Configuration: configuration, EvaluationID: evaluationID, TriggerID: trigger,
		LogicalTime: closeTime, Frame: frame, PriorState: state,
		Readiness: ReadinessEvidence{
			State: readiness.StateReady, PolicyVersion: "policy/v1",
			CalendarVersion: "calendar/v1", EvaluatedAt: closeTime,
		},
		Entropy: fixedEntropy(7),
	}
	if err := input.Validate(descriptor); err != nil {
		t.Fatalf("valid context: %v", err)
	}
	input.Readiness.State = readiness.StateStale
	if err := input.Validate(descriptor); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("stale readiness should reject evaluation, got %v", err)
	}
}

type fixedEntropy uint64

func (value fixedEntropy) Uint64() uint64 { return uint64(value) }

func testDescriptor(t *testing.T, instrument domain.InstrumentID) Descriptor {
	t.Helper()
	definitionID, _ := NewDefinitionID("moving-average-fixture")
	manifest := VersionManifest{
		DefinitionID: definitionID, ImplementationVersion: "fixture/v1",
		InputContractVersion: "candle-frame/v1", ConfigurationSchemaVersion: "config/v1",
		StateSchemaVersion: "state/v1", ResultSchemaVersion: "result/v1",
		ProposalSchemaVersion: "proposal/v1",
	}
	subscriptions, err := NewSubscriptionSpec(
		SubscriptionSingleStream,
		[]InputSubscription{{
			Role: "primary", InstrumentID: instrument, Interval: marketmodel.Interval1Minute,
			Required: true, Trigger: true, Lookback: 1, MaximumAge: time.Minute,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := NewDescriptor(manifest, subscriptions)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func testInstrumentID(t *testing.T, key string) domain.InstrumentID {
	t.Helper()
	id, err := domain.InstrumentIDFromCanonicalKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func testCandle(
	t *testing.T,
	instrument domain.InstrumentID,
	openTime time.Time,
	closeTime time.Time,
	closeMinor int64,
) marketmodel.CompletedCandleEvent {
	t.Helper()
	open, _ := domain.NewPrice(closeMinor-10, "INR")
	high, _ := domain.NewPrice(closeMinor+20, "INR")
	low, _ := domain.NewPrice(closeMinor-20, "INR")
	closePrice, _ := domain.NewPrice(closeMinor, "INR")
	event, err := marketmodel.NewCompletedCandleEvent(marketmodel.CandleSpec{
		InstrumentID: instrument, Interval: marketmodel.Interval1Minute,
		OpenTime: openTime, CloseTime: closeTime, Open: open, High: high, Low: low,
		Close: closePrice, Volume: 100, EventCount: 10, IngestedAt: closeTime,
		Provenance: marketmodel.Provenance{
			Provider: "FIXTURE", ProviderToken: "redacted-fixture-token",
			MasterVersion: "master/v1", DatasetRevision: "dataset/v1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func testProposalMetadata(
	t *testing.T,
	now time.Time,
	eventID marketmodel.EventID,
	instruments ...domain.InstrumentID,
) ProposalMetadata {
	t.Helper()
	definitionID, _ := NewDefinitionID("moving-average-fixture")
	versionID, _ := NewVersionID(VersionManifest{
		DefinitionID: definitionID, ImplementationVersion: "fixture/v1",
		InputContractVersion: "candle-frame/v1", ConfigurationSchemaVersion: "config/v1",
		StateSchemaVersion: "state/v1", ResultSchemaVersion: "result/v1",
		ProposalSchemaVersion: "proposal/v1",
	})
	instanceID, _ := domain.NewStrategyID("instance-1")
	configuration, _ := NewStrategyConfiguration("config/v1", []byte(`{"fast":5}`))
	revisionID, _ := NewInstanceRevisionID(instanceID, versionID, configuration.Hash(), 1)
	evaluationID, _ := NewEvaluationID("evaluation")
	frameID, _ := NewFrameID("frame")
	return ProposalMetadata{
		DefinitionID: definitionID, VersionID: versionID, InstanceID: instanceID,
		InstanceRevisionID: revisionID, EvaluationID: evaluationID, FrameID: frameID,
		GeneratedAt: now, SourceEventIDs: []marketmodel.EventID{eventID},
		RequiredInstrumentIDs: instruments,
	}
}
