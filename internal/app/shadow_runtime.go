package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	brokerzerodha "github.com/bibhuyash/tradeedge/internal/adapters/broker/zerodha"
	"github.com/bibhuyash/tradeedge/internal/adapters/marketdata/calendarfile"
	telegramadapter "github.com/bibhuyash/tradeedge/internal/adapters/notification/telegram"
	shadowcheckpoint "github.com/bibhuyash/tradeedge/internal/adapters/shadowruntime/checkpointfile"
	"github.com/bibhuyash/tradeedge/internal/config"
	"github.com/bibhuyash/tradeedge/internal/derivatives"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	"github.com/bibhuyash/tradeedge/internal/marketdata"
	marketcalendar "github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
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
	"github.com/bibhuyash/tradeedge/internal/qualification"
	qualificationops "github.com/bibhuyash/tradeedge/internal/qualification/opshttp"
	riskconfig "github.com/bibhuyash/tradeedge/internal/risk/config"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	"github.com/bibhuyash/tradeedge/internal/risk/rules"
	"github.com/bibhuyash/tradeedge/internal/shadowruntime"
	shadowops "github.com/bibhuyash/tradeedge/internal/shadowruntime/opshttp"
)

// productionShadow contains read-only market data, strategy, derivatives,
// released Phase 3 risk, qualification, persistence, and notifications. Its
// type graph contains no simulated broker, order system, or accounting owner.
type productionShadow struct {
	bundle        config.RuntimeBundle
	authorization marketvalidation.AuthorizationManifest
	stream        *brokerzerodha.MarketStream
	session       *brokerzerodha.SessionManager
	evaluator     *marketreadiness.Evaluator
	runtime       *shadowruntime.Runtime
	risk          *shadowruntime.Phase3Gateway
	checkpoint    *shadowcheckpoint.Store
	sequence      uint64
	calendar      string
	schedule      *marketcalendar.Schedule
	controls      *control.Controller
	local         *control.LocalServer
	mu            sync.Mutex
	sink          marketdata.ObservationSink
	streamErr     error
	readyOnce     sync.Once
	shutdown      shutdownOperation
}

// shutdownOperation converges repeated and concurrent shutdown requests onto
// one authoritative operation. The first result, including any checkpoint
// conflict or persistence failure, remains authoritative for every caller.
type shutdownOperation struct {
	once sync.Once
	err  error
}

func (o *shutdownOperation) Run(operation func() error) error {
	o.once.Do(func() { o.err = operation() })
	return o.err
}

type shadowQualityObserver struct{ runtime *shadowruntime.Runtime }

func (shadowQualityObserver) Accepted(marketmodel.Event)                 {}
func (o shadowQualityObserver) Quality(record marketmodel.QualityRecord) { o.runtime.Quality(record) }

type shadowEOD struct{ target *productionShadow }

func (e shadowEOD) RunEOD(context.Context) error {
	return e.target.runtime.CloseSession(time.Now().UTC(), true)
}

func runProductionShadow(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	composition, options, err := composeProductionShadow(ctx, cfg)
	if err != nil {
		return fmt.Errorf("compose production SHADOW runtime: %w", err)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		if streamErr := composition.stream.Stream(streamCtx, composition.bundle.Tokens, composition.accept); streamErr != nil && !errors.Is(streamErr, context.Canceled) {
			composition.mu.Lock()
			composition.streamErr = streamErr
			composition.mu.Unlock()
			composition.evaluator.SetProviderAvailable("zerodha", false)
		}
	}()
	return runProductionShadowApplication(ctx, cfg, logger, options)
}

// runProductionShadowApplication is the authoritative owner of application
// shutdown. RunWithOptions invokes the registered TradingRuntime exactly once;
// the production wrapper must not perform an independent cleanup publication.
func runProductionShadowApplication(ctx context.Context, cfg config.Config, logger *slog.Logger, options Options) error {
	return RunWithOptions(ctx, cfg, logger, options)
}

func composeProductionShadow(ctx context.Context, cfg config.Config) (*productionShadow, Options, error) {
	if cfg.ZerodhaMode != config.ZerodhaModeShadow || cfg.TradingMode != config.ModeShadow || !cfg.ZerodhaReadOnly {
		return nil, Options{}, shadowruntime.ErrAuthorization
	}
	bundle, err := config.LoadRuntimeBundle(cfg.RuntimeBundlePath)
	if err != nil || bundle.Manifest.Mode != config.ZerodhaModeShadow || len(bundle.Tokens) > shadowruntime.MaximumSubscriptions {
		return nil, Options{}, errors.Join(shadowruntime.ErrAuthorization, err)
	}
	authorization, err := marketvalidation.LoadAuthorization(cfg.AuthorizationManifestPath)
	if err != nil {
		return nil, Options{}, err
	}
	if err = validateRuntimeAuthorization(authorization, bundle.Checksum, config.ZerodhaModeShadow, time.Now()); err != nil {
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
	streamConfig := brokerzerodha.DefaultMarketStreamConfig()
	streamConfig.MaxSubscriptions = shadowruntime.MaximumSubscriptions
	streamConfig.BufferCapacity = 4096
	streamConfig.ResubscribeInterval = shadowruntime.MinimumRemapInterval
	stream, err := brokerzerodha.NewMarketStream(streamConfig, brokerzerodha.NewWebSocketMarketDialer(), session, brokerzerodha.RealClock{}, nil)
	if err != nil {
		return nil, Options{}, err
	}
	var sender notification.Sender = telegramadapter.Disabled{}
	if enabled, token, chatID := cfg.Telegram(); enabled {
		sender, err = telegramadapter.New(telegramadapter.Config{Enabled: true, Token: token, ChatID: chatID}, &http.Client{}, time.Now)
		if err != nil {
			return nil, Options{}, err
		}
	}
	operational, err := operations.NewSubsystem(sender, nil)
	if err != nil {
		return nil, Options{}, err
	}
	qualificationEngine, err := qualification.New(qualification.DefaultPolicy(), operational)
	if err != nil {
		return nil, Options{}, err
	}
	portfolio, riskConfiguration, err := shadowAuthority(bundle)
	if err != nil {
		return nil, Options{}, err
	}
	riskGateway, err := shadowruntime.NewPhase3Gateway(ctx, shadowruntime.Phase3Config{Master: bundle.Master, PortfolioConfiguration: portfolio, RiskConfiguration: riskConfiguration, Timeout: cfg.RiskTimeout})
	if err != nil {
		return nil, Options{}, err
	}
	spots, policies, err := shadowInstruments(bundle.Master)
	if err != nil {
		return nil, Options{}, err
	}
	now := time.Now().UTC()
	location := time.FixedZone("IST", 5*60*60+30*60)
	runtime, err := shadowruntime.New(shadowruntime.RuntimeConfig{Master: bundle.Master, SpotIDs: spots, Policies: policies, Qualification: qualificationEngine, Risk: riskGateway, Observer: operational, TradingDate: now.In(location).Format("2006-01-02"), StartedAt: now, TelegramAvailable: cfg.TelegramEnabled})
	if err != nil {
		return nil, Options{}, err
	}
	checkpointStore, err := shadowcheckpoint.New(cfg.CheckpointRoot)
	if err != nil {
		return nil, Options{}, err
	}
	composition := &productionShadow{bundle: bundle, authorization: authorization, stream: stream, session: session, evaluator: evaluator, runtime: runtime, risk: riskGateway, checkpoint: checkpointStore, calendar: string(schedule.Version()), schedule: schedule}
	controls, err := control.New(cfg.CheckpointRoot+string(os.PathSeparator)+"shadow-operator-controls.json", shadowEOD{composition}, nil)
	if err != nil {
		return nil, Options{}, err
	}
	composition.controls = controls
	local, err := control.StartLocalServer(cfg.OperatorControlSocket, control.Handler{Controller: controls})
	if err != nil {
		return nil, Options{}, err
	}
	composition.local = local
	if snapshot, generation, loadErr := checkpointStore.Load(ctx); loadErr == nil {
		if generation.CalendarVersion != composition.calendar || generation.ConfigurationChecksum != bundle.Checksum || runtime.Restore(snapshot) != nil {
			return nil, Options{}, checkpointfile.ErrConflict
		}
		composition.sequence = generation.Sequence
	} else if !errors.Is(loadErr, checkpointfile.ErrNotFound) {
		return nil, Options{}, loadErr
	}
	live, err := ingest.NewLiveService(ingest.Normalizer{Resolver: instrumentmaster.Resolver{Repository: repository}, Calendar: schedule}, ingest.ObserverGroup{evaluator, latestObservations, shadowQualityObserver{runtime}}, composition, 2*time.Second, 4096)
	if err != nil {
		return nil, Options{}, err
	}
	composition.sink = live.Accept
	qualificationHandler, _ := qualificationops.New(qualificationEngine)
	shadowHandler, _ := shadowops.New(runtime)
	return composition, Options{MarketReadiness: evaluator, LatestObservations: latestObservations, StrategyOperations: shadowHandler, IntegrationOperations: composition, IntegrationRuntime: composition, TradingRuntime: composition, OperationalOperations: operational.Handler(), NotificationRuntime: operational, QualificationOperations: qualificationHandler, ShadowOperations: shadowHandler}, nil
}

func shadowAuthority(bundle config.RuntimeBundle) (portfolioconfig.PortfolioConfiguration, riskconfig.RiskConfiguration, error) {
	portfolio, err := portfolioconfig.Decode(bundle.Files["portfolio"])
	if err != nil || !portfolio.Enabled() {
		return portfolioconfig.PortfolioConfiguration{}, riskconfig.RiskConfiguration{}, errors.Join(shadowruntime.ErrAuthorization, err)
	}
	descriptors := map[riskmodel.RiskRuleID]riskmodel.RiskRuleDescriptor{}
	for _, rule := range rules.ProductionCatalog() {
		descriptors[rule.Descriptor().ID] = rule.Descriptor()
	}
	riskConfiguration, err := riskconfig.Decode(bundle.Files["risk"], descriptors, portfolio.AllocationPolicy().Limits.ExposureGroups)
	if err != nil || rules.ValidateProductionPolicy(riskConfiguration.Policy()) != nil {
		return portfolioconfig.PortfolioConfiguration{}, riskconfig.RiskConfiguration{}, errors.Join(shadowruntime.ErrAuthorization, err)
	}
	return portfolio, riskConfiguration, nil
}

func shadowInstruments(master instrumentmaster.Master) (map[qualification.Underlying]domain.InstrumentID, map[qualification.Underlying]derivatives.Policy, error) {
	spots := map[qualification.Underlying]domain.InstrumentID{}
	policies := map[qualification.Underlying]derivatives.Policy{}
	for _, underlying := range []qualification.Underlying{qualification.NIFTY, qualification.BANKNIFTY} {
		for _, instrument := range master.Instruments() {
			if instrument.UnderlyingID() == domain.UnderlyingID(underlying) && instrument.Type() == domain.InstrumentIndex {
				if !spots[underlying].IsZero() {
					return nil, nil, shadowruntime.ErrInvalid
				}
				spots[underlying] = instrument.ID()
			}
		}
		policy, err := derivatives.PolicyFor(domain.UnderlyingID(underlying))
		if err != nil {
			return nil, nil, err
		}
		policies[underlying] = policy
	}
	if spots[qualification.NIFTY].IsZero() || spots[qualification.BANKNIFTY].IsZero() {
		return nil, nil, shadowruntime.ErrInvalid
	}
	return spots, policies, nil
}

func (p *productionShadow) accept(ctx context.Context, observation marketdata.Observation) error {
	if p.sink == nil {
		return shadowruntime.ErrNotReady
	}
	return p.sink(ctx, observation)
}

func (p *productionShadow) Process(ctx context.Context, event marketmodel.Event) error {
	quote, ok := event.(marketmodel.QuoteEvent)
	if !ok {
		return nil
	}
	p.readyOnce.Do(func() { _ = p.runtime.SessionReady(quote.ExchangeTime()) })
	regime, active, regimeErr := p.schedule.RegimeAt(ctx, domain.ExchangeNSE, quote.ExchangeTime())
	if regimeErr != nil || !active {
		return errors.Join(shadowruntime.ErrNotReady, regimeErr)
	}
	controls := p.controls.Snapshot()
	err := p.runtime.Process(ctx, quote, "NORMAL_TRADING", regime == marketcalendar.RegimeCAS, controls.NewExposureBlocked)
	if errors.Is(err, shadowruntime.ErrNotReady) || errors.Is(err, shadowruntime.ErrDuplicate) || errors.Is(err, shadowruntime.ErrOutOfOrder) {
		err = nil
	}
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	generation, publishErr := p.checkpoint.Publish(ctx, p.runtime.Snapshot(), p.sequence, p.calendar, p.bundle.Checksum, time.Now().UTC(), false)
	if publishErr == nil {
		p.sequence = generation.Sequence
	}
	return publishErr
}

func (p *productionShadow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/status") {
		w.Header().Set("Allow", http.MethodGet)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"mode": "SHADOW", "read_only": true, "state": p.runtime.Status(), "session": p.session.Snapshot(), "stream": p.stream.Snapshot(), "broker_orders": "DISABLED", "paper_execution": "DISABLED", "real_broker_mutation": "PROHIBITED", "authorization_checksum": p.authorization.Checksum})
}

func (p *productionShadow) Shutdown(ctx context.Context) error {
	return p.shutdown.Run(func() error { return p.shutdownRuntime(ctx) })
}

func (p *productionShadow) shutdownRuntime(ctx context.Context) error {
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
	p.mu.Lock()
	_, checkpointErr := p.checkpoint.Publish(ctx, p.runtime.Snapshot(), p.sequence, p.calendar, p.bundle.Checksum, time.Now().UTC(), true)
	p.mu.Unlock()
	return errors.Join(localErr, controlErr, checkpointErr, p.risk.Shutdown(ctx))
}

var _ = fmt.Sprintf
