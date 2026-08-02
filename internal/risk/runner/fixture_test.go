package runner

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/adapters/portfolio/memory"
	runtimememory "github.com/bibhuyash/tradeedge/internal/adapters/riskruntime/memory"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	portfolioallocation "github.com/bibhuyash/tradeedge/internal/portfolio/allocation"
	portfolioconfig "github.com/bibhuyash/tradeedge/internal/portfolio/config"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	"github.com/bibhuyash/tradeedge/internal/risk/rules"
	riskstorage "github.com/bibhuyash/tradeedge/internal/risk/storage"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

type proposalSource struct{ value strategymodel.TradeProposal }

func (source proposalSource) Proposal(_ context.Context, id strategymodel.ProposalID) (strategymodel.TradeProposal, error) {
	if source.value.ID() != id {
		return strategymodel.TradeProposal{}, fmt.Errorf("proposal not found")
	}
	return source.value, nil
}

type policySource struct{ value riskmodel.RiskPolicy }

func (source policySource) Policy(_ context.Context, id riskmodel.RiskPolicyID) (riskmodel.RiskPolicy, error) {
	if source.value.ID() != id {
		return riskmodel.RiskPolicy{}, fmt.Errorf("policy not found")
	}
	return source.value, nil
}

type testRule struct {
	descriptor riskmodel.RiskRuleDescriptor
	status     riskmodel.RuleResultStatus
	panicValue any
	invalid    bool
	started    chan struct{}
	release    chan struct{}
	inFlight   *atomic.Int32
	maximum    *atomic.Int32
	record     func(string)
	effect     riskmodel.RuleEffect
}

func (rule *testRule) Descriptor() riskmodel.RiskRuleDescriptor { return rule.descriptor }
func (rule *testRule) Evaluate(ctx context.Context, input riskmodel.RiskRuleInput) riskmodel.RuleResult {
	if rule.record != nil {
		rule.record(string(rule.descriptor.ID))
	}
	if rule.panicValue != nil {
		panic(rule.panicValue)
	}
	if rule.invalid {
		return riskmodel.RuleResult{}
	}
	if rule.inFlight != nil {
		current := rule.inFlight.Add(1)
		defer rule.inFlight.Add(-1)
		for {
			previous := rule.maximum.Load()
			if current <= previous || rule.maximum.CompareAndSwap(previous, current) {
				break
			}
		}
	}
	if rule.started != nil {
		select {
		case rule.started <- struct{}{}:
		default:
		}
	}
	if rule.release != nil {
		select {
		case <-rule.release:
		case <-ctx.Done():
			return riskmodel.RuleResult{}
		}
	}
	spec := input.Spec()
	configured := spec.RuleConfiguration
	effect := riskmodel.EffectNone
	reason := "WITHIN_LIMIT"
	switch rule.status {
	case riskmodel.RuleViolation:
		effect, reason = riskmodel.EffectReject, "LIMIT_EXCEEDED"
	case riskmodel.RuleModificationRequired:
		effect, reason = riskmodel.EffectModify, "MODIFICATION_REQUIRED"
	case riskmodel.RuleDefer:
		effect, reason = riskmodel.EffectDefer, "DATA_UNAVAILABLE"
	case riskmodel.RuleError:
		effect, reason = riskmodel.EffectDefer, "RULE_ERROR"
	}
	if rule.effect != "" && rule.status == riskmodel.RuleViolation {
		effect = rule.effect
	}
	resultSpec := riskmodel.RuleResultSpec{RuleID: configured.Descriptor.ID,
		RuleVersion: configured.Descriptor.Version, ConfigurationHash: configured.ConfigurationHash,
		Status: rule.status, ReasonCode: reason, Severity: riskmodel.SeverityBlocking,
		Effect: effect, EvaluatedAt: spec.EvaluatedAt}
	if rule.status == riskmodel.RuleViolation || rule.status == riskmodel.RuleModificationRequired {
		observed := riskmodel.EvidenceValue{Kind: riskmodel.EvidenceMoney,
			Availability: portfoliomodel.AvailabilityKnown, Money: spec.AllocationCandidate.CandidateCapital()}
		evidence, _ := riskmodel.NewRiskEvidence(riskmodel.RiskEvidenceSpec{Code: "CAPITAL_LIMIT",
			Observed: observed, Limit: observed, Projected: observed, RemainingHeadroom: observed,
			Unit: "INR_MINOR", Comparison: riskmodel.CompareLessOrEqual,
			SubjectType: riskmodel.SubjectAllocation, SubjectIdentity: spec.AllocationCandidate.ID().String(),
			SourceSnapshotID: spec.PortfolioSnapshot.ID(), SourceProposalID: spec.Proposal.ID(),
			FormulaVersion: "test-rule/v1", EvidenceAt: spec.EvaluatedAt, Explanation: "test evidence"})
		resultSpec.Evidence = []riskmodel.RiskEvidence{evidence}
	}
	if rule.status == riskmodel.RuleModificationRequired {
		candidate := spec.AllocationCandidate.Spec()
		half, _ := domain.NewMoney(candidate.CandidateCapital.MinorUnits()/2, candidate.CandidateCapital.Currency().String())
		legs := append([]portfoliomodel.AllocationLegBound(nil), candidate.LegBounds...)
		for index := range legs {
			units := legs[index].MaximumUnits.Int64() / 2
			units -= units % legs[index].LotSize.Int64()
			legs[index].MaximumUnits, _ = domain.NewQuantity(units)
		}
		resultSpec.Adjustment = &riskmodel.RuleAdjustment{MaximumCapital: half, LegBounds: legs,
			Constraints: []portfoliomodel.AllocationConstraint{{Code: portfoliomodel.ReasonInstrumentLimitExceeded,
				Before: candidate.CandidateCapital, After: half, Explanation: "test modification"}},
			ValidUntil: candidate.ExpiresAt}
	}
	result, _ := riskmodel.NewRuleResult(resultSpec)
	return result
}

type runnerFixture struct {
	runner     *Runner
	runtime    *runtimememory.Store
	request    Request
	rule       *testRule
	proposal   strategymodel.TradeProposal
	checkpoint riskstorage.PortfolioCheckpoint
}

func newRunnerFixture(t *testing.T, status riskmodel.RuleResultStatus, mutate func(*testRule)) runnerFixture {
	t.Helper()
	now := fixtureTime()
	instrument := fixtureInstrument(t)
	master, err := instrumentmaster.New(now.Add(-time.Hour), []domain.Instrument{instrument}, nil)
	if err != nil {
		t.Fatal(err)
	}
	masterRepo := instrumentmaster.NewMemoryRepository()
	if err := masterRepo.Put(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	proposal := fixtureProposal(t, instrument, now)
	configRaw := []byte(fmt.Sprintf(`{"schema_version":"portfolio/v1","version":1,"enabled":true,"base_currency":"INR","effective_from":%q,"effective_until":%q,"total_capital_minor":1000000,"reserve_bps":1000,"emergency_reserve_bps":500,"maximum_strategy_capital_minor":100000,"maximum_instrument_capital_minor":100000,"maximum_underlying_capital_minor":100000,"maximum_exposure_group_capital_minor":100000,"maximum_strategies":10,"exposure_groups":["ALL"]}`,
		now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano)))
	configuration, err := portfolioconfig.Decode(configRaw)
	if err != nil {
		t.Fatal(err)
	}
	portfolioStore := memory.NewStore()
	if _, err := portfolioStore.RegisterConfiguration(context.Background(), configuration); err != nil {
		t.Fatal(err)
	}
	portfolioID, _ := portfoliomodel.NewPortfolioID("primary")
	allocationID, _ := portfoliomodel.NewStrategyAllocationID("primary-allocation")
	metadata := proposal.Metadata()
	allocationPolicy := configuration.AllocationPolicy()
	allocation, err := portfoliomodel.NewStrategyAllocation(portfoliomodel.StrategyAllocationSpec{
		ID: allocationID, DefinitionID: metadata.DefinitionID, VersionID: metadata.VersionID,
		InstanceID: metadata.InstanceID, InstanceRevisionID: metadata.InstanceRevisionID,
		PolicyID: allocationPolicy.ID, PolicyVersion: allocationPolicy.Version,
		Limit: money(t, 100000), Deployed: money(t, 0), Reserved: money(t, 0), Remaining: money(t, 100000),
		DailyLoss: money(t, 0), State: portfoliomodel.StrategyAllocationEnabled,
		EffectiveFrom: now.Add(-time.Hour), EffectiveUntil: now.Add(time.Hour),
		ConfigurationHash: configuration.Hash(), SchemaVersion: "strategy-allocation/v1"})
	if err != nil {
		t.Fatal(err)
	}
	capital, _ := portfoliomodel.NewCapitalState(money(t, 1000000), money(t, 1000000), money(t, 0), money(t, 0))
	date, _ := domain.NewCivilDate(2026, 8, 1)
	source, _ := portfoliomodel.NewStateChecksum([]byte("genesis"))
	controlReason, _ := portfoliomodel.NewControlReason("RUNNER_FIXTURE_CLEAR")
	killID, _ := portfoliomodel.NewKillSwitchID("runner-fixture")
	kill, err := portfoliomodel.NewKillSwitch(portfoliomodel.KillSwitchSpec{ID: killID,
		Scope: portfoliomodel.ScopePortfolio, ScopeSubject: portfolioID.String(), State: portfoliomodel.KillSwitchInactive,
		ReasonCode: controlReason, ConfigurationID: configuration.ID(), ConfigurationHash: configuration.Hash(),
		StateRevision: 1, SchemaVersion: "kill-switch/v1"})
	if err != nil {
		t.Fatal(err)
	}
	circuitID, _ := portfoliomodel.NewCircuitBreakerID("runner-fixture")
	circuit, err := portfoliomodel.NewCircuitBreaker(portfoliomodel.CircuitBreakerSpec{ID: circuitID,
		Scope: portfoliomodel.ScopePortfolio, ScopeSubject: portfolioID.String(), State: portfoliomodel.CircuitBreakerClosed,
		ReasonCode: controlReason, ConfigurationID: configuration.ID(), ConfigurationHash: configuration.Hash(),
		StateRevision: 1, SchemaVersion: "circuit-breaker/v1"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := portfoliomodel.NewPortfolioSnapshot(portfoliomodel.PortfolioSnapshotSpec{
		SchemaVersion: "portfolio-snapshot/v1", PortfolioID: portfolioID, Revision: 1,
		AsOfExchangeTime: now.Add(-time.Minute), GeneratedAt: now.Add(-time.Minute), TradingDate: date,
		BaseCurrency: "INR", State: portfoliomodel.PortfolioEnabled, ConfigurationID: configuration.ID(),
		ConfigurationVersion: configuration.Version(), ConfigurationHash: configuration.Hash(), Capital: capital,
		RealizedPnL: money(t, 0), UnrealizedPnL: money(t, 0), DailyRealizedPnL: money(t, 0),
		DailyUnrealizedPnL: money(t, 0), WeeklyRealizedPnL: money(t, 0), HighWaterMark: money(t, 1000000),
		CurrentEquity: money(t, 1000000), StrategyAllocations: []portfoliomodel.StrategyAllocation{allocation},
		KillSwitches: []portfoliomodel.KillSwitch{kill}, CircuitBreakers: []portfoliomodel.CircuitBreaker{circuit},
		SourceStateChecksum: source})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := riskstorage.NewPortfolioCheckpoint(riskstorage.PortfolioCheckpoint{Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	runtimeStore := runtimememory.NewStore()
	if _, err := runtimeStore.InitializePortfolio(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	descriptor := riskmodel.RiskRuleDescriptor{ID: "TEST_RULE", Version: 1, Name: "test", Description: "test rule", SchemaVersion: "risk-rule/v1"}
	ruleHash, _ := riskmodel.NewRiskConfigurationHash([]byte(`{"limit_minor":1}`))
	ruleConfiguration, err := riskmodel.NewRiskRuleConfiguration(riskmodel.RiskRuleConfiguration{Descriptor: descriptor,
		Order: 1, Severity: riskmodel.SeverityBlocking, Effect: effectFor(status), ConfigurationHash: ruleHash,
		CanonicalJSON: []byte(`{"limit_minor":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	policyHash, _ := riskmodel.NewRiskConfigurationHash([]byte(`{"policy":1}`))
	policyID, _ := riskmodel.NewRiskPolicyID("policy")
	policy, err := riskmodel.NewRiskPolicy(riskmodel.RiskPolicySpec{ID: policyID, Version: 1,
		SchemaVersion: "risk-policy/v1", Lifecycle: riskmodel.PolicyActive, FailPosture: riskmodel.FailClosed,
		EffectiveFrom: now.Add(-time.Hour), EffectiveUntil: now.Add(time.Hour),
		Rules: []riskmodel.RiskRuleConfiguration{ruleConfiguration}, ConfigurationHash: policyHash})
	if err != nil {
		t.Fatal(err)
	}
	rule := &testRule{descriptor: descriptor, status: status}
	if mutate != nil {
		mutate(rule)
	}
	registry := rules.NewRegistry()
	if err := registry.Register(rule); err != nil {
		t.Fatal(err)
	}
	runner, err := New(Dependencies{Proposals: proposalSource{proposal}, Portfolio: portfolioStore,
		Policies: policySource{policy}, Masters: masterRepo, Runtime: runtimeStore},
		portfolioallocation.Engine{}, registry, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	request := Request{PortfolioID: portfolioID, ProposalID: proposal.ID(), ExpectedRevision: 1,
		RiskPolicyID: policyID, InstrumentMasterVersion: master.Version(), LogicalTime: now}
	return runnerFixture{runner: runner, runtime: runtimeStore, request: request, rule: rule,
		proposal: proposal, checkpoint: checkpoint}
}

func effectFor(status riskmodel.RuleResultStatus) riskmodel.RuleEffect {
	switch status {
	case riskmodel.RuleViolation:
		return riskmodel.EffectReject
	case riskmodel.RuleModificationRequired:
		return riskmodel.EffectModify
	case riskmodel.RuleDefer, riskmodel.RuleError:
		return riskmodel.EffectDefer
	default:
		return riskmodel.EffectNone
	}
}
func money(t *testing.T, value int64) domain.Money {
	t.Helper()
	result, err := domain.NewMoney(value, "INR")
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func fixtureTime() time.Time { return time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC) }

func fixtureInstrument(t *testing.T) domain.Instrument {
	t.Helper()
	underlying, _ := domain.NewUnderlyingID("NIFTY")
	lot, _ := domain.NewQuantity(50)
	tick, _ := domain.NewPrice(1, "INR")
	value, err := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentCash,
		UnderlyingID: underlying, Type: domain.InstrumentEquity, ExchangeSymbol: "TEST", LotSize: lot,
		TickSize: tick, Currency: "INR"})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func fixtureProposal(t *testing.T, instrument domain.Instrument, now time.Time) strategymodel.TradeProposal {
	t.Helper()
	definitionID, _ := strategymodel.NewDefinitionID("risk-runtime")
	manifest := strategymodel.VersionManifest{DefinitionID: definitionID, ImplementationVersion: "1",
		InputContractVersion: "1", ConfigurationSchemaVersion: "1", StateSchemaVersion: "1",
		ResultSchemaVersion: "1", ProposalSchemaVersion: "1"}
	versionID, _ := strategymodel.NewVersionID(manifest)
	configuration, _ := strategymodel.NewStrategyConfiguration("1", []byte(`{"fixture":1}`))
	revision, _ := strategymodel.NewInstanceRevisionID("risk-runtime", versionID, configuration.Hash(), 1)
	evaluationID, _ := strategymodel.NewEvaluationID("risk-runtime")
	frameID, _ := strategymodel.NewFrameID("risk-runtime")
	eventID, _ := marketmodel.NewEventID("risk-runtime")
	price, _ := domain.NewPrice(100, "INR")
	draft, err := strategymodel.NewProposalDraft(strategymodel.ProposalDraft{SchemaVersion: "proposal/v1",
		Legs: []strategymodel.ProposalLeg{{InstrumentID: instrument.ID(), Side: domain.SideBuy, Ratio: 1,
			ReferencePrice: price, MaxDeviationBPS: 100}},
		Sizing:    strategymodel.SizingIntent{Kind: strategymodel.SizingStrategyBudgetBPS, ValueBPS: 1000},
		ValidFrom: now.Add(-time.Minute), ExpiresAt: now.Add(10 * time.Minute), RationaleCode: "TEST",
		Explanation: "runtime fixture", Evidence: []strategymodel.Evidence{{Code: "SIGNAL",
			SourceEventIDs: []marketmodel.EventID{eventID}, Value: 1, Unit: "COUNT", Explanation: "test"}},
		ExitPolicyReference: "DAY_ONLY"})
	if err != nil {
		t.Fatal(err)
	}
	value, err := strategymodel.NewTradeProposal(strategymodel.ProposalMetadata{DefinitionID: definitionID,
		VersionID: versionID, InstanceID: "risk-runtime", InstanceRevisionID: revision,
		EvaluationID: evaluationID, FrameID: frameID, GeneratedAt: now,
		SourceEventIDs: []marketmodel.EventID{eventID}, RequiredInstrumentIDs: []domain.InstrumentID{instrument.ID()}}, draft)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
