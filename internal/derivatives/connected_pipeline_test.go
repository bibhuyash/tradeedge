package derivatives

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	accountingcoordinator "github.com/bibhuyash/tradeedge/internal/accounting/coordinator"
	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	accountingvaluation "github.com/bibhuyash/tradeedge/internal/accounting/valuation"
	accountingmemory "github.com/bibhuyash/tradeedge/internal/adapters/accounting/memory"
	"github.com/bibhuyash/tradeedge/internal/adapters/broker/paper"
	executionmemory "github.com/bibhuyash/tradeedge/internal/adapters/execution/memory"
	portfoliomemory "github.com/bibhuyash/tradeedge/internal/adapters/portfolio/memory"
	runtimememory "github.com/bibhuyash/tradeedge/internal/adapters/riskruntime/memory"
	"github.com/bibhuyash/tradeedge/internal/domain"
	executioncoordinator "github.com/bibhuyash/tradeedge/internal/execution/coordinator"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
	"github.com/bibhuyash/tradeedge/internal/notification"
	portfolioallocation "github.com/bibhuyash/tradeedge/internal/portfolio/allocation"
	portfolioconfig "github.com/bibhuyash/tradeedge/internal/portfolio/config"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskconfig "github.com/bibhuyash/tradeedge/internal/risk/config"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	"github.com/bibhuyash/tradeedge/internal/risk/rules"
	riskrunner "github.com/bibhuyash/tradeedge/internal/risk/runner"
	riskstorage "github.com/bibhuyash/tradeedge/internal/risk/storage"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

type connectedProposalSource struct {
	values map[strategymodel.ProposalID]strategymodel.TradeProposal
}

func (s connectedProposalSource) Proposal(_ context.Context, id strategymodel.ProposalID) (strategymodel.TradeProposal, error) {
	value, ok := s.values[id]
	if !ok {
		return strategymodel.TradeProposal{}, errors.New("proposal not found")
	}
	return value, nil
}

type connectedPolicySource struct{ value riskmodel.RiskPolicy }

func (s connectedPolicySource) Policy(_ context.Context, id riskmodel.RiskPolicyID) (riskmodel.RiskPolicy, error) {
	if s.value.ID() != id {
		return riskmodel.RiskPolicy{}, errors.New("policy not found")
	}
	return s.value, nil
}

type connectedClock struct{ now time.Time }

func (c *connectedClock) Now() time.Time { return c.now }

type disabledNotificationSender struct{}

func (disabledNotificationSender) Send(context.Context, notification.RenderedMessage) (notification.Receipt, error) {
	return notification.Receipt{}, errors.New("disabled sender must not be called")
}
func (disabledNotificationSender) Status() notification.ProviderStatus {
	return notification.ProviderStatus{Provider: "telegram", State: "DISABLED"}
}

type connectedFixture struct {
	pipeline  ReleasedPipeline
	request   ConnectedRequest
	proposal  strategymodel.TradeProposal
	selection Selection
	option    domain.Instrument
	portfolio portfoliomodel.PortfolioID
	position  *accountingmemory.Store
	observer  *eventSink
	clock     *connectedClock
}

func TestReleasedConnectedShadowUsesPhase3AndCannotMutateExecution(t *testing.T) {
	fixture := newConnectedFixture(t, ConnectedShadow, false, paper.BehaviorImmediateFill)
	result, err := fixture.pipeline.Process(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("process: %v receipt=%#v", err, result.RiskReceipt)
	}
	if result.Decision.Spec().Outcome != riskmodel.DecisionApproved || !result.Intent.IsZero() || len(result.Orders) != 0 || len(result.Fills) != 0 || !result.Position.IsZero() {
		t.Fatalf("shadow crossed mutation boundary: %#v", result)
	}
	if len(fixture.observer.events) != 5 || fixture.observer.events[len(fixture.observer.events)-1].Kind != notification.KindShadowSignal {
		t.Fatalf("shadow event evidence missing: %#v", fixture.observer.events)
	}
}

func TestReleasedConnectedPaperEntryValuationExitReplayAndRestore(t *testing.T) {
	first := runConnectedLifecycle(t)
	second := runConnectedLifecycle(t)
	if !bytes.Equal(first.entry.Decision.CanonicalJSON(), second.entry.Decision.CanonicalJSON()) ||
		!bytes.Equal(first.entry.Intent.CanonicalJSON(), second.entry.Intent.CanonicalJSON()) ||
		!bytes.Equal(first.entry.Fills[0].CanonicalJSON(), second.entry.Fills[0].CanonicalJSON()) ||
		!bytes.Equal(first.entry.Position.CanonicalJSON(), second.entry.Position.CanonicalJSON()) ||
		!bytes.Equal(first.entry.Valuation.CanonicalJSON(), second.entry.Valuation.CanonicalJSON()) ||
		!bytes.Equal(first.exit.Position.CanonicalJSON(), second.exit.Position.CanonicalJSON()) {
		t.Fatal("released connected replay diverged")
	}
	if first.entry.Position.State() != accountingmodel.PositionOpenLong || first.entry.Position.Spec().InstrumentID != first.fixture.option.ID() || first.entry.Fills[0].Spec().Price.MinorUnits() != 10_100 {
		t.Fatalf("wrong option entry authority: %#v", first.entry.Position.Spec())
	}
	if first.entry.Valuation.Status != accountingvaluation.StatusComplete || !first.entry.Valuation.UnrealizedPnL.Known() || first.entry.Valuation.UnrealizedPnL.Value.MinorUnits() != 26_000 {
		t.Fatalf("option valuation missing: %#v", first.entry.Valuation)
	}
	if first.exit.Position.State() != accountingmodel.PositionFlat || first.exit.Position.Spec().NetQuantity.Int64() != 0 || first.exit.Position.Spec().GrossRealizedPnL.MinorUnits() != 19_500 {
		t.Fatalf("option exit not closed: %#v", first.exit.Position.Spec())
	}
	t.Logf("proposal=%s risk=%s intent=%s entry_fill=%s open_checkpoint=%s exit_fill=%s closed_checkpoint=%s valuation=%s", first.fixture.proposal.ID(), first.entry.Decision.ID(), first.entry.Intent.ID(), first.entry.Fills[0].ID(), first.entry.Position.Checksum(), first.exit.Fills[0].ID(), first.exit.Position.Checksum(), first.entry.Valuation.ID)
	publications, err := first.fixture.position.Publications(context.Background(), first.entry.Position.ID())
	if err != nil {
		t.Fatal(err)
	}
	restored := accountingmemory.NewDefault()
	if err = restored.RestorePosition(context.Background(), publications); err != nil {
		t.Fatal(err)
	}
	restoredPosition, err := restored.Position(context.Background(), first.entry.Position.ID())
	if err != nil || !bytes.Equal(restoredPosition.CanonicalJSON(), first.exit.Position.CanonicalJSON()) {
		t.Fatalf("phase 6 checkpoint restore diverged: %v", err)
	}
}

func TestReleasedConnectedDuplicateSuppressionAndEODCloseUnderStop(t *testing.T) {
	fixture := newConnectedFixture(t, ConnectedPaper, false, paper.BehaviorImmediateFill, paper.BehaviorImmediateFill)
	entry, err := fixture.pipeline.Process(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := fixture.pipeline.Process(context.Background(), fixture.request)
	if err != nil || duplicate.RiskReceipt.Outcome != riskrunner.OutcomeDuplicateCommitted || len(duplicate.Fills) != 1 || duplicate.Position.Revision() != entry.Position.Revision() {
		t.Fatalf("duplicate was not idempotently suppressed: %#v %v", duplicate, err)
	}
	exitAt := testAt.Add(3 * time.Second)
	fixture.clock.now = exitAt
	exitProposal := connectedProposal(t, fixture.selection, domain.SideSell, price(t, 10_400), "bearish-exit", exitAt, fixture.option.ID())
	request := fixture.request
	request.Proposal = exitProposal
	request.ExpectedPortfolioRevision = 2
	request.At = exitAt
	request.ExistingOpenOption = fixture.option.ID()
	request.Session = "EOD_CLOSE"
	request.StopNewExposure = true
	request.Mark = nil
	exit, err := fixture.pipeline.Process(context.Background(), request)
	if err != nil || exit.Position.State() != accountingmodel.PositionFlat {
		t.Fatalf("EOD close was blocked or orphaned: %#v %v", exit, err)
	}
}

func TestReleasedConnectedRiskRejectControlsAndNotificationFailure(t *testing.T) {
	rejected := newConnectedFixture(t, ConnectedPaper, true, paper.BehaviorImmediateFill)
	result, err := rejected.pipeline.Process(context.Background(), rejected.request)
	if err != nil || result.Decision.Spec().Outcome != riskmodel.DecisionRejected || len(result.Orders) != 0 {
		t.Fatalf("production risk rejection failed closed: %#v %v", result, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*ConnectedRequest)
		want   error
	}{
		{"stop new exposure", func(r *ConnectedRequest) { r.StopNewExposure = true }, ErrStopNewExposure},
		{"CAS restricted", func(r *ConnectedRequest) { r.CASRestricted = true }, ErrCASRestricted},
		{"session closed", func(r *ConnectedRequest) { r.Session = "MARKET_CLOSED" }, ErrSessionNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newConnectedFixture(t, ConnectedPaper, false, paper.BehaviorImmediateFill)
			tc.mutate(&fixture.request)
			if _, got := fixture.pipeline.Process(context.Background(), fixture.request); !errors.Is(got, tc.want) {
				t.Fatalf("got %v", got)
			}
		})
	}
	fixture := newConnectedFixture(t, ConnectedShadow, false, paper.BehaviorImmediateFill)
	fixture.pipeline.Observer = panicObserver{}
	if _, err = fixture.pipeline.Process(context.Background(), fixture.request); err != nil {
		t.Fatalf("outbound notification failure affected trading authority: %v", err)
	}
}

func TestReleasedConnectedExecutionUnknownAndCancellation(t *testing.T) {
	fixture := newConnectedFixture(t, ConnectedPaper, false, paper.BehaviorTimeout)
	if result, err := fixture.pipeline.Process(context.Background(), fixture.request); err == nil || len(result.Fills) != 0 {
		t.Fatalf("unknown execution did not fail closed: %#v %v", result, err)
	}
	fixture = newConnectedFixture(t, ConnectedPaper, false, paper.BehaviorImmediateFill)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.pipeline.Process(ctx, fixture.request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
}

func TestReleasedConnectedDuplicateAndOutOfOrderPaperEvents(t *testing.T) {
	for _, behavior := range []paper.Behavior{paper.BehaviorDuplicateEvents, paper.BehaviorOutOfOrder} {
		t.Run(string(behavior), func(t *testing.T) {
			fixture := newConnectedFixture(t, ConnectedPaper, false, behavior)
			result, err := fixture.pipeline.Process(context.Background(), fixture.request)
			if err != nil || len(result.Fills) != 1 || result.Position.Revision() != 1 {
				t.Fatalf("non-monotonic or duplicate fill publication: %#v %v", result, err)
			}
		})
	}
}

func TestReleasedConnectedUsesPhase7DispatcherOutboundOnly(t *testing.T) {
	fixture := newConnectedFixture(t, ConnectedShadow, false, paper.BehaviorImmediateFill)
	store, err := notification.NewStore(32, 64)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := notification.NewDispatcher(notification.DefaultConfig(), disabledNotificationSender{}, store, nil, func() time.Time { return testAt })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()
	fixture.pipeline.Observer = dispatcher
	if _, err = fixture.pipeline.Process(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	events := store.RecentEvents(16)
	if len(events) != 5 || events[0].Mode != "SHADOW" {
		t.Fatalf("Phase 7 dispatcher evidence missing: %#v", events)
	}
	for _, delivery := range store.RecentDeliveries(16, false) {
		if delivery.State != notification.DeliverySuppressed || delivery.Reason != "PROVIDER_DISABLED" {
			t.Fatalf("unexpected outbound state: %#v", delivery)
		}
	}
}

type lifecycleResult struct {
	fixture connectedFixture
	entry   ConnectedResult
	exit    ConnectedResult
}

func runConnectedLifecycle(t *testing.T) lifecycleResult {
	t.Helper()
	fixture := newConnectedFixture(t, ConnectedPaper, false, paper.BehaviorImmediateFill, paper.BehaviorImmediateFill)
	entry, err := fixture.pipeline.Process(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	exitAt := testAt.Add(3 * time.Second)
	exitPrice := price(t, 10_400)
	exitProposal := connectedProposal(t, fixture.selection, domain.SideSell, exitPrice, "bearish-exit", exitAt, fixture.option.ID())
	source := fixture.pipeline.Risk
	_ = source
	// The runner owns a proposal interface; the fixture source map is shared and
	// was populated with both deterministic proposals during setup.
	request := fixture.request
	request.Proposal = exitProposal
	request.ExpectedPortfolioRevision = entry.Decision.Spec().ExpectedPortfolioRevision + 1
	request.At = exitAt
	request.ExistingOpenOption = fixture.option.ID()
	request.Mark = nil
	fixture.clock.now = exitAt
	exit, err := fixture.pipeline.Process(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return lifecycleResult{fixture, entry, exit}
}

func newConnectedFixture(t *testing.T, mode ConnectedMode, reject bool, behaviors ...paper.Behavior) connectedFixture {
	t.Helper()
	master, _, option := fixtureMaster(t)
	selection, err := Resolve(master, testAt, price(t, 24_812_00), domain.OptionCall, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	entry := connectedProposal(t, selection, domain.SideBuy, price(t, 10_100), "bullish-entry", testAt, domain.InstrumentID{})
	exit := connectedProposal(t, selection, domain.SideSell, price(t, 10_400), "bearish-exit", testAt.Add(3*time.Second), option.ID())
	proposals := connectedProposalSource{values: map[strategymodel.ProposalID]strategymodel.TradeProposal{entry.ID(): entry, exit.ID(): exit}}

	portfolioRaw, err := os.ReadFile("../../configs/validation/portfolio.paper.json")
	if err != nil {
		t.Fatal(err)
	}
	portfolioConfiguration, err := portfolioconfig.Decode(portfolioRaw)
	if err != nil {
		t.Fatal(err)
	}
	portfolioStore := portfoliomemory.NewStore()
	if _, err = portfolioStore.RegisterConfiguration(context.Background(), portfolioConfiguration); err != nil {
		t.Fatal(err)
	}
	portfolioID, _ := portfoliomodel.NewPortfolioID("phase8-m2-paper")
	allocationID, _ := portfoliomodel.NewStrategyAllocationID("phase8-m2-option")
	metadata := entry.Metadata()
	allocationPolicy := portfolioConfiguration.AllocationPolicy()
	allocation, err := portfoliomodel.NewStrategyAllocation(portfoliomodel.StrategyAllocationSpec{ID: allocationID, DefinitionID: metadata.DefinitionID, VersionID: metadata.VersionID, InstanceID: metadata.InstanceID, InstanceRevisionID: metadata.InstanceRevisionID, PolicyID: allocationPolicy.ID, PolicyVersion: allocationPolicy.Version, Limit: connectedMoney(t, 10_000_000), Deployed: connectedMoney(t, 0), Reserved: connectedMoney(t, 0), Remaining: connectedMoney(t, 10_000_000), DailyLoss: connectedMoney(t, 0), State: portfoliomodel.StrategyAllocationEnabled, EffectiveFrom: testAt.Add(-time.Hour), EffectiveUntil: testAt.Add(time.Hour), ConfigurationHash: portfolioConfiguration.Hash(), SchemaVersion: "strategy-allocation/v1"})
	if err != nil {
		t.Fatal(err)
	}
	capital, _ := portfoliomodel.NewCapitalState(connectedMoney(t, 100_000_000), connectedMoney(t, 100_000_000), connectedMoney(t, 0), connectedMoney(t, 0))
	date, _ := domain.NewCivilDate(2026, 8, 11)
	sourceChecksum, _ := portfoliomodel.NewStateChecksum([]byte("phase8-m2-genesis"))
	reason, _ := portfoliomodel.NewControlReason("PHASE8_M2_HEALTHY")
	killID, _ := portfoliomodel.NewKillSwitchID("phase8-m2")
	killState := portfoliomodel.KillSwitchInactive
	var activationEvidence portfoliomodel.StateChecksum
	var activatedAt time.Time
	if reject {
		killState = portfoliomodel.KillSwitchActive
		activationEvidence, _ = portfoliomodel.NewStateChecksum([]byte("phase8-m2-kill-switch"))
		activatedAt = testAt.Add(-time.Minute)
	}
	kill, err := portfoliomodel.NewKillSwitch(portfoliomodel.KillSwitchSpec{ID: killID, Scope: portfoliomodel.ScopePortfolio, ScopeSubject: portfolioID.String(), State: killState, ReasonCode: reason, ActivationEvidence: activationEvidence, ActivatedAt: activatedAt, ConfigurationID: portfolioConfiguration.ID(), ConfigurationHash: portfolioConfiguration.Hash(), StateRevision: 1, SchemaVersion: "kill-switch/v1"})
	if err != nil {
		t.Fatal(err)
	}
	circuitID, _ := portfoliomodel.NewCircuitBreakerID("phase8-m2")
	circuit, _ := portfoliomodel.NewCircuitBreaker(portfoliomodel.CircuitBreakerSpec{ID: circuitID, Scope: portfoliomodel.ScopePortfolio, ScopeSubject: portfolioID.String(), State: portfoliomodel.CircuitBreakerClosed, ReasonCode: reason, ConfigurationID: portfolioConfiguration.ID(), ConfigurationHash: portfolioConfiguration.Hash(), StateRevision: 1, SchemaVersion: "circuit-breaker/v1"})
	snapshot, err := portfoliomodel.NewPortfolioSnapshot(portfoliomodel.PortfolioSnapshotSpec{SchemaVersion: "portfolio-snapshot/v1", PortfolioID: portfolioID, Revision: 1, AsOfExchangeTime: testAt.Add(-time.Second), GeneratedAt: testAt.Add(-time.Second), TradingDate: date, BaseCurrency: "INR", State: portfoliomodel.PortfolioEnabled, ConfigurationID: portfolioConfiguration.ID(), ConfigurationVersion: portfolioConfiguration.Version(), ConfigurationHash: portfolioConfiguration.Hash(), Capital: capital, RealizedPnL: connectedMoney(t, 0), UnrealizedPnL: connectedMoney(t, 0), DailyRealizedPnL: connectedMoney(t, 0), DailyUnrealizedPnL: connectedMoney(t, 0), WeeklyRealizedPnL: connectedMoney(t, 0), HighWaterMark: connectedMoney(t, 100_000_000), CurrentEquity: connectedMoney(t, 100_000_000), StrategyAllocations: []portfoliomodel.StrategyAllocation{allocation}, KillSwitches: []portfoliomodel.KillSwitch{kill}, CircuitBreakers: []portfoliomodel.CircuitBreaker{circuit}, SourceStateChecksum: sourceChecksum})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := riskstorage.NewPortfolioCheckpoint(riskstorage.PortfolioCheckpoint{Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	riskStore := runtimememory.NewStore()
	if _, err = riskStore.InitializePortfolio(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}

	registry := rules.NewRegistry()
	if err = rules.RegisterProduction(registry); err != nil {
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
	riskConfiguration, err := riskconfig.Decode(riskRaw, descriptors, []string{"INDEX_OPTIONS_VALIDATION"})
	if err != nil {
		t.Fatal(err)
	}
	masterRepo := instrumentmaster.NewMemoryRepository()
	if err = masterRepo.Put(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	runnerConfig := riskrunner.DefaultConfig()
	runnerConfig.Timeout = 5 * time.Second
	riskRunner, err := riskrunner.New(riskrunner.Dependencies{Proposals: proposals, Portfolio: portfolioStore, Policies: connectedPolicySource{riskConfiguration.Policy()}, Masters: masterRepo, Runtime: riskStore}, portfolioallocation.Engine{}, registry, runnerConfig)
	if err != nil {
		t.Fatal(err)
	}

	clock := &connectedClock{testAt}
	scenarios := make([]paper.Scenario, len(behaviors))
	for i, behavior := range behaviors {
		scenarios[i] = paper.Scenario{Behavior: behavior}
	}
	broker, err := paper.NewScripted(clock, scenarios)
	if err != nil {
		t.Fatal(err)
	}
	oms := executionmemory.NewStore()
	execution, err := executioncoordinator.New(oms, broker, executioncoordinator.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	positions := accountingmemory.NewDefault()
	accounting, err := accountingcoordinator.New(positions, accountingcoordinator.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	markQuote := quote(t, option, testAt, 10_500, 10_400, 10_600, 65)
	markChecksum, _ := accountingmodel.NewStateChecksum("phase8-m2-option-mark/v1", []byte(markQuote.ID().String()))
	mark, err := accountingvaluation.NewMarkPrice(markQuote, "phase8-m2-market-r1", markChecksum, readiness.StateReady, readiness.ReasonNone)
	if err != nil {
		t.Fatal(err)
	}
	observer := &eventSink{}
	pipeline := ReleasedPipeline{Risk: riskRunner, RiskStore: riskStore, Execution: execution, OMS: oms, Accounting: accounting, Positions: positions, Observer: observer}
	request := ConnectedRequest{Mode: mode, Proposal: entry, PortfolioID: portfolioID, ExpectedPortfolioRevision: 1, RiskPolicyID: riskConfiguration.Policy().ID(), MasterVersion: master.Version(), At: testAt, Session: "NORMAL_TRADING", Selection: selection, Mark: &mark}
	return connectedFixture{pipeline, request, entry, selection, option, portfolioID, positions, observer, clock}
}

func connectedProposal(t *testing.T, selection Selection, side domain.Side, premium domain.Price, signal string, at time.Time, existing domain.InstrumentID) strategymodel.TradeProposal {
	t.Helper()
	eventID, _ := marketmodel.NewEventID(signal)
	proposal, err := NewOptionProposal(ProposalInput{SignalID: signal, SignalEventID: eventID, At: at, Spot: price(t, 24_790_00), Future: selection.Future, FuturePrice: price(t, 24_812_00), Option: selection.Option, OptionPrice: premium, Side: side, FastEMAScaled: 24_800_00_000_000, SlowEMAScaled: 24_780_00_000_000, QuantityLots: 1, SizingBPS: 1000, ExistingOption: existing})
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

func connectedMoney(t *testing.T, minor int64) domain.Money {
	t.Helper()
	value, err := domain.NewMoney(minor, "INR")
	if err != nil {
		t.Fatal(err)
	}
	return value
}
