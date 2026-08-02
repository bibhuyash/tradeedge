package runner

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/canonicaljson"
	"github.com/bibhuyash/tradeedge/internal/domain"
	portfolioallocation "github.com/bibhuyash/tradeedge/internal/portfolio/allocation"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	riskops "github.com/bibhuyash/tradeedge/internal/risk/opshttp"
	"github.com/bibhuyash/tradeedge/internal/risk/rules"
)

func TestProductionRulesPassViolationAndModification(t *testing.T) {
	tests := []struct {
		name   string
		id     riskmodel.RiskRuleID
		config string
		mutate func(*portfoliomodel.PortfolioSnapshotSpec)
		want   riskmodel.RuleResultStatus
	}{
		{"portfolio pass", rules.PortfolioCapitalLimitID, `{"limit_minor":1000000,"currency":"INR"}`, nil, riskmodel.RulePass},
		{"portfolio modification", rules.PortfolioCapitalLimitID, `{"limit_minor":5000,"currency":"INR"}`, nil, riskmodel.RuleModificationRequired},
		{"strategy pass", rules.StrategyAllocationLimitID, `{"limit_minor":100000,"currency":"INR"}`, nil, riskmodel.RulePass},
		{"strategy modification", rules.StrategyAllocationLimitID, `{"limit_minor":5000,"currency":"INR"}`, nil, riskmodel.RuleModificationRequired},
		{"daily pass", rules.DailyLossLimitID, `{"loss_limit_minor":1000,"currency":"INR"}`, nil, riskmodel.RulePass},
		{"daily violation", rules.DailyLossLimitID, `{"loss_limit_minor":1000,"currency":"INR"}`, func(spec *portfoliomodel.PortfolioSnapshotSpec) {
			spec.DailyRealizedPnL = money(t, -1000)
		}, riskmodel.RuleViolation},
		{"drawdown pass", rules.DrawdownLimitID, `{"limit_bps":100}`, nil, riskmodel.RulePass},
		{"drawdown violation", rules.DrawdownLimitID, `{"limit_bps":100}`, func(spec *portfoliomodel.PortfolioSnapshotSpec) {
			spec.RealizedPnL = money(t, -10000)
			spec.CurrentEquity = money(t, 990000)
		}, riskmodel.RuleViolation},
		{"instrument pass", rules.InstrumentExposureLimitID, `{"limit_minor":100000,"currency":"INR"}`, nil, riskmodel.RulePass},
		{"instrument violation", rules.InstrumentExposureLimitID, `{"limit_minor":100,"currency":"INR"}`, nil, riskmodel.RuleViolation},
		{"underlying pass", rules.UnderlyingExposureLimitID, `{"limit_minor":100000,"currency":"INR"}`, nil, riskmodel.RulePass},
		{"underlying violation", rules.UnderlyingExposureLimitID, `{"limit_minor":100,"currency":"INR"}`, nil, riskmodel.RuleViolation},
		{"open pass", rules.MaximumOpenExposureLimitID, `{"limit_minor":100000,"currency":"INR"}`, nil, riskmodel.RulePass},
		{"open violation", rules.MaximumOpenExposureLimitID, `{"limit_minor":100,"currency":"INR"}`, nil, riskmodel.RuleViolation},
		{"reserve pass", rules.ReserveCapitalLimitID, `{"limit_bps":1000}`, nil, riskmodel.RulePass},
		{"reserve modification", rules.ReserveCapitalLimitID, `{"limit_bps":9950}`, nil, riskmodel.RuleModificationRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule, input := productionRuleInput(t, test.id, test.config, test.mutate, false, false)
			result := rule.Evaluate(context.Background(), input)
			if result.Status() != test.want {
				t.Fatalf("status = %s, want %s; spec=%+v", result.Status(), test.want, result.Spec())
			}
			if result.Spec().EvaluatedAt != input.Spec().EvaluatedAt {
				t.Fatal("logical evaluation time changed")
			}
		})
	}
}

func TestRiskOperationalSnapshotIsBoundedAndReadOnly(t *testing.T) {
	fixture := newRunnerFixture(t, riskmodel.RulePass, nil)
	handler := riskops.New(fixture.runtime, nil, fixture.runner, time.Second)
	target := "/api/v1/risk/portfolio/snapshot?portfolio=" + fixture.request.PortfolioID.String() + "&limit=100"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("GET status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, target, nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("DELETE status=%d allow=%q", response.Code, response.Header().Get("Allow"))
	}
}

func TestProductionControlRulesFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		id      riskmodel.RiskRuleID
		kill    bool
		circuit bool
		want    riskmodel.RuleResultStatus
	}{
		{"kill clear", rules.KillSwitchRuleID, false, false, riskmodel.RulePass},
		{"kill active", rules.KillSwitchRuleID, true, false, riskmodel.RuleViolation},
		{"circuit closed", rules.CircuitBreakerRuleID, false, false, riskmodel.RulePass},
		{"circuit open", rules.CircuitBreakerRuleID, false, true, riskmodel.RuleViolation},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule, input := productionRuleInput(t, test.id, `{"threshold":3,"reset_threshold":1}`,
				nil, test.kill, test.circuit)
			if result := rule.Evaluate(context.Background(), input); result.Status() != test.want {
				t.Fatalf("status = %s, want %s", result.Status(), test.want)
			}
		})
	}
}

func TestProductionRuleCancellationAndUnknownExposureDefer(t *testing.T) {
	rule, input := productionRuleInput(t, rules.MaximumOpenExposureLimitID,
		`{"limit_minor":100000,"currency":"INR"}`, nil, false, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := rule.Evaluate(ctx, input); result.Status() != riskmodel.RuleDefer {
		t.Fatalf("cancelled status = %s", result.Status())
	}

	rule, input = productionRuleInput(t, rules.MaximumOpenExposureLimitID,
		`{"limit_minor":100000,"currency":"INR"}`, func(spec *portfoliomodel.PortfolioSnapshotSpec) {
			unknown, _ := portfoliomodel.NewUnavailableMoney(portfoliomodel.AvailabilityUnknown)
			known := func(value int64) portfoliomodel.MeasuredMoney {
				result, _ := portfoliomodel.NewKnownMoney(money(t, value))
				return result
			}
			record, err := portfoliomodel.NewExposureRecord(portfoliomodel.ExposureRecordSpec{
				Dimension: portfoliomodel.ExposurePortfolioWide, Subject: spec.PortfolioID.String(),
				Gross: unknown, NetDirectional: known(0), PremiumAtRisk: known(0), Long: known(0), Short: known(0),
				PremiumPaid: known(0), PremiumReceived: known(0), MaximumLoss: known(0), LossBound: portfoliomodel.LossBoundKnown,
			})
			if err != nil {
				t.Fatal(err)
			}
			spec.Exposures = []portfoliomodel.ExposureRecord{record}
		}, false, false)
	if result := rule.Evaluate(context.Background(), input); result.Status() != riskmodel.RuleDefer {
		t.Fatalf("unknown status = %s", result.Status())
	}
}

func TestProductionDailyLossOverflowDefers(t *testing.T) {
	rule, input := productionRuleInput(t, rules.DailyLossLimitID,
		`{"loss_limit_minor":1000,"currency":"INR"}`, func(spec *portfoliomodel.PortfolioSnapshotSpec) {
			spec.DailyRealizedPnL = money(t, math.MaxInt64)
			spec.DailyUnrealizedPnL = money(t, 1)
		}, false, false)
	if result := rule.Evaluate(context.Background(), input); result.Status() != riskmodel.RuleDefer {
		t.Fatalf("overflow status = %s", result.Status())
	}
}

func productionRuleInput(t *testing.T, id riskmodel.RiskRuleID, raw string,
	mutate func(*portfoliomodel.PortfolioSnapshotSpec), killActive, circuitOpen bool) (rules.Rule, riskmodel.RiskRuleInput) {
	t.Helper()
	fixture := newRunnerFixture(t, riskmodel.RulePass, nil)
	spec := fixture.checkpoint.Snapshot.Spec()
	controlReason, _ := portfoliomodel.NewControlReason("M3_TEST_STATE")
	killID, _ := portfoliomodel.NewKillSwitchID("m3-test")
	circuitID, _ := portfoliomodel.NewCircuitBreakerID("m3-test")
	evidence, _ := portfoliomodel.NewStateChecksum([]byte("m3-test-evidence"))
	killState := portfoliomodel.KillSwitchInactive
	if killActive {
		killState = portfoliomodel.KillSwitchActive
	}
	killSpec := portfoliomodel.KillSwitchSpec{ID: killID, Scope: portfoliomodel.ScopePortfolio,
		ScopeSubject: spec.PortfolioID.String(), State: killState, ReasonCode: controlReason,
		ConfigurationID: spec.ConfigurationID, ConfigurationHash: spec.ConfigurationHash,
		StateRevision: 1, SchemaVersion: "kill-switch/v1"}
	if killActive {
		killSpec.ActivatedAt, killSpec.ActivationEvidence = fixtureTime(), evidence
	}
	kill, err := portfoliomodel.NewKillSwitch(killSpec)
	if err != nil {
		t.Fatal(err)
	}
	circuitState := portfoliomodel.CircuitBreakerClosed
	if circuitOpen {
		circuitState = portfoliomodel.CircuitBreakerOpen
	}
	circuitSpec := portfoliomodel.CircuitBreakerSpec{ID: circuitID, Scope: portfoliomodel.ScopePortfolio,
		ScopeSubject: spec.PortfolioID.String(), State: circuitState, ReasonCode: controlReason,
		ConfigurationID: spec.ConfigurationID, ConfigurationHash: spec.ConfigurationHash,
		StateRevision: 1, SchemaVersion: "circuit-breaker/v1"}
	if circuitOpen {
		circuitSpec.ChangedAt, circuitSpec.Evidence = fixtureTime(), evidence
	}
	circuit, err := portfoliomodel.NewCircuitBreaker(circuitSpec)
	if err != nil {
		t.Fatal(err)
	}
	spec.KillSwitches = []portfoliomodel.KillSwitch{kill}
	spec.CircuitBreakers = []portfoliomodel.CircuitBreaker{circuit}
	if mutate != nil {
		mutate(&spec)
	}
	source, _ := portfoliomodel.NewStateChecksum([]byte("m3-production-rule-input"))
	spec.SourceStateChecksum = source
	snapshot, err := portfoliomodel.NewPortfolioSnapshot(spec)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := fixture.runner.deps.Portfolio.AllocationPolicy(context.Background(), spec.StrategyAllocations[0].Spec().PolicyID)
	if err != nil {
		t.Fatal(err)
	}
	master, err := fixture.runner.deps.Masters.Get(context.Background(), fixture.request.InstrumentMasterVersion)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := (portfolioallocation.Engine{}).Evaluate(portfolioallocation.Input{
		Proposal: fixture.proposal, Snapshot: snapshot, Policy: policy, Master: master, LogicalTime: fixtureTime()})
	if err != nil {
		t.Fatal(err)
	}
	var selected rules.Rule
	for _, value := range rules.ProductionCatalog() {
		if value.Descriptor().ID == id {
			selected = value
		}
	}
	if selected == nil {
		t.Fatalf("missing production rule %s", id)
	}
	canonical, err := canonicaljson.ObjectBounded([]byte(raw), canonicaljson.Limits{MaximumBytes: 64 << 10, MaximumDepth: 8, MaximumCollection: 64})
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := riskmodel.NewRiskConfigurationHash(canonical)
	configuration, err := riskmodel.NewRiskRuleConfiguration(riskmodel.RiskRuleConfiguration{
		Descriptor: selected.Descriptor(), Order: 1, Severity: riskmodel.SeverityBlocking,
		Effect: riskmodel.EffectReject, ConfigurationHash: hash, CanonicalJSON: canonical})
	if err != nil {
		t.Fatal(err)
	}
	policyID, _ := riskmodel.NewRiskPolicyID("m3-production")
	date, _ := domain.NewCivilDate(2026, 8, 1)
	input, err := riskmodel.NewRiskRuleInput(riskmodel.RiskRuleInputSpec{SchemaVersion: "risk-rule-input/v2",
		Proposal: fixture.proposal, PortfolioSnapshot: snapshot, AllocationCandidate: candidate,
		StrategyAllocation: spec.StrategyAllocations[0], TradingDate: date, SessionContext: "PORTFOLIO_DECISION",
		RiskPolicyID: policyID, RiskPolicyVersion: 1, RuleConfiguration: configuration, EvaluatedAt: fixtureTime()})
	if err != nil {
		t.Fatal(err)
	}
	return selected, input
}
