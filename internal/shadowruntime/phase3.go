package shadowruntime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	portfoliomemory "github.com/bibhuyash/tradeedge/internal/adapters/portfolio/memory"
	runtimememory "github.com/bibhuyash/tradeedge/internal/adapters/riskruntime/memory"
	"github.com/bibhuyash/tradeedge/internal/derivatives"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
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

// Phase3Config contains only released portfolio/risk authorities. There are no
// Phase 4, broker, OMS, fill, or accounting dependencies in this composition.
type Phase3Config struct {
	Master                 instrumentmaster.Master
	PortfolioConfiguration portfolioconfig.PortfolioConfiguration
	RiskConfiguration      riskconfig.RiskConfiguration
	Timeout                time.Duration
	KillSwitchActive       bool
}

type phase3ProposalSource struct {
	values map[strategymodel.ProposalID]strategymodel.TradeProposal
}

func (s *phase3ProposalSource) Proposal(_ context.Context, id strategymodel.ProposalID) (strategymodel.TradeProposal, error) {
	value, ok := s.values[id]
	if !ok {
		return strategymodel.TradeProposal{}, errors.New("shadow proposal not found")
	}
	return value, nil
}

type phase3PolicySource struct{ value riskmodel.RiskPolicy }

func (s phase3PolicySource) Policy(_ context.Context, id riskmodel.RiskPolicyID) (riskmodel.RiskPolicy, error) {
	if s.value.ID() != id {
		return riskmodel.RiskPolicy{}, errors.New("shadow risk policy not found")
	}
	return s.value, nil
}

// Phase3Gateway evaluates the real released Phase 3 rules through the M2
// SHADOW boundary. Its object graph intentionally cannot submit an order.
type Phase3Gateway struct {
	mu        sync.Mutex
	config    Phase3Config
	proposals *phase3ProposalSource
	portfolio *portfoliomemory.Store
	store     *runtimememory.Store
	runner    *riskrunner.Runner
	pipeline  derivatives.ReleasedPipeline
	ID        portfoliomodel.PortfolioID
	PolicyID  riskmodel.RiskPolicyID
	started   bool
}

func NewPhase3Gateway(ctx context.Context, config Phase3Config) (*Phase3Gateway, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.Master.Version() == "" || !config.PortfolioConfiguration.Enabled() ||
		config.RiskConfiguration.Policy().ID().IsZero() {
		return nil, ErrInvalid
	}
	if config.Timeout <= 0 {
		config.Timeout = 5 * time.Second
	}
	if config.Timeout > time.Minute {
		return nil, ErrInvalid
	}
	if err := rules.ValidateProductionPolicy(config.RiskConfiguration.Policy()); err != nil {
		return nil, err
	}
	portfolioStore := portfoliomemory.NewStore()
	if _, err := portfolioStore.RegisterConfiguration(ctx, config.PortfolioConfiguration); err != nil {
		return nil, err
	}
	masterStore := instrumentmaster.NewMemoryRepository()
	if err := masterStore.Put(ctx, config.Master); err != nil {
		return nil, err
	}
	registry := rules.NewRegistry()
	if err := rules.RegisterProduction(registry); err != nil {
		return nil, err
	}
	proposals := &phase3ProposalSource{values: map[strategymodel.ProposalID]strategymodel.TradeProposal{}}
	runtimeStore := runtimememory.NewStore()
	runnerConfig := riskrunner.DefaultConfig()
	runnerConfig.Timeout = config.Timeout
	runner, err := riskrunner.New(riskrunner.Dependencies{
		Proposals: proposals,
		Portfolio: portfolioStore,
		Policies:  phase3PolicySource{value: config.RiskConfiguration.Policy()},
		Masters:   masterStore,
		Runtime:   runtimeStore,
	}, portfolioallocation.Engine{}, registry, runnerConfig)
	if err != nil {
		return nil, err
	}
	portfolioID, _ := portfoliomodel.NewPortfolioID("phase8-m4-live-shadow")
	return &Phase3Gateway{
		config: config, proposals: proposals, portfolio: portfolioStore, store: runtimeStore,
		runner: runner, pipeline: derivatives.ReleasedPipeline{Risk: runner, RiskStore: runtimeStore},
		ID: portfolioID, PolicyID: config.RiskConfiguration.Policy().ID(),
	}, nil
}

func (g *Phase3Gateway) Evaluate(ctx context.Context, request derivatives.ConnectedRequest) (riskmodel.PortfolioRiskDecision, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return riskmodel.PortfolioRiskDecision{}, err
	}
	if request.Mode != derivatives.ConnectedShadow || request.Proposal.IsZero() {
		return riskmodel.PortfolioRiskDecision{}, ErrAuthorization
	}
	if !g.started {
		if err := g.initialize(ctx, request.Proposal, request.At); err != nil {
			return riskmodel.PortfolioRiskDecision{}, err
		}
	}
	g.proposals.values[request.Proposal.ID()] = request.Proposal
	checkpoint, err := g.store.CurrentPortfolioCheckpoint(ctx, g.ID)
	if err != nil {
		return riskmodel.PortfolioRiskDecision{}, err
	}
	request.PortfolioID = g.ID
	request.ExpectedPortfolioRevision = checkpoint.Snapshot.Revision()
	request.RiskPolicyID = g.PolicyID
	request.MasterVersion = g.config.Master.Version()
	result, err := g.pipeline.Process(ctx, request)
	if err != nil {
		return riskmodel.PortfolioRiskDecision{}, err
	}
	if result.Decision.ID().IsZero() {
		return riskmodel.PortfolioRiskDecision{}, ErrNotReady
	}
	return result.Decision, nil
}

func (g *Phase3Gateway) initialize(ctx context.Context, proposal strategymodel.TradeProposal, at time.Time) error {
	configuration := g.config.PortfolioConfiguration
	policy := configuration.AllocationPolicy()
	metadata := proposal.Metadata()
	zero, _ := domain.NewMoney(0, configuration.BaseCurrency().String())
	limit := policy.Limits.MaximumStrategyCapital
	allocationID, _ := portfoliomodel.NewStrategyAllocationID("phase8-m4-reference-candidate")
	allocation, err := portfoliomodel.NewStrategyAllocation(portfoliomodel.StrategyAllocationSpec{
		ID: allocationID, DefinitionID: metadata.DefinitionID, VersionID: metadata.VersionID,
		InstanceID: metadata.InstanceID, InstanceRevisionID: metadata.InstanceRevisionID,
		PolicyID: policy.ID, PolicyVersion: policy.Version, Limit: limit, Deployed: zero,
		Reserved: zero, Remaining: limit, DailyLoss: zero, State: portfoliomodel.StrategyAllocationEnabled,
		EffectiveFrom: policy.EffectiveFrom, EffectiveUntil: policy.EffectiveUntil,
		ConfigurationHash: configuration.Hash(), SchemaVersion: "strategy-allocation/v1",
	})
	if err != nil {
		return fmt.Errorf("create shadow allocation: %w", err)
	}
	total := policy.Limits.TotalCapital
	capital, err := portfoliomodel.NewCapitalState(total, total, zero, zero)
	if err != nil {
		return err
	}
	location := time.FixedZone("IST", 5*60*60+30*60)
	local := at.In(location)
	date, err := domain.NewCivilDate(local.Year(), local.Month(), local.Day())
	if err != nil {
		return err
	}
	reason, _ := portfoliomodel.NewControlReason("PHASE8_M4_SHADOW_HEALTHY")
	killID, _ := portfoliomodel.NewKillSwitchID("phase8-m4-live-shadow")
	killSpec := portfoliomodel.KillSwitchSpec{ID: killID, Scope: portfoliomodel.ScopePortfolio,
		ScopeSubject: g.ID.String(), State: portfoliomodel.KillSwitchInactive, ReasonCode: reason,
		ConfigurationID: configuration.ID(), ConfigurationHash: configuration.Hash(), StateRevision: 1,
		SchemaVersion: "kill-switch/v1"}
	if g.config.KillSwitchActive {
		killSpec.State = portfoliomodel.KillSwitchActive
		killSpec.ActivatedAt = at.Add(-time.Second)
		killSpec.ActivationEvidence, _ = portfoliomodel.NewStateChecksum([]byte("phase8-m4-kill-switch-active"))
	}
	kill, err := portfoliomodel.NewKillSwitch(killSpec)
	if err != nil {
		return err
	}
	circuitID, _ := portfoliomodel.NewCircuitBreakerID("phase8-m4-live-shadow")
	circuit, err := portfoliomodel.NewCircuitBreaker(portfoliomodel.CircuitBreakerSpec{
		ID: circuitID, Scope: portfoliomodel.ScopePortfolio, ScopeSubject: g.ID.String(),
		State: portfoliomodel.CircuitBreakerClosed, ReasonCode: reason,
		ConfigurationID: configuration.ID(), ConfigurationHash: configuration.Hash(),
		StateRevision: 1, SchemaVersion: "circuit-breaker/v1",
	})
	if err != nil {
		return err
	}
	sourceChecksum, _ := portfoliomodel.NewStateChecksum([]byte("phase8-m4-live-shadow-genesis"))
	snapshot, err := portfoliomodel.NewPortfolioSnapshot(portfoliomodel.PortfolioSnapshotSpec{
		SchemaVersion: "portfolio-snapshot/v1", PortfolioID: g.ID, Revision: 1,
		AsOfExchangeTime: at.Add(-time.Nanosecond), GeneratedAt: at.Add(-time.Nanosecond),
		TradingDate: date, BaseCurrency: configuration.BaseCurrency(), State: portfoliomodel.PortfolioEnabled,
		ConfigurationID: configuration.ID(), ConfigurationVersion: configuration.Version(),
		ConfigurationHash: configuration.Hash(), Capital: capital, RealizedPnL: zero, UnrealizedPnL: zero,
		DailyRealizedPnL: zero, DailyUnrealizedPnL: zero, WeeklyRealizedPnL: zero,
		HighWaterMark: total, CurrentEquity: total, StrategyAllocations: []portfoliomodel.StrategyAllocation{allocation},
		KillSwitches: []portfoliomodel.KillSwitch{kill}, CircuitBreakers: []portfoliomodel.CircuitBreaker{circuit},
		SourceStateChecksum: sourceChecksum,
	})
	if err != nil {
		return fmt.Errorf("create shadow portfolio: %w", err)
	}
	checkpoint, err := riskstorage.NewPortfolioCheckpoint(riskstorage.PortfolioCheckpoint{Snapshot: snapshot})
	if err != nil {
		return err
	}
	if _, err = g.store.InitializePortfolio(ctx, checkpoint); err != nil {
		return err
	}
	g.started = true
	return nil
}

func (g *Phase3Gateway) Shutdown(ctx context.Context) error {
	if g == nil || g.runner == nil {
		return nil
	}
	return g.runner.Shutdown(ctx)
}
