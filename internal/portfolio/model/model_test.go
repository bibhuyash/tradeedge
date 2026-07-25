package model

import (
	"bytes"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

func TestIdentityFramingAndArithmetic(t *testing.T) {
	left, _ := NewPortfolioID("a", "bc")
	right, _ := NewPortfolioID("ab", "c")
	repeated, _ := NewPortfolioID("a", "bc")
	if left == right || left != repeated {
		t.Fatal("framed identity derivation is ambiguous or unstable")
	}
	maximum, _ := domain.NewMoney(math.MaxInt64, "INR")
	if _, err := CheckedMoneyMultiply(maximum, 2); !errors.Is(err, ErrArithmeticOverflow) {
		t.Fatalf("multiply error = %v", err)
	}
	minimum, _ := domain.NewMoney(math.MinInt64, "INR")
	zero, _ := domain.NewMoney(0, "INR")
	if _, err := CheckedMoneySubtract(zero, minimum); !errors.Is(err, ErrArithmeticOverflow) {
		t.Fatalf("subtract error = %v", err)
	}
	ratio, err := NewRational(10, 20)
	if err != nil || ratio.Numerator() != 1 || ratio.Denominator() != 2 {
		t.Fatalf("ratio = %#v, %v", ratio, err)
	}
}

func TestCapitalAccountingProperty(t *testing.T) {
	for available := int64(0); available <= 100; available += 10 {
		for reserved := int64(0); reserved <= 50; reserved += 10 {
			for deployed := int64(0); deployed <= 50; deployed += 10 {
				total := available + reserved + deployed
				value, err := NewCapitalState(money(t, total), money(t, available),
					money(t, reserved), money(t, deployed))
				if err != nil || value.Total.MinorUnits() != total {
					t.Fatalf("valid partition %d/%d/%d rejected: %v",
						available, reserved, deployed, err)
				}
				if _, err := NewCapitalState(money(t, total+1), money(t, available),
					money(t, reserved), money(t, deployed)); !errors.Is(err, ErrInvalidPortfolioSnapshot) {
					t.Fatalf("invalid partition %d/%d/%d accepted", available, reserved, deployed)
				}
			}
		}
	}
}

func TestExposureProjectionPreservesUnknown(t *testing.T) {
	current := exposureFixture(t, 100, 50, LossBoundKnown)
	incremental := exposureFixture(t, 20, 10, LossBoundUnknown)
	projected, err := ProjectExposure(current, incremental)
	if err != nil {
		t.Fatal(err)
	}
	gross, known := projected.Gross().Value()
	if !known || gross.MinorUnits() != 120 {
		t.Fatalf("gross = %v, %t", gross, known)
	}
	if projected.LossBound() != LossBoundUnknown {
		t.Fatalf("loss bound = %s", projected.LossBound())
	}
	if _, known := projected.MaximumLoss().Value(); known {
		t.Fatal("unknown maximum loss silently became known")
	}
}

func TestPortfolioSnapshotDeterminismAccountingAndDefensiveCopies(t *testing.T) {
	spec := snapshotFixture(t)
	first, err := NewPortfolioSnapshot(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Exposures = []ExposureRecord{spec.Exposures[1], spec.Exposures[0]}
	second, err := NewPortfolioSnapshot(spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() != second.ID() || !bytes.Equal(first.CanonicalJSON(), second.CanonicalJSON()) {
		t.Fatal("snapshot identity changed with input ordering")
	}
	changedSpec := snapshotFixture(t)
	changedSpec.RealizedPnL = money(t, 21)
	changedSpec.CurrentEquity = money(t, 951)
	changed, err := NewPortfolioSnapshot(changedSpec)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ID() == first.ID() {
		t.Fatal("changed authoritative snapshot content retained its identity")
	}
	returned := first.Exposures()
	returned[0] = ExposureRecord{}
	if first.Exposures()[0].Subject() == "" {
		t.Fatal("returned exposure mutated snapshot")
	}
	raw := first.CanonicalJSON()
	raw[0] = 'x'
	if first.CanonicalJSON()[0] == 'x' {
		t.Fatal("returned canonical bytes mutated snapshot")
	}
	invalid := snapshotFixture(t)
	invalid.Capital.Available = money(t, 799)
	if _, err := NewPortfolioSnapshot(invalid); !errors.Is(err, ErrInvalidPortfolioSnapshot) {
		t.Fatalf("accounting error = %v", err)
	}
	if first.Drawdown().Amount.MinorUnits() != 50 {
		t.Fatalf("drawdown = %v", first.Drawdown().Amount)
	}
}

func TestAllocationCandidateDeterminismAndNonReservationOutcomes(t *testing.T) {
	snapshot, _ := NewPortfolioSnapshot(snapshotFixture(t))
	proposal := proposalFixture(t)
	allocation := snapshot.StrategyAllocations()[0]
	spec := candidateFixture(t, proposal, snapshot, allocation)
	first, err := NewAllocationCandidate(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Reasons = []AllocationReason{ReasonInstrumentLimitExceeded, ReasonInsufficientAvailableCapital}
	second, err := NewAllocationCandidate(spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() != second.ID() {
		t.Fatal("candidate reason ordering changed identity")
	}
	if _, err := NewAllocationAssessment(AllocationAssessment{
		Outcome: AllocationRejected, Candidate: &first,
		Reasons:     []AllocationReason{ReasonInsufficientAvailableCapital},
		Explanation: "no capital reservation is created",
	}); !errors.Is(err, ErrInvalidAllocationCandidate) {
		t.Fatalf("rejected assessment accepted candidate: %v", err)
	}
	if _, err := NewAllocationAssessment(AllocationAssessment{
		Outcome:     AllocationDeferred,
		Reasons:     []AllocationReason{ReasonStalePortfolioSnapshot},
		Explanation: "requires a fresh immutable snapshot",
	}); err != nil {
		t.Fatal(err)
	}
	changed := candidateFixture(t, proposal, snapshot, allocation)
	changed.Rounding.RemainderMinor++
	if _, err := NewAllocationCandidate(changed); !errors.Is(err, ErrInvalidAllocationCandidate) {
		t.Fatalf("invalid rounding error = %v", err)
	}
}

func TestKillSwitchAndCircuitBreakerValidation(t *testing.T) {
	configID, _ := NewPortfolioConfigurationID("config")
	hash, _ := NewConfigurationHash([]byte(`{"enabled":true}`))
	state, _ := NewStateChecksum([]byte(`{"state":"stop"}`))
	killID, _ := NewKillSwitchID("global")
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	kill, err := NewKillSwitch(KillSwitchSpec{
		ID: killID, Scope: ScopeGlobal, ScopeSubject: "GLOBAL", State: KillSwitchActive,
		ReasonCode: "OPERATOR_STOP", ActivationEvidence: state, ActivatedAt: now,
		ConfigurationID: configID, ConfigurationHash: hash, StateRevision: 1,
		SchemaVersion: "kill-switch/v1",
	})
	if err != nil || !kill.Blocks() {
		t.Fatalf("kill switch = %#v, %v", kill, err)
	}
	breakerID, _ := NewCircuitBreakerID("portfolio")
	breaker, err := NewCircuitBreaker(CircuitBreakerSpec{
		ID: breakerID, Scope: ScopePortfolio, ScopeSubject: "PRIMARY",
		State: CircuitBreakerOpen, ReasonCode: "FAILURE_THRESHOLD", Evidence: state,
		ChangedAt: now, ConfigurationID: configID, ConfigurationHash: hash,
		StateRevision: 1, SchemaVersion: "circuit-breaker/v1",
	})
	if err != nil || !breaker.Blocks() {
		t.Fatalf("circuit breaker = %#v, %v", breaker, err)
	}
}

func FuzzPortfolioSnapshotValidation(f *testing.F) {
	f.Add(int64(800), int64(50), int64(150))
	f.Add(int64(-1), int64(0), int64(1001))
	f.Fuzz(func(t *testing.T, available, reserved, deployed int64) {
		spec := snapshotFixture(t)
		spec.Capital.Available = money(t, available)
		spec.Capital.Reserved = money(t, reserved)
		spec.Capital.Deployed = money(t, deployed)
		value, err := NewPortfolioSnapshot(spec)
		if err == nil {
			repeated, repeatErr := NewPortfolioSnapshot(value.Spec())
			if repeatErr != nil || repeated.ID() != value.ID() ||
				!bytes.Equal(repeated.CanonicalJSON(), value.CanonicalJSON()) {
				t.Fatalf("valid snapshot was not stable: %v", repeatErr)
			}
		}
	})
}

func FuzzExposureArithmetic(f *testing.F) {
	f.Add(int64(100), int64(50))
	f.Add(int64(math.MaxInt64), int64(1))
	f.Fuzz(func(t *testing.T, left, right int64) {
		if left < 0 || right < 0 {
			return
		}
		current := exposureFixture(t, left, 0, LossBoundKnown)
		incremental := exposureFixture(t, right, 0, LossBoundKnown)
		projected, err := ProjectExposure(current, incremental)
		if err == nil {
			value, known := projected.Gross().Value()
			if !known || value.MinorUnits() < left || value.MinorUnits() < right {
				t.Fatal("projection wrapped or lost known state")
			}
		}
	})
}

func FuzzAllocationCandidateValidation(f *testing.F) {
	f.Add(int64(100), int64(60))
	f.Add(int64(-1), int64(0))
	f.Fuzz(func(t *testing.T, capital, validitySeconds int64) {
		snapshot, err := NewPortfolioSnapshot(snapshotFixture(t))
		if err != nil {
			t.Fatal(err)
		}
		proposal := proposalFixture(t)
		spec := candidateFixture(t, proposal, snapshot, snapshot.StrategyAllocations()[0])
		spec.CandidateCapital = money(t, capital)
		spec.ExpiresAt = spec.ValidFrom.Add(time.Duration(validitySeconds) * time.Second)
		value, candidateErr := NewAllocationCandidate(spec)
		if candidateErr == nil {
			repeated, repeatErr := NewAllocationCandidate(value.Spec())
			if repeatErr != nil || repeated.ID() != value.ID() {
				t.Fatalf("valid candidate was not stable: %v", repeatErr)
			}
		}
	})
}

func snapshotFixture(t *testing.T) PortfolioSnapshotSpec {
	t.Helper()
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	portfolioID, _ := NewPortfolioID("primary")
	configID, _ := NewPortfolioConfigurationID("config")
	hash, _ := NewConfigurationHash([]byte(`{"version":1}`))
	source, _ := NewStateChecksum([]byte(`{"source":"fixture"}`))
	policyID, _ := NewAllocationPolicyID("policy")
	allocationID, _ := NewStrategyAllocationID("strategy")
	definitionID, _ := strategymodel.NewDefinitionID("fixture")
	manifest := strategymodel.VersionManifest{
		DefinitionID: definitionID, ImplementationVersion: "1",
		InputContractVersion: "1", ConfigurationSchemaVersion: "1",
		StateSchemaVersion: "1", ResultSchemaVersion: "1", ProposalSchemaVersion: "1",
	}
	versionID, _ := strategymodel.NewVersionID(manifest)
	strategyConfig, _ := strategymodel.NewStrategyConfiguration("1", []byte(`{"fixture":1}`))
	instanceRevision, _ := strategymodel.NewInstanceRevisionID(
		domain.StrategyID("instance"), versionID, strategyConfig.Hash(), 1)
	allocation, err := NewStrategyAllocation(StrategyAllocationSpec{
		ID: allocationID, DefinitionID: definitionID, VersionID: versionID,
		InstanceID: "instance", InstanceRevisionID: instanceRevision,
		PolicyID: policyID, PolicyVersion: 1, Limit: money(t, 500),
		Deployed: money(t, 100), Reserved: money(t, 50), Remaining: money(t, 350),
		DailyLoss: money(t, -10), State: StrategyAllocationEnabled,
		EffectiveFrom: now.Add(-time.Hour), EffectiveUntil: now.Add(time.Hour),
		ConfigurationHash: hash, SchemaVersion: "strategy-allocation/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	capital, _ := NewCapitalState(money(t, 1000), money(t, 800), money(t, 50), money(t, 150))
	date, _ := domain.NewCivilDate(2026, 7, 18)
	return PortfolioSnapshotSpec{
		SchemaVersion: "portfolio-snapshot/v1", PortfolioID: portfolioID, Revision: 1,
		AsOfExchangeTime: now.Add(-time.Second), GeneratedAt: now, TradingDate: date,
		BaseCurrency: "INR", State: PortfolioEnabled, ConfigurationID: configID,
		ConfigurationVersion: 1, ConfigurationHash: hash, Capital: capital,
		RealizedPnL: money(t, 20), UnrealizedPnL: money(t, -70),
		DailyRealizedPnL: money(t, 5), DailyUnrealizedPnL: money(t, -15),
		WeeklyRealizedPnL: money(t, -25), HighWaterMark: money(t, 1000),
		CurrentEquity: money(t, 950), StrategyAllocations: []StrategyAllocation{allocation},
		Exposures: []ExposureRecord{
			exposureFixtureSubject(t, "B", 100, 50, LossBoundKnown),
			exposureFixtureSubject(t, "A", 200, -25, LossBoundKnown),
		}, SourceStateChecksum: source,
	}
}

func exposureFixture(t *testing.T, gross, net int64, bound LossBoundState) ExposureRecord {
	return exposureFixtureSubject(t, "NIFTY", gross, net, bound)
}

func exposureFixtureSubject(t *testing.T, subject string, gross, net int64, bound LossBoundState) ExposureRecord {
	t.Helper()
	known := func(value int64) MeasuredMoney {
		result, _ := NewKnownMoney(money(t, value))
		return result
	}
	unknown, _ := NewUnavailableMoney(AvailabilityUnknown)
	maxLoss := known(gross)
	if bound != LossBoundKnown {
		maxLoss = unknown
	}
	value, err := NewExposureRecord(ExposureRecordSpec{
		Dimension: ExposureInstrument, Subject: subject, Gross: known(gross),
		NetDirectional: known(net), PremiumAtRisk: known(gross / 2),
		Long: known(gross), Short: known(0), PremiumPaid: known(gross / 2),
		PremiumReceived: known(0), MaximumLoss: maxLoss, LossBound: bound,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func candidateFixture(t *testing.T, proposal strategymodel.TradeProposal,
	snapshot PortfolioSnapshot, allocation StrategyAllocation) AllocationCandidateSpec {
	t.Helper()
	now := proposal.Metadata().GeneratedAt
	instrument := proposal.Draft().Legs[0].InstrumentID
	ratio, _ := NewContractRatio(1)
	quantity, _ := domain.NewQuantity(50)
	known, _ := NewKnownMoney(money(t, 100))
	unknown, _ := NewUnavailableMoney(AvailabilityUnknown)
	return AllocationCandidateSpec{
		SchemaVersion: "allocation-candidate/v1", Proposal: proposal,
		PortfolioID: snapshot.PortfolioID(), PortfolioSnapshotID: snapshot.ID(),
		PortfolioRevision: snapshot.Revision(), StrategyAllocationID: allocation.ID(),
		PolicyID: allocation.Spec().PolicyID, PolicyVersion: allocation.Spec().PolicyVersion,
		RequestedSizing: proposal.Draft().Sizing, CandidateCapital: money(t, 100),
		CandidatePremium: known, CandidateRiskBudget: unknown,
		LegBounds: []AllocationLegBound{{
			InstrumentID: instrument, Side: domain.SideBuy, Ratio: ratio,
			Resolution: QuantityResolved, MaximumUnits: quantity, LotSize: quantity,
		}},
		IncrementalExposure: []ExposureRecord{exposureFixture(t, 10, 10, LossBoundKnown)},
		ProjectedExposure:   []ExposureRecord{exposureFixture(t, 110, 60, LossBoundKnown)},
		ReserveImpact:       money(t, 0),
		Rounding: RoundingEvidence{RequestedMinor: 100, ApprovedMinor: 100,
			RemainderMinor: 0, Method: "FLOOR_TO_BOUNDED_UNITS"},
		Constraints: []AllocationConstraint{{
			Code: ReasonInstrumentLimitExceeded, Before: money(t, 150), After: money(t, 100),
			Explanation: "bounded by the configured instrument limit",
		}},
		Reasons:           []AllocationReason{ReasonInsufficientAvailableCapital, ReasonInstrumentLimitExceeded},
		ConfigurationHash: snapshot.ConfigurationHash(), GeneratedAt: now,
		ValidFrom: proposal.Draft().ValidFrom, ExpiresAt: proposal.Draft().ExpiresAt,
	}
}

func proposalFixture(t *testing.T) strategymodel.TradeProposal {
	t.Helper()
	instrument, _ := domain.InstrumentIDFromCanonicalKey("instrument")
	price, _ := domain.NewPrice(100, "INR")
	definitionID, _ := strategymodel.NewDefinitionID("fixture")
	manifest := strategymodel.VersionManifest{
		DefinitionID: definitionID, ImplementationVersion: "1", InputContractVersion: "1",
		ConfigurationSchemaVersion: "1", StateSchemaVersion: "1",
		ResultSchemaVersion: "1", ProposalSchemaVersion: "1",
	}
	versionID, _ := strategymodel.NewVersionID(manifest)
	config, _ := strategymodel.NewStrategyConfiguration("1", []byte(`{"window":5}`))
	instanceRevision, _ := strategymodel.NewInstanceRevisionID("instance", versionID, config.Hash(), 1)
	evaluationID, _ := strategymodel.NewEvaluationID("evaluation")
	frameID, _ := strategymodel.NewFrameID("frame")
	eventID, _ := marketmodel.NewEventID("event")
	now := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	draft, err := strategymodel.NewProposalDraft(strategymodel.ProposalDraft{
		SchemaVersion: "proposal/v1",
		Legs: []strategymodel.ProposalLeg{{
			InstrumentID: instrument, Side: domain.SideBuy, Ratio: 1,
			ReferencePrice: price, MaxDeviationBPS: 100,
		}},
		Sizing: strategymodel.SizingIntent{
			Kind: strategymodel.SizingStrategyBudgetBPS, ValueBPS: 1000,
		},
		ValidFrom: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
		RationaleCode: "FIXTURE", Explanation: "deterministic fixture",
		Evidence: []strategymodel.Evidence{{
			Code: "SIGNAL", SourceEventIDs: []marketmodel.EventID{eventID},
			Value: 1, Unit: "COUNT", Explanation: "fixture evidence",
		}},
		ExitPolicyReference: "DAY_ONLY",
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := strategymodel.NewTradeProposal(strategymodel.ProposalMetadata{
		DefinitionID: definitionID, VersionID: versionID, InstanceID: "instance",
		InstanceRevisionID: instanceRevision, EvaluationID: evaluationID, FrameID: frameID,
		GeneratedAt: now, SourceEventIDs: []marketmodel.EventID{eventID},
		RequiredInstrumentIDs: []domain.InstrumentID{instrument},
	}, draft)
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

func money(t *testing.T, value int64) domain.Money {
	t.Helper()
	result, err := domain.NewMoney(value, "INR")
	if err != nil {
		t.Fatal(err)
	}
	return result
}
