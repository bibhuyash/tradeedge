package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/accounting/valuation"
	accountingmemory "github.com/bibhuyash/tradeedge/internal/adapters/accounting/memory"
	"github.com/bibhuyash/tradeedge/internal/adapters/broker/paper"
	brokerzerodha "github.com/bibhuyash/tradeedge/internal/adapters/broker/zerodha"
	executionmemory "github.com/bibhuyash/tradeedge/internal/adapters/execution/memory"
	"github.com/bibhuyash/tradeedge/internal/adapters/marketdata/calendarfile"
	telegramadapter "github.com/bibhuyash/tradeedge/internal/adapters/notification/telegram"
	"github.com/bibhuyash/tradeedge/internal/config"
	"github.com/bibhuyash/tradeedge/internal/domain"
	executionhealth "github.com/bibhuyash/tradeedge/internal/execution/health"
	executionops "github.com/bibhuyash/tradeedge/internal/execution/opshttp"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	"github.com/bibhuyash/tradeedge/internal/marketdata"
	"github.com/bibhuyash/tradeedge/internal/marketdata/ingest"
	"github.com/bibhuyash/tradeedge/internal/marketdata/latest"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	marketreadiness "github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
	"github.com/bibhuyash/tradeedge/internal/marketvalidation"
	"github.com/bibhuyash/tradeedge/internal/notification"
	"github.com/bibhuyash/tradeedge/internal/operations"
	"github.com/bibhuyash/tradeedge/internal/operations/control"
	"github.com/bibhuyash/tradeedge/internal/platform/checkpointfile"
	portfolioconfig "github.com/bibhuyash/tradeedge/internal/portfolio/config"
	riskconfig "github.com/bibhuyash/tradeedge/internal/risk/config"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	"github.com/bibhuyash/tradeedge/internal/risk/rules"
	"github.com/bibhuyash/tradeedge/internal/tradingruntime"
	runtimeops "github.com/bibhuyash/tradeedge/internal/tradingruntime/opshttp"
)

// productionPaper owns the only real-market production composition. It has a
// read-only market connection and a deterministic in-process PAPER broker; no
// live broker port exists in this object graph.
type productionPaper struct {
	bundle        config.RuntimeBundle
	authorization marketvalidation.AuthorizationManifest
	stream        *brokerzerodha.MarketStream
	session       *brokerzerodha.SessionManager
	evaluator     *marketreadiness.Evaluator
	runtime       *tradingruntime.Runtime
	controls      *control.Controller
	local         *control.LocalServer
	paper         *paper.ObservedBroker
	checkpoint    *runtimeCheckpoint
	mu            sync.Mutex
	streamErr     error
	sink          marketdata.ObservationSink
}

type eodProxy struct{ target *productionPaper }

func (p *eodProxy) RunEOD(ctx context.Context) error { return p.target.RunEOD(ctx) }

func runProductionPaper(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	composition, options, err := composeProductionPaper(ctx, cfg)
	if err != nil {
		return fmt.Errorf("compose production PAPER runtime: %w", err)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		err := composition.stream.Stream(streamCtx, composition.bundle.Tokens, composition.accept)
		if err != nil && !errors.Is(err, context.Canceled) {
			composition.mu.Lock()
			composition.streamErr = err
			composition.mu.Unlock()
			composition.evaluator.SetProviderAvailable("zerodha", false)
			composition.runtime.Refresh(context.Background(), composition.dependencies())
		}
	}()
	err = RunWithOptions(ctx, cfg, logger, options)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer shutdownCancel()
	return errors.Join(err, composition.shutdown(shutdownCtx))
}

func composeProductionPaper(ctx context.Context, cfg config.Config) (*productionPaper, Options, error) {
	if cfg.ZerodhaMode != config.ZerodhaModePaper || !cfg.ZerodhaReadOnly {
		return nil, Options{}, tradingruntime.ErrUnsupportedLiveMode
	}
	bundle, err := config.LoadRuntimeBundle(cfg.RuntimeBundlePath)
	if err != nil {
		return nil, Options{}, err
	}
	if err := validateAuthorityConfiguration(bundle); err != nil {
		return nil, Options{}, err
	}
	authorization, err := marketvalidation.LoadAuthorization(cfg.AuthorizationManifestPath)
	if err != nil {
		return nil, Options{}, err
	}
	if err := validateRuntimeAuthorization(authorization, bundle.Checksum, time.Now()); err != nil {
		return nil, Options{}, err
	}
	schedule, err := calendarfile.Load(bundle.CalendarPath)
	if err != nil {
		return nil, Options{}, err
	}
	repository := instrumentmaster.NewMemoryRepository()
	if err = repository.Put(ctx, bundle.Master); err != nil {
		return nil, Options{}, err
	}
	evaluator, err := marketreadiness.New(marketreadiness.RealClock{}, schedule, marketreadiness.DefaultPolicy(), []marketreadiness.Watchlist{bundle.Watchlist})
	if err != nil {
		return nil, Options{}, err
	}
	latestObservations, err := latest.New(bundle.Master, bundle.Watchlist)
	if err != nil {
		return nil, Options{}, err
	}
	zerodhaConfig, err := brokerzerodha.LoadConfig(os.LookupEnv)
	if err != nil || !zerodhaConfig.Enabled {
		return nil, Options{}, errors.Join(brokerzerodha.ErrInvalidConfiguration, err)
	}
	credentials, err := (brokerzerodha.EnvCredentialSource{Lookup: os.LookupEnv}).Load(ctx)
	if err != nil {
		return nil, Options{}, err
	}
	exchanger, err := brokerzerodha.NewHTTPTokenExchanger(zerodhaConfig, nil, brokerzerodha.RealClock{})
	if err != nil {
		return nil, Options{}, err
	}
	session := brokerzerodha.NewSessionManager(credentials, exchanger, brokerzerodha.RealClock{}, nil)
	if err = session.Authenticate(ctx); err != nil {
		return nil, Options{}, err
	}
	stream, err := brokerzerodha.NewMarketStream(brokerzerodha.DefaultMarketStreamConfig(), brokerzerodha.NewWebSocketMarketDialer(), session, brokerzerodha.RealClock{}, nil)
	if err != nil {
		return nil, Options{}, err
	}
	checkpointStore, err := checkpointfile.New(cfg.CheckpointRoot)
	if err != nil {
		return nil, Options{}, err
	}
	checkpoint := &runtimeCheckpoint{store: checkpointStore, calendar: string(schedule.Version()), configuration: bundle.Checksum, clock: func() time.Time { return time.Now().UTC() }}
	manifest, err := checkpoint.restoreOrInitialize(ctx)
	if err != nil {
		return nil, Options{}, err
	}
	var sender notification.Sender = telegramadapter.Disabled{}
	if enabled, botToken, chatID := cfg.Telegram(); enabled {
		sender, err = telegramadapter.New(telegramadapter.Config{Enabled: true, Token: botToken, ChatID: chatID}, &http.Client{}, time.Now)
		if err != nil {
			return nil, Options{}, err
		}
	}
	operational, err := operations.NewSubsystem(sender, nil)
	if err != nil {
		return nil, Options{}, err
	}
	value := &productionPaper{bundle: bundle, authorization: authorization, stream: stream, session: session, evaluator: evaluator, paper: paper.NewObserved(), checkpoint: checkpoint}
	controls, err := control.New(cfg.CheckpointRoot+string(os.PathSeparator)+"operator-controls.json", &eodProxy{target: value}, nil)
	if err != nil {
		return nil, Options{}, err
	}
	value.controls = controls
	local, err := control.StartLocalServer(cfg.OperatorControlSocket, control.Handler{Controller: controls})
	if err != nil {
		return nil, Options{}, err
	}
	value.local = local
	sessionCoordinator, err := tradingruntime.NewSessionCoordinator(schedule, domain.ExchangeNSE)
	if err != nil {
		return nil, Options{}, err
	}
	executionStore := executionmemory.NewStore()
	deps := tradingruntime.PipelineDependencies{Strategy: zeroStrategyStage{}, Risk: sealedRiskStage{}, Execution: &sealedExecutionStage{store: executionStore, broker: value.paper}, Accounting: &sealedAccountingStage{store: accountingmemory.NewDefault()}, Controls: controls, Restorer: checkpoint, Checkpointer: checkpoint, Observer: operational}
	runtimeConfig := tradingruntime.DefaultConfig()
	runtimeConfig.Mode = tradingruntime.ModePaper
	runtimeConfig.OperationTimeout = cfg.StrategyTimeout + cfg.RiskTimeout
	runtimeConfig.DrainTimeout = cfg.ShutdownTimeout
	runtime, err := tradingruntime.New(runtimeConfig, sessionCoordinator, tradingruntime.NewStrategyManager(), deps, nil)
	if err != nil {
		return nil, Options{}, err
	}
	value.runtime, checkpoint.runtime = runtime, runtime
	startErr := runtime.Start(ctx, manifest, value.dependencies())
	if startErr != nil && !errors.Is(startErr, tradingruntime.ErrNotReady) {
		return nil, Options{}, startErr
	}
	live, err := ingest.NewLiveService(ingest.Normalizer{Resolver: instrumentmaster.Resolver{Repository: repository}, Calendar: schedule}, ingest.ObserverGroup{evaluator, latestObservations}, value, 2*time.Second, 4096)
	if err != nil {
		return nil, Options{}, err
	}
	value.sink = live.Accept
	executionOperations := executionops.New(executionops.Dependencies{Repository: executionStore, OMS: executionStore, PaperBroker: value.paper, Coordinator: zeroAuthorityExecutionHealth{}, Reconciliation: emptyReconciliationHealth{at: time.Now().UTC()}, Timeout: 2 * time.Second})
	return value, Options{MarketReadiness: evaluator, LatestObservations: latestObservations, RuntimeReadiness: runtime, RuntimeOperations: runtimeops.New(runtime), TradingRuntime: runtime, ExecutionOperations: executionOperations, IntegrationOperations: value, IntegrationRuntime: value, OperationalOperations: operational.Handler(), NotificationRuntime: operational}, nil
}

func validateRuntimeAuthorization(value marketvalidation.AuthorizationManifest, runtimeBundleChecksum string, now time.Time) error {
	commit, ok := runtimeCommit()
	location := time.FixedZone("IST", 5*60*60+30*60)
	if !ok || value.ApplicationCommit != commit || value.Mode != "PAPER" || value.Scope != marketvalidation.ScopeOperationsOnly ||
		value.Artifacts.RuntimeBundle.Identity != runtimeBundleChecksum ||
		now.Before(value.AuthorizedAt) || !now.Before(value.ExpiresAt) || now.In(location).Format("2006-01-02") != value.TradingDate {
		return errors.New("runtime authorization mismatch")
	}
	return nil
}

func runtimeCommit() (string, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && (len(setting.Value) == 40 || len(setting.Value) == 64) {
			return setting.Value, true
		}
	}
	return "", false
}

func validateAuthorityConfiguration(bundle config.RuntimeBundle) error {
	portfolio, err := portfolioconfig.Decode(bundle.Files["portfolio"])
	if err != nil || !portfolio.Enabled() {
		return errors.Join(errors.New("invalid or disabled portfolio configuration"), err)
	}
	descriptors := map[riskmodel.RiskRuleID]riskmodel.RiskRuleDescriptor{}
	for _, rule := range rules.ProductionCatalog() {
		descriptors[rule.Descriptor().ID] = rule.Descriptor()
	}
	risk, err := riskconfig.Decode(bundle.Files["risk"], descriptors, portfolio.AllocationPolicy().Limits.ExposureGroups)
	if err != nil {
		return err
	}
	if err := rules.ValidateProductionPolicy(risk.Policy()); err != nil {
		return err
	}
	return nil
}

// accept is assigned only after construction, before the stream starts.
func (p *productionPaper) accept(ctx context.Context, observation marketdata.Observation) error {
	if p.sink == nil {
		return errors.New("uninitialized market sink")
	}
	return p.sink(ctx, observation)
}

func (p *productionPaper) Process(ctx context.Context, event marketmodel.Event) error {
	if quote, ok := event.(marketmodel.QuoteEvent); ok {
		if err := p.paper.ObserveQuote(ctx, quote); err != nil {
			return err
		}
	}
	p.runtime.Refresh(ctx, p.dependencies())
	_, err := p.runtime.Process(ctx, event)
	if errors.Is(err, tradingruntime.ErrNotReady) {
		return nil
	}
	return err
}

func (p *productionPaper) dependencies() []tradingruntime.Dependency {
	now := time.Now().UTC()
	ready := func(name, version string) tradingruntime.Dependency {
		return tradingruntime.Dependency{Name: name, Requirement: tradingruntime.Required, State: tradingruntime.HealthReady, Version: version, ObservedAt: now}
	}
	values := []tradingruntime.Dependency{
		ready("configuration", p.authorization.Checksum), ready("calendar", string(p.bundle.Master.Version())),
		ready("instrument_mappings", string(p.bundle.Master.Version())), ready("strategy", "zero-strategy/v1"),
		ready("risk", "phase-4-sealed/v1"), ready("paper_broker", "observed-paper/v1"),
		ready("reconciliation", "empty-authoritative/v1"), ready("valuation", "phase-6/v1"),
		ready("checkpoint_restore", checkpointfile.SchemaVersion), ready("operator_controls", control.SchemaVersion),
	}
	market := p.evaluator.Snapshot(context.Background())
	marketState := tradingruntime.HealthBlocked
	if market.OperationallyReady() {
		marketState = tradingruntime.HealthReady
	}
	values = append(values, tradingruntime.Dependency{Name: "market_data", Requirement: tradingruntime.Required, State: marketState, Reasons: reasonStrings(market.Reasons), Version: market.PolicyVersion, ObservedAt: now})
	session := p.session.Snapshot()
	sessionState := tradingruntime.HealthBlocked
	if session.State == brokerzerodha.SessionAuthenticated {
		sessionState = tradingruntime.HealthReady
	}
	values = append(values, tradingruntime.Dependency{Name: "broker_session", Requirement: tradingruntime.Required, State: sessionState, Reasons: []string{string(session.State)}, ObservedAt: now})
	stream := p.stream.Snapshot()
	streamState := tradingruntime.HealthBlocked
	if stream.State == brokerzerodha.StreamConnected && stream.Subscriptions == len(p.bundle.Tokens) {
		streamState = tradingruntime.HealthReady
	}
	values = append(values, tradingruntime.Dependency{Name: "broker_stream", Requirement: tradingruntime.Required, State: streamState, Reasons: []string{string(stream.State)}, ObservedAt: now})
	return values
}

func reasonStrings(values []marketreadiness.ReasonCode) []string {
	result := make([]string, len(values))
	for i, v := range values {
		result[i] = string(v)
	}
	return result
}

func (p *productionPaper) RunEOD(ctx context.Context) error {
	if p.runtime == nil {
		return tradingruntime.ErrNotReady
	}
	return p.runtime.Shutdown(ctx)
}

func (p *productionPaper) Shutdown(ctx context.Context) error { return p.shutdown(ctx) }
func (p *productionPaper) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/status") {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	session, stream := p.session.Snapshot(), p.stream.Snapshot()
	state := "NOT_READY"
	now := time.Now()
	if session.State == brokerzerodha.SessionAuthenticated && stream.State == brokerzerodha.StreamConnected && !now.Before(p.authorization.AuthorizedAt) && now.Before(p.authorization.ExpiresAt) {
		state = "READY"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"mode": "PAPER", "state": state, "read_only": true, "session_state": session.State, "mutation_permitted": false, "mapping_version": string(p.bundle.Master.Version()), "stream": stream, "unknown_orders": 0, "reconciliation_blocked": false, "authorization_checksum": p.authorization.Checksum})
}
func (p *productionPaper) shutdown(ctx context.Context) error {
	p.stream.Shutdown()
	p.session.Shutdown()
	var localErr error
	if p.local != nil {
		localErr = p.local.Shutdown(ctx)
	}
	var controlErr error
	if p.controls != nil {
		controlErr = p.controls.Shutdown(ctx)
	}
	return errors.Join(localErr, controlErr)
}

type zeroStrategyStage struct{}

func (zeroStrategyStage) Evaluate(context.Context, marketmodel.Event, []tradingruntime.StrategySnapshot) ([]tradingruntime.Proposal, error) {
	return nil, nil
}

type sealedRiskStage struct{}

func (sealedRiskStage) Decide(context.Context, tradingruntime.Proposal) (riskmodel.PortfolioRiskDecision, error) {
	return riskmodel.PortfolioRiskDecision{}, errors.New("zero-strategy PAPER composition has no proposal authority")
}
func (sealedRiskStage) UpdateFinancial(context.Context, valuation.PortfolioFinancialSnapshot) error {
	return errors.New("zero-strategy PAPER composition has no financial mutation authority")
}

type sealedExecutionStage struct {
	store  *executionmemory.Store
	broker *paper.ObservedBroker
}

type zeroAuthorityExecutionHealth struct{}

func (zeroAuthorityExecutionHealth) Health() executionhealth.Coordinator {
	return executionhealth.Coordinator{Available: true, MaximumPlans: 0}
}

type emptyReconciliationHealth struct{ at time.Time }

func (h emptyReconciliationHealth) Health() executionhealth.Reconciliation {
	return executionhealth.Reconciliation{Available: true, LastAttempt: h.at, LastSuccess: h.at, IssueCounts: map[string]int{}}
}

func (*sealedExecutionStage) Execute(context.Context, riskmodel.PortfolioRiskDecision) (tradingruntime.ExecutionResult, error) {
	return tradingruntime.ExecutionResult{}, errors.New("zero-strategy PAPER composition has no execution authority")
}
func (s *sealedExecutionStage) Health(context.Context) tradingruntime.Dependency {
	state := tradingruntime.HealthBlocked
	if s.store != nil && s.broker != nil && s.store.Health().Available && s.broker.Health().Available {
		state = tradingruntime.HealthReady
	}
	return tradingruntime.Dependency{Name: "oms", Requirement: tradingruntime.Required, State: state, ObservedAt: time.Now().UTC()}
}

type sealedAccountingStage struct{ store *accountingmemory.Store }

func (*sealedAccountingStage) Ingest(context.Context, tradingruntime.ExecutionResult) (valuation.PortfolioFinancialSnapshot, error) {
	return valuation.PortfolioFinancialSnapshot{}, errors.New("zero-strategy PAPER composition has no accounting mutation authority")
}
func (s *sealedAccountingStage) Health(context.Context) tradingruntime.Dependency {
	state := tradingruntime.HealthBlocked
	if s.store != nil {
		state = tradingruntime.HealthReady
	}
	return tradingruntime.Dependency{Name: "accounting", Requirement: tradingruntime.Required, State: state, ObservedAt: time.Now().UTC()}
}

type runtimeCheckpoint struct {
	store                   *checkpointfile.Store
	calendar, configuration string
	clock                   func() time.Time
	mu                      sync.Mutex
	sequence                uint64
	runtime                 *tradingruntime.Runtime
}

func (c *runtimeCheckpoint) restoreOrInitialize(ctx context.Context) (tradingruntime.CheckpointManifest, error) {
	value, err := c.store.Load(ctx)
	if errors.Is(err, checkpointfile.ErrNotFound) {
		value, err = c.publish(ctx, false, 0)
	}
	if err != nil {
		return tradingruntime.CheckpointManifest{}, err
	}
	c.sequence = value.Sequence
	if value.CalendarVersion != c.calendar || value.ConfigurationChecksum != c.configuration {
		return tradingruntime.CheckpointManifest{}, checkpointfile.ErrConflict
	}
	return generationManifest(value)
}
func (c *runtimeCheckpoint) Restore(ctx context.Context, manifest tradingruntime.CheckpointManifest) error {
	value, err := c.store.Load(ctx)
	if err != nil {
		return err
	}
	rebuilt, err := generationManifest(value)
	if err != nil || rebuilt.Checksum != manifest.Checksum {
		return checkpointfile.ErrCorrupt
	}
	return nil
}
func (c *runtimeCheckpoint) Checkpoint(ctx context.Context) (tradingruntime.CheckpointManifest, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, err := c.publish(ctx, true, c.sequence)
	if err != nil {
		return tradingruntime.CheckpointManifest{}, err
	}
	c.sequence = value.Sequence
	return generationManifest(value)
}
func (c *runtimeCheckpoint) publish(ctx context.Context, clean bool, expected uint64) (checkpointfile.Generation, error) {
	state := map[string]any{"state": "GENESIS", "strategies": 0, "orders": 0, "fills": 0}
	if c.runtime != nil {
		snapshot := c.runtime.Snapshot()
		state["state"], state["restored"] = snapshot.State, snapshot.Restored
	}
	raw, _ := json.Marshal(state)
	return c.store.Publish(ctx, checkpointfile.Generation{SchemaVersion: checkpointfile.SchemaVersion, Sequence: expected + 1, Mode: "PAPER", CalendarVersion: c.calendar, ConfigurationChecksum: c.configuration, CreatedAt: c.clock(), CleanShutdown: clean, Components: []checkpointfile.Component{{Name: "paper_runtime", Revision: strconv.FormatUint(expected+1, 10), Data: raw}}}, expected)
}
func generationManifest(value checkpointfile.Generation) (tradingruntime.CheckpointManifest, error) {
	heads := make([]tradingruntime.CheckpointHead, len(value.Components))
	for i, v := range value.Components {
		heads[i] = tradingruntime.CheckpointHead{Subsystem: v.Name, Revision: v.Revision, Checksum: v.Checksum}
	}
	return tradingruntime.NewCheckpointManifest(tradingruntime.CheckpointManifest{SchemaVersion: tradingruntime.CheckpointSchemaVersion, Mode: tradingruntime.ModePaper, CalendarVersion: value.CalendarVersion, Configuration: value.ConfigurationChecksum, Session: tradingruntime.SessionStarting, Heads: heads, CreatedAt: value.CreatedAt, CleanShutdown: value.CleanShutdown})
}
