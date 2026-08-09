package marketvalidation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/adapters/marketdata/calendarfile"
	"github.com/bibhuyash/tradeedge/internal/domain"
	portfolioconfig "github.com/bibhuyash/tradeedge/internal/portfolio/config"
	riskconfig "github.com/bibhuyash/tradeedge/internal/risk/config"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	"github.com/bibhuyash/tradeedge/internal/risk/rules"
)

const ReadinessSchemaVersion = "market-validation-readiness/v1"

type ReadinessConfig struct {
	SchemaVersion    string `json:"schema_version"`
	TradingDate      string `json:"trading_date"`
	ExpectedCommit   string `json:"expected_commit"`
	Mode             string `json:"mode"`
	BaseURL          string `json:"base_url"`
	EvidenceRoot     string `json:"evidence_root"`
	PortfolioID      string `json:"portfolio_id"`
	Scope            Scope  `json:"scope"`
	TelegramRequired bool   `json:"telegram_required"`
	Files            struct {
		Calendar         string `json:"calendar"`
		InstrumentMaster string `json:"instrument_master"`
		Watchlist        string `json:"watchlist"`
		Strategies       string `json:"strategies"`
		Portfolio        string `json:"portfolio"`
		Risk             string `json:"risk"`
		TelegramCheck    string `json:"telegram_check,omitempty"`
	} `json:"files"`
}

type ReadinessCheck struct {
	Name         string   `json:"name"`
	Passed       bool     `json:"passed"`
	Reasons      []string `json:"reasons"`
	HTTPStatus   int      `json:"http_status,omitempty"`
	EvidenceHash string   `json:"evidence_sha256,omitempty"`
}

type ReadinessReport struct {
	SchemaVersion         string            `json:"schema_version"`
	TradingDate           string            `json:"trading_date"`
	Mode                  string            `json:"mode"`
	Scope                 Scope             `json:"scope"`
	Commit                string            `json:"commit"`
	CheckedAt             time.Time         `json:"checked_at"`
	EvidenceRoot          string            `json:"evidence_root"`
	ConfigurationHashes   map[string]string `json:"configuration_hashes"`
	Checks                []ReadinessCheck  `json:"checks"`
	Ready                 bool              `json:"ready"`
	LiveTradingAuthorized bool              `json:"live_trading_authorized"`
}

func DecodeReadinessConfig(raw []byte) (ReadinessConfig, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value ReadinessConfig
	if err := decoder.Decode(&value); err != nil {
		return ReadinessConfig{}, fmt.Errorf("decode readiness configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ReadinessConfig{}, ErrInvalidRecord
	}
	if value.SchemaVersion != ReadinessSchemaVersion || !validCommit(value.ExpectedCommit) ||
		(value.Mode != "PAPER" && value.Mode != "SHADOW") ||
		(value.Scope != ScopeOperationsOnly && value.Scope != ScopeFullPipeline) || value.PortfolioID == "" {
		return ReadinessConfig{}, ErrInvalidRecord
	}
	if _, err := time.Parse("2006-01-02", value.TradingDate); err != nil {
		return ReadinessConfig{}, ErrInvalidRecord
	}
	parsed, err := url.Parse(value.BaseURL)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" ||
		(parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback(parsed.Hostname()))) {
		return ReadinessConfig{}, ErrInvalidRecord
	}
	if value.EvidenceRoot == "" || strings.Contains(strings.ToLower(value.EvidenceRoot), "secret") ||
		strings.Contains(strings.ToLower(value.EvidenceRoot), "credential") || strings.Contains(strings.ToLower(value.EvidenceRoot), "token") {
		return ReadinessConfig{}, ErrInvalidRecord
	}
	for _, path := range configPaths(value) {
		if strings.TrimSpace(path) == "" {
			return ReadinessConfig{}, ErrInvalidRecord
		}
	}
	if value.TelegramRequired && strings.TrimSpace(value.Files.TelegramCheck) == "" {
		return ReadinessConfig{}, ErrInvalidRecord
	}
	return value, nil
}

func RunReadiness(ctx context.Context, cfg ReadinessConfig, repoRoot string, client *http.Client, now func() time.Time) (ReadinessReport, error) {
	if now == nil {
		now = time.Now
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	report := ReadinessReport{SchemaVersion: ReadinessSchemaVersion, TradingDate: cfg.TradingDate, Mode: cfg.Mode, Scope: cfg.Scope, CheckedAt: now().UTC(), EvidenceRoot: filepath.ToSlash(cfg.EvidenceRoot), ConfigurationHashes: map[string]string{}, LiveTradingAuthorized: false, Ready: true}
	commit, commitErr := currentCommit(ctx, repoRoot)
	report.Commit = commit
	report.add("release_commit", commitErr == nil && commit == cfg.ExpectedCommit, reason(commitErr, commit != cfg.ExpectedCommit, "COMMIT_MISMATCH"), 0, "")
	if err := os.MkdirAll(cfg.EvidenceRoot, 0o750); err != nil {
		report.add("evidence_root", false, []string{"EVIDENCE_ROOT_UNAVAILABLE"}, 0, "")
	} else {
		report.add("evidence_root", true, nil, 0, "")
	}
	names := []string{"calendar", "instrument_master", "watchlist", "strategies", "portfolio", "risk"}
	validationErrors := validateConfigurationSet(ctx, cfg)
	for index, path := range configPaths(cfg) {
		hash, err := hashJSONFile(path)
		if err == nil {
			err = validationErrors[names[index]]
		}
		passed := err == nil
		if passed {
			report.ConfigurationHashes[names[index]] = hash
		}
		report.add("configuration_"+names[index], passed, reason(err, false, ""), 0, hash)
	}
	if cfg.TelegramRequired {
		hash, err := validateTelegramEvidence(cfg.Files.TelegramCheck, cfg.TradingDate, cfg.Mode)
		if err == nil {
			report.ConfigurationHashes["telegram_check"] = hash
		}
		report.add("telegram_delivery_evidence", err == nil, reason(err, false, ""), 0, hash)
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	portfolio := url.QueryEscape(cfg.PortfolioID)
	checks := []struct {
		name, path string
		validate   func(map[string]any) []string
	}{
		{"process_health", "/healthz", func(v map[string]any) []string { return requireString(v, "status", "ok") }},
		{"global_readiness", "/readyz", validateReady},
		{"runtime", "/api/v1/runtime/status", func(v map[string]any) []string { return validateRuntime(v, cfg) }},
		{"zerodha", "/api/v1/integrations/zerodha/health", func(v map[string]any) []string { return validateZerodha(v, cfg.Mode) }},
		{"market_data", "/api/v1/market-data/readiness", validateMarketData},
		{"strategy", "/api/v1/strategy/instances", func(v map[string]any) []string { return validateStrategies(v, cfg.Scope) }},
		{"risk_configuration", "/api/v1/risk/configuration?portfolio=" + portfolio, validateRiskConfiguration},
		{"kill_switch", "/api/v1/risk/kill-switch?portfolio=" + portfolio, validateKillSwitch},
		{"circuit_breaker", "/api/v1/risk/circuit-breaker?portfolio=" + portfolio, validateCircuitBreaker},
		{"execution", "/api/v1/execution/health", validateExecution},
		{"financial", "/api/v1/financial/readiness?portfolio_id=" + portfolio, validateFinancial},
		{"notification_health", "/api/v1/notifications/health", validateNotificationHealth},
	}
	if cfg.TelegramRequired {
		checks = append(checks, struct {
			name, path string
			validate   func(map[string]any) []string
		}{"telegram", "/api/v1/notifications/providers/telegram", validateTelegram})
	}
	for _, check := range checks {
		body, status, err := get(ctx, client, base+check.path)
		reasons := reason(err, status != http.StatusOK, "HTTP_NOT_READY")
		var payload map[string]any
		if err == nil && status == http.StatusOK {
			if decodeErr := json.Unmarshal(body, &payload); decodeErr != nil {
				reasons = append(reasons, "MALFORMED_RESPONSE")
			} else {
				reasons = append(reasons, check.validate(payload)...)
			}
		}
		sum := sha256.Sum256(body)
		hash := ""
		if len(body) > 0 {
			hash = hex.EncodeToString(sum[:])
		}
		report.add(check.name, len(reasons) == 0, reasons, status, hash)
	}
	sort.Slice(report.Checks, func(i, j int) bool { return report.Checks[i].Name < report.Checks[j].Name })
	return report, nil
}

func (r *ReadinessReport) add(name string, passed bool, reasons []string, status int, hash string) {
	if !passed {
		r.Ready = false
	}
	sort.Strings(reasons)
	r.Checks = append(r.Checks, ReadinessCheck{Name: name, Passed: passed, Reasons: reasons, HTTPStatus: status, EvidenceHash: hash})
}

func configPaths(c ReadinessConfig) []string {
	return []string{c.Files.Calendar, c.Files.InstrumentMaster, c.Files.Watchlist, c.Files.Strategies, c.Files.Portfolio, c.Files.Risk}
}

func hashJSONFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 || !json.Valid(raw) {
		return "", ErrInvalidRecord
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func validateConfigurationSet(ctx context.Context, cfg ReadinessConfig) map[string]error {
	result := map[string]error{}
	schedule, err := calendarfile.Load(cfg.Files.Calendar)
	if err == nil {
		parsed, _ := time.Parse("2006-01-02", cfg.TradingDate)
		date, dateErr := domain.NewCivilDate(parsed.Year(), parsed.Month(), parsed.Day())
		if dateErr != nil {
			err = dateErr
		} else {
			_, err = schedule.Day(ctx, domain.ExchangeNSE, date)
		}
	}
	result["calendar"] = err
	_, masterErr := validateWatchlistAndMaster(cfg.Files.Watchlist, cfg.Files.InstrumentMaster, cfg.TradingDate)
	result["watchlist"], result["instrument_master"] = masterErr, masterErr
	result["strategies"] = validateStrategyConfiguration(cfg.Files.Strategies, cfg.Scope)
	portfolioRaw, portfolioErr := os.ReadFile(cfg.Files.Portfolio)
	portfolio, decodePortfolioErr := portfolioconfig.Decode(portfolioRaw)
	if portfolioErr != nil {
		decodePortfolioErr = portfolioErr
	}
	result["portfolio"] = decodePortfolioErr
	riskRaw, riskErr := os.ReadFile(cfg.Files.Risk)
	if riskErr == nil && decodePortfolioErr != nil {
		riskErr = ErrInvalidRecord
	} else if riskErr == nil {
		descriptors := make(map[riskmodel.RiskRuleID]riskmodel.RiskRuleDescriptor)
		for _, rule := range rules.ProductionCatalog() {
			descriptors[rule.Descriptor().ID] = rule.Descriptor()
		}
		configuration, decodeErr := riskconfig.Decode(riskRaw, descriptors, portfolio.AllocationPolicy().Limits.ExposureGroups)
		if decodeErr == nil {
			decodeErr = rules.ValidateProductionPolicy(configuration.Policy())
		}
		riskErr = decodeErr
	}
	result["risk"] = riskErr
	return result
}

func validateWatchlistAndMaster(watchlistPath, masterPath, tradingDate string) (map[string]bool, error) {
	type requirement struct {
		Provider      string `json:"provider"`
		InstrumentKey string `json:"instrument_key"`
		Exchange      string `json:"exchange"`
		Segment       string `json:"segment"`
		EventKind     string `json:"event_kind"`
		Required      bool   `json:"required"`
	}
	var watchlist struct {
		SchemaVersion int           `json:"schema_version"`
		ID            string        `json:"id"`
		Requirements  []requirement `json:"requirements"`
	}
	if err := decodeStrictPath(watchlistPath, &watchlist); err != nil || watchlist.SchemaVersion != 1 || watchlist.ID == "" || len(watchlist.Requirements) == 0 || len(watchlist.Requirements) > 4 {
		return nil, ErrInvalidRecord
	}
	keys := map[string]bool{}
	for _, item := range watchlist.Requirements {
		if item.Provider != "zerodha" || item.InstrumentKey == "" || item.Exchange != "NSE" || item.EventKind != "QUOTE" || !item.Required || keys[item.InstrumentKey] {
			return nil, ErrInvalidRecord
		}
		keys[item.InstrumentKey] = true
	}
	var master struct {
		AsOf        time.Time `json:"as_of"`
		Instruments []struct {
			Key string `json:"key"`
		} `json:"instruments"`
		Mappings []struct {
			Provider      string    `json:"provider"`
			Token         string    `json:"token"`
			InstrumentKey string    `json:"instrument_key"`
			ValidFrom     time.Time `json:"valid_from"`
			ValidUntil    time.Time `json:"valid_until"`
		} `json:"mappings"`
	}
	raw, readErr := os.ReadFile(masterPath)
	if readErr != nil || json.Unmarshal(raw, &master) != nil || master.AsOf.IsZero() {
		return nil, ErrInvalidRecord
	}
	date, _ := time.Parse("2006-01-02", tradingDate)
	at := date.Add(9*time.Hour + 15*time.Minute - 5*time.Hour - 30*time.Minute)
	for key := range keys {
		instrumentFound, mappingFound := false, false
		for _, instrument := range master.Instruments {
			instrumentFound = instrumentFound || instrument.Key == key
		}
		for _, mapping := range master.Mappings {
			if mapping.Provider == "zerodha" && mapping.InstrumentKey == key && mapping.Token != "" && !at.Before(mapping.ValidFrom) && at.Before(mapping.ValidUntil) {
				if mappingFound {
					return nil, ErrInvalidRecord
				}
				mappingFound = true
			}
		}
		if !instrumentFound || !mappingFound {
			return nil, ErrInvalidRecord
		}
	}
	return keys, nil
}

func validateStrategyConfiguration(path string, scope Scope) error {
	var value struct {
		SchemaVersion string `json:"schema_version"`
		Instances     []struct {
			ID             string `json:"id"`
			Classification string `json:"classification"`
			Enabled        bool   `json:"enabled"`
			CASPolicy      string `json:"cas_policy"`
		} `json:"instances"`
	}
	if err := decodeStrictPath(path, &value); err != nil || value.SchemaVersion != "market-validation-strategies/v1" {
		return ErrInvalidRecord
	}
	approved := 0
	for _, item := range value.Instances {
		if item.ID == "" || (item.CASPolicy != "CAS_SAFE" && item.CASPolicy != "CAS_RESTRICTED" && item.CASPolicy != "CAS_DISABLED") {
			return ErrInvalidRecord
		}
		if item.Enabled && item.Classification == "PRODUCTION_CANDIDATE" {
			approved++
		}
		if item.Enabled && item.Classification != "PRODUCTION_CANDIDATE" && scope == ScopeFullPipeline {
			return ErrInvalidRecord
		}
	}
	if scope == ScopeFullPipeline && approved == 0 {
		return ErrInvalidRecord
	}
	return nil
}

func decodeStrictPath(path string, destination any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidRecord
	}
	return nil
}

func currentCommit(ctx context.Context, root string) (string, error) {
	command := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	command.Dir = root
	raw, err := command.Output()
	return strings.TrimSpace(string(raw)), err
}

func get(ctx context.Context, client *http.Client, endpoint string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return body, response.StatusCode, err
}

func validateReady(v map[string]any) []string {
	reasons := requireString(v, "status", "ready")
	if value, _ := v["trading_permitted"].(bool); !value {
		reasons = append(reasons, "TRADING_NOT_PERMITTED")
	}
	return reasons
}

func validateRuntime(v map[string]any, cfg ReadinessConfig) []string {
	reasons := requireString(v, "mode", cfg.Mode)
	reasons = append(reasons, requireString(v, "state", "RUNNING")...)
	if restored, _ := v["restored"].(bool); !restored {
		reasons = append(reasons, "RUNTIME_NOT_RESTORED")
	}
	readiness, _ := v["readiness"].(map[string]any)
	if ready, _ := readiness["ready"].(bool); !ready {
		reasons = append(reasons, "RUNTIME_NOT_READY")
	}
	controls, _ := v["controls"].(map[string]any)
	if blocked, _ := controls["global_blocked"].(bool); blocked {
		reasons = append(reasons, "GLOBAL_CONTROL_BLOCKED")
	}
	if cfg.Scope == ScopeFullPipeline {
		strategies, _ := v["strategies"].([]any)
		active := false
		for _, item := range strategies {
			value, _ := item.(map[string]any)
			active = active || value["state"] == "ACTIVE"
		}
		if !active {
			reasons = append(reasons, "NO_ACTIVE_STRATEGY")
		}
	}
	return reasons
}

func validateZerodha(v map[string]any, mode string) []string {
	reasons := requireString(v, "mode", mode)
	reasons = append(reasons, requireString(v, "state", "READY")...)
	reasons = append(reasons, requireString(v, "session_state", "AUTHENTICATED")...)
	if mutation, _ := v["mutation_permitted"].(bool); mutation {
		reasons = append(reasons, "BROKER_MUTATION_PERMITTED")
	}
	if value, _ := v["mapping_version"].(string); strings.TrimSpace(value) == "" {
		reasons = append(reasons, "MAPPING_VERSION_MISSING")
	}
	stream, _ := v["stream"].(map[string]any)
	reasons = append(reasons, requireString(stream, "state", "CONNECTED")...)
	if blocked, _ := v["reconciliation_blocked"].(bool); blocked {
		reasons = append(reasons, "RECONCILIATION_BLOCKED")
	}
	if count, _ := v["unknown_orders"].(float64); count != 0 {
		reasons = append(reasons, "UNKNOWN_ORDERS")
	}
	return reasons
}

func validateMarketData(v map[string]any) []string {
	reasons := requireString(v, "state", "READY")
	if ready, _ := v["trading_permitted"].(bool); !ready {
		reasons = append(reasons, "MARKET_DATA_NOT_TRADING_READY")
	}
	if version, _ := v["calendar_version"].(string); version == "" {
		reasons = append(reasons, "CALENDAR_VERSION_MISSING")
	}
	return reasons
}

func validateStrategies(v map[string]any, scope Scope) []string {
	items, ok := v["items"].([]any)
	if !ok {
		// Current strategy handler returns a JSON array through respond().
		items, ok = v["_items"].([]any)
	}
	if scope == ScopeFullPipeline && (!ok || len(items) == 0) {
		return []string{"STRATEGY_CONFIGURATION_MISSING"}
	}
	return nil
}

func validateRiskConfiguration(v map[string]any) []string {
	rules, _ := v["rules"].([]any)
	if len(rules) != 10 {
		return []string{"PRODUCTION_RISK_CATALOG_INCOMPLETE"}
	}
	return nil
}

func validateKillSwitch(v map[string]any) []string {
	items, _ := v["items"].([]any)
	if len(items) == 0 {
		return []string{"KILL_SWITCH_MISSING"}
	}
	for _, item := range items {
		value, _ := item.(map[string]any)
		if value["state"] != "INACTIVE" {
			return []string{"KILL_SWITCH_NOT_INACTIVE"}
		}
	}
	return nil
}

func validateCircuitBreaker(v map[string]any) []string {
	items, _ := v["items"].([]any)
	if len(items) == 0 {
		return []string{"CIRCUIT_BREAKER_MISSING"}
	}
	for _, item := range items {
		value, _ := item.(map[string]any)
		if value["state"] != "CLOSED" {
			return []string{"CIRCUIT_BREAKER_NOT_CLOSED"}
		}
	}
	return nil
}

func validateExecution(v map[string]any) []string {
	if state, _ := v["state"].(string); state != "HEALTHY" {
		return []string{"EXECUTION_NOT_READY"}
	}
	if unknown, _ := v["unknown_orders"].(float64); unknown != 0 {
		return []string{"UNKNOWN_ORDERS"}
	}
	return nil
}

func validateFinancial(v map[string]any) []string {
	status, _ := v["status"].(string)
	if status != "COMPLETE" {
		return []string{"FINANCIAL_STATE_NOT_READY"}
	}
	return nil
}

func validateNotificationHealth(v map[string]any) []string {
	if accepting, exists := v["accepting"].(bool); exists && !accepting {
		return []string{"NOTIFICATION_DISPATCHER_NOT_ACCEPTING"}
	}
	return nil
}

func validateTelegram(v map[string]any) []string {
	return requireString(v, "state", "READY")
}

func validateTelegramEvidence(path, date, mode string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var value struct {
		SchemaVersion string `json:"schema_version"`
		TradingDate   string `json:"trading_date"`
		Mode          string `json:"mode"`
		Delivered     bool   `json:"delivered"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || value.SchemaVersion != "market-validation-telegram-check/v1" || value.TradingDate != date || value.Mode != mode || !value.Delivered {
		return "", ErrInvalidRecord
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func requireString(v map[string]any, key, expected string) []string {
	if value, _ := v[key].(string); value != expected {
		return []string{strings.ToUpper(key) + "_MISMATCH"}
	}
	return nil
}

func reason(err error, condition bool, code string) []string {
	if err != nil {
		return []string{"UNAVAILABLE"}
	}
	if condition && code != "" {
		return []string{code}
	}
	return nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
