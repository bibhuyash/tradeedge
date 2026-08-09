package tradingruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/accounting/valuation"
	"github.com/bibhuyash/tradeedge/internal/domain"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	executionfixture "github.com/bibhuyash/tradeedge/internal/execution/testfixture"
	"github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	riskfixture "github.com/bibhuyash/tradeedge/internal/risk/testfixture"
)

func TestSessionCalendarCASAndLegalTransitions(t *testing.T) {
	schedule, times := testCalendar(t)
	coordinator, err := NewSessionCoordinator(schedule, domain.ExchangeNSE)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		at    time.Time
		ready bool
		want  SessionState
	}{
		{times["pre"], false, SessionPreMarket}, {times["normal"], false, SessionWarmingUp},
		{times["normal"], true, SessionReady}, {times["normal"], true, SessionNormalTrading},
		{times["precas"], true, SessionPreCAS}, {times["cas"], true, SessionCASActive},
		{times["postcas"], true, SessionPostCAS}, {times["close"], true, SessionClosing},
		{times["close"], true, SessionClosed},
	}
	for _, step := range steps {
		got, advanceErr := coordinator.Advance(context.Background(), step.at, step.ready)
		if advanceErr != nil || got.State != step.want {
			t.Fatalf("at %s: state=%s err=%v want=%s", step.at, got.State, advanceErr, step.want)
		}
	}
	if _, err := coordinator.Stop(times["close"]); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Advance(context.Background(), times["normal"], true); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected invalid transition, got %v", err)
	}
}

func TestReadinessAndStrategyCASFailClosed(t *testing.T) {
	at := time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC)
	complete := append(healthyDependencies(at), Dependency{Name: "oms", Requirement: Required, State: HealthReady, ObservedAt: at}, Dependency{Name: "accounting", Requirement: Required, State: HealthReady, ObservedAt: at})
	ready := AggregateReadiness(ModePaper, complete, at)
	if !ready.Ready {
		t.Fatalf("expected ready: %+v", ready)
	}
	blocked := AggregateReadiness(ModeShadow, []Dependency{{Name: "mapping", Requirement: Required, State: HealthUnknown}}, at)
	if blocked.Ready || blocked.State != HealthBlocked {
		t.Fatalf("expected fail closed: %+v", blocked)
	}
	if AggregateReadiness(Mode("LIVE_ENABLED"), nil, at).Ready {
		t.Fatal("unknown live mode became ready")
	}

	manager := NewStrategyManager()
	if _, err := manager.Register(StrategyRegistration{ID: "cas-restricted", Enabled: true, CAS: CASRestricted, Version: "v1"}, at); err != nil {
		t.Fatal(err)
	}
	values := manager.Reconcile(SessionSnapshot{State: SessionCASActive, Regime: calendar.RegimeCAS}, ready, ControlSnapshot{}, at)
	if values[0].State != StrategySessionRestricted || !strategyAllows(values[0], ExposureReduce, calendar.RegimeCAS) || strategyAllows(values[0], ExposureIncrease, calendar.RegimeCAS) {
		t.Fatalf("unexpected CAS eligibility: %+v", values[0])
	}
	disabled, err := manager.Disable("cas-restricted", "OPERATOR_CONFIGURATION", at.Add(time.Second))
	if err != nil || disabled.State != StrategyDisabled || len(manager.Transitions()) < 3 {
		t.Fatalf("safe disable failed: %+v %v", disabled, err)
	}

	haltedManager := NewStrategyManager()
	_, _ = haltedManager.Register(StrategyRegistration{ID: "halted", Enabled: true, CAS: CASSafe, Version: "v1"}, at)
	halted := haltedManager.Reconcile(SessionSnapshot{State: SessionNormalTrading, Regime: calendar.RegimeNormal}, ready, ControlSnapshot{GlobalBlocked: true}, at)[0]
	if halted.State != StrategyHalted {
		t.Fatalf("control did not halt strategy: %+v", halted)
	}
	recovered, err := haltedManager.Recover("halted", "CONTROL_RECOVERY_EVIDENCE", at.Add(time.Second))
	if err != nil || recovered.State != StrategyRegistered {
		t.Fatalf("recovery failed: %+v %v", recovered, err)
	}
}

func TestRuntimeRestoreBeforeActivateEndToEndAndShutdown(t *testing.T) {
	runtime, stages, clock, manifest := testRuntime(t, nil)
	event := testQuote(t, *clock)
	if _, err := runtime.Process(context.Background(), event); !errors.Is(err, ErrRestoreRequired) {
		t.Fatalf("expected restore block, got %v", err)
	}
	deps := healthyDependencies(*clock)
	if err := runtime.Start(context.Background(), manifest, deps); err != nil {
		t.Fatal(err)
	}
	runtime.Refresh(context.Background(), deps)
	runtime.Refresh(context.Background(), deps)
	receipt, err := runtime.Process(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Outcome != OutcomeCompleted || receipt.ProposalCount != 1 || receipt.DecisionCount != 1 || receipt.PlanCount != 1 || receipt.FillCount != 1 || len(receipt.FinancialRevisions) != 1 {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	if stages.feedback != 1 || stages.accountingCalls != 1 {
		t.Fatalf("financial feedback missing: %+v", stages)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtime.Snapshot().State != RuntimeStopped || runtime.Snapshot().Session.State != SessionStopped {
		t.Fatalf("not stopped: %+v", runtime.Snapshot())
	}
}

func TestRuntimeBackpressureDegradesWithoutDroppingAcceptedWork(t *testing.T) {
	block := make(chan struct{})
	runtime, _, clock, manifest := testRuntime(t, block)
	deps := healthyDependencies(*clock)
	if err := runtime.Start(context.Background(), manifest, deps); err != nil {
		t.Fatal(err)
	}
	runtime.Refresh(context.Background(), deps)
	runtime.Refresh(context.Background(), deps)
	event := testQuote(t, *clock)
	done := make(chan error, 1)
	go func() { _, err := runtime.Process(context.Background(), event); done <- err }()
	<-runtime.deps.Strategy.(*fakeStages).started
	if _, err := runtime.Process(context.Background(), event); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("expected backpressure, got %v", err)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if runtime.Snapshot().State != RuntimeDegraded {
		t.Fatalf("expected degraded: %+v", runtime.Snapshot())
	}
}

func TestCheckpointManifestCanonicalAndCorruption(t *testing.T) {
	value, err := NewCheckpointManifest(CheckpointManifest{SchemaVersion: 1, Mode: ModePaper, CalendarVersion: "calendar", Configuration: "config", Session: SessionClosed, Heads: []CheckpointHead{{Subsystem: "strategy", Revision: "1", Checksum: "a"}, {Subsystem: "oms", Revision: "2", Checksum: "b"}}, CreatedAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC), CleanShutdown: true})
	if err != nil || value.Verify() != nil {
		t.Fatal(err)
	}
	corrupt := value
	corrupt.Heads[0].Checksum = "changed"
	if !errors.Is(corrupt.Verify(), ErrCheckpointCorrupt) {
		t.Fatal("corrupt manifest accepted")
	}
}

func TestDeterministicFullDayReplayAcrossCASAndClosing(t *testing.T) {
	run := func() []byte {
		runtime, stages, clock, manifest := testRuntime(t, nil)
		location, _ := time.LoadLocation("Asia/Kolkata")
		at := func(hour, minute int) time.Time { return time.Date(2026, 8, 10, hour, minute, 0, 0, location) }
		*clock = at(9, 0)
		dependencies := healthyDependencies(*clock)
		if err := runtime.Start(context.Background(), manifest, dependencies); err != nil {
			t.Fatal(err)
		}
		var receipts []EventReceipt
		for _, step := range []struct {
			at          time.Time
			evaluations int
		}{{at(9, 16), 2}, {at(14, 56), 1}, {at(15, 1), 1}, {at(15, 11), 1}} {
			*clock = step.at
			for count := 0; count < step.evaluations; count++ {
				runtime.Refresh(context.Background(), healthyDependencies(*clock))
			}
			receipt, err := runtime.Process(context.Background(), testQuote(t, *clock))
			if err != nil {
				t.Fatal(err)
			}
			receipts = append(receipts, receipt)
		}
		*clock = at(15, 31)
		runtime.Refresh(context.Background(), healthyDependencies(*clock))
		runtime.Refresh(context.Background(), healthyDependencies(*clock))
		if err := runtime.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(struct {
			Receipts             []EventReceipt
			Snapshot             RuntimeSnapshot
			Feedback, Accounting int
		}{receipts, runtime.Snapshot(), stages.feedback, stages.accountingCalls})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	first, second := run(), run()
	if !bytes.Equal(first, second) {
		t.Fatalf("full-day replay differs\n%s\n%s", first, second)
	}
}

type fakeStages struct {
	decision        riskmodel.PortfolioRiskDecision
	plan            executionmodel.OrderPlan
	block           <-chan struct{}
	started         chan struct{}
	startOnce       sync.Once
	mu              sync.Mutex
	feedback        int
	accountingCalls int
	observed        func() time.Time
}

func (f *fakeStages) Evaluate(ctx context.Context, _ marketmodel.Event, strategies []StrategySnapshot) ([]Proposal, error) {
	if f.block != nil {
		f.startOnce.Do(func() { close(f.started) })
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []Proposal{{StrategyID: "fixture", Value: f.decision.Spec().Proposal, Effect: ExposureIncrease}}, nil
}
func (f *fakeStages) Decide(context.Context, Proposal) (riskmodel.PortfolioRiskDecision, error) {
	return f.decision, nil
}
func (f *fakeStages) UpdateFinancial(context.Context, valuation.PortfolioFinancialSnapshot) error {
	f.mu.Lock()
	f.feedback++
	f.mu.Unlock()
	return nil
}
func (f *fakeStages) Execute(context.Context, riskmodel.PortfolioRiskDecision) (ExecutionResult, error) {
	return ExecutionResult{Plan: f.plan, Fills: []executionmodel.Fill{{}}}, nil
}
func (f *fakeStages) Ingest(context.Context, ExecutionResult) (valuation.PortfolioFinancialSnapshot, error) {
	f.mu.Lock()
	f.accountingCalls++
	f.mu.Unlock()
	return valuation.PortfolioFinancialSnapshot{Revision: 1}, nil
}
func (f *fakeStages) Controls(context.Context) (ControlSnapshot, error) {
	return ControlSnapshot{}, nil
}
func (f *fakeStages) Restore(context.Context, CheckpointManifest) error { return nil }
func (f *fakeStages) Checkpoint(context.Context) (CheckpointManifest, error) {
	return NewCheckpointManifest(CheckpointManifest{SchemaVersion: 1, Mode: ModePaper, CalendarVersion: "runtime", Configuration: "config", Session: SessionDraining, Heads: []CheckpointHead{{Subsystem: "all", Revision: "1", Checksum: "clean"}}, CreatedAt: time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC), CleanShutdown: true})
}
func (f *fakeStages) Drain(context.Context) error    { return nil }
func (f *fakeStages) Shutdown(context.Context) error { return nil }

func testRuntime(t *testing.T, block <-chan struct{}) (*Runtime, *fakeStages, *time.Time, CheckpointManifest) {
	t.Helper()
	schedule, times := testCalendar(t)
	now := times["normal"]
	session, _ := NewSessionCoordinator(schedule, domain.ExchangeNSE)
	strategies := NewStrategyManager()
	_, _ = strategies.Register(StrategyRegistration{ID: "fixture", Enabled: true, CAS: CASSafe, Version: "v1"}, now)
	decision, err := riskfixture.ApprovedDecision()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := executionfixture.New(false)
	if err != nil {
		t.Fatal(err)
	}
	stages := &fakeStages{decision: decision, plan: fixture.Plan, block: block, observed: func() time.Time { return now }}
	if block != nil {
		stages.started = make(chan struct{})
	}
	cfg := DefaultConfig()
	cfg.AdmissionLimit = 1
	cfg.OperationTimeout = time.Second
	runtime, err := New(cfg, session, strategies, PipelineDependencies{Strategy: stages, Risk: stages, Execution: fakeExecution{stages}, Accounting: fakeAccounting{stages}, Controls: stages, Restorer: stages, Checkpointer: stages, Drainer: stages, Shutdowner: stages}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := NewCheckpointManifest(CheckpointManifest{SchemaVersion: 1, Mode: ModePaper, CalendarVersion: string(schedule.Version()), Configuration: "config", Session: SessionStarting, Heads: []CheckpointHead{{Subsystem: "all", Revision: "0", Checksum: "genesis"}}, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return runtime, stages, &now, manifest
}

func healthyDependencies(at time.Time) []Dependency {
	names := []string{"configuration", "calendar", "market_data", "strategy", "risk", "paper_broker", "valuation", "instrument_mappings", "reconciliation"}
	values := make([]Dependency, len(names))
	for index, name := range names {
		values[index] = Dependency{Name: name, Requirement: Required, State: HealthReady, ObservedAt: at}
	}
	return values
}

func testCalendar(t *testing.T) (*calendar.Schedule, map[string]time.Time) {
	t.Helper()
	location, _ := time.LoadLocation("Asia/Kolkata")
	date, _ := domain.NewCivilDate(2026, 8, 10)
	at := func(hour, minute int) time.Time { return time.Date(2026, 8, 10, hour, minute, 0, 0, location) }
	schedule, err := calendar.New(calendar.Spec{Source: calendar.Source{Name: "test", PublishedAt: at(8, 0)}, Timezone: "Asia/Kolkata", EffectiveFrom: date, EffectiveTo: date, Days: []calendar.TradingDay{{Exchange: domain.ExchangeNSE, Date: date, Status: calendar.DayTrading, Sessions: []calendar.Session{{Open: at(9, 15), Close: at(15, 30), Kind: calendar.SessionRegular}}, Regimes: []calendar.RegimeWindow{{Open: at(14, 55), Close: at(15, 0), Regime: calendar.RegimePreCAS}, {Open: at(15, 0), Close: at(15, 10), Regime: calendar.RegimeCAS}, {Open: at(15, 10), Close: at(15, 20), Regime: calendar.RegimePostCAS}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return schedule, map[string]time.Time{"pre": at(9, 0), "normal": at(9, 16), "precas": at(14, 56), "cas": at(15, 1), "postcas": at(15, 11), "close": at(15, 31)}
}

func testQuote(t *testing.T, at time.Time) marketmodel.QuoteEvent {
	t.Helper()
	instrument, _ := domain.InstrumentIDFromCanonicalKey("runtime-test")
	price, _ := domain.NewPrice(100, "INR")
	quote, err := marketmodel.NewQuoteEvent(marketmodel.QuoteSpec{InstrumentID: instrument, LastPrice: price, Volume: 1, ExchangeTime: at, IngestedAt: at, Provenance: marketmodel.Provenance{Provider: "fixture", ProviderToken: "token", MasterVersion: "v1"}})
	if err != nil {
		t.Fatal(err)
	}
	return quote
}

type fakeExecution struct{ *fakeStages }

func (f fakeExecution) Health(context.Context) Dependency {
	return Dependency{Name: "oms", Requirement: Required, State: HealthReady, ObservedAt: f.observed()}
}

type fakeAccounting struct{ *fakeStages }

func (f fakeAccounting) Health(context.Context) Dependency {
	return Dependency{Name: "accounting", Requirement: Required, State: HealthReady, ObservedAt: f.observed()}
}
