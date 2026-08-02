package opshttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	portfolioconfig "github.com/bibhuyash/tradeedge/internal/portfolio/config"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskconfig "github.com/bibhuyash/tradeedge/internal/risk/config"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	riskstorage "github.com/bibhuyash/tradeedge/internal/risk/storage"
)

type Repository interface {
	CurrentPortfolioCheckpoint(context.Context, portfoliomodel.PortfolioID) (riskstorage.PortfolioCheckpoint, error)
	RecentDecisions(context.Context, portfoliomodel.PortfolioID, int) ([]riskmodel.PortfolioRiskDecision, error)
}

type ConfigurationSource interface {
	CurrentPortfolioConfiguration(context.Context, portfoliomodel.PortfolioID) (portfolioconfig.PortfolioConfiguration, error)
	CurrentRiskConfiguration(context.Context, portfoliomodel.PortfolioID) (riskconfig.RiskConfiguration, error)
}

type HealthSource interface {
	Health() (bool, int, int, int, time.Duration)
}

type Handler struct {
	repository Repository
	configs    ConfigurationSource
	runner     HealthSource
	timeout    time.Duration
}

func New(repository Repository, configs ConfigurationSource, runner HealthSource, timeout time.Duration) http.Handler {
	if timeout <= 0 || timeout > 10*time.Second {
		timeout = 2 * time.Second
	}
	return Handler{repository: repository, configs: configs, runner: runner, timeout: timeout}
}

func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		write(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	if handler.repository == nil {
		write(writer, http.StatusServiceUnavailable, map[string]string{"error": "repository_unavailable"})
		return
	}
	portfolio, err := portfoliomodel.ParsePortfolioID(request.URL.Query().Get("portfolio"))
	if err != nil {
		write(writer, http.StatusBadRequest, map[string]string{"error": "invalid_portfolio"})
		return
	}
	limit, ok := parseLimit(request)
	if !ok {
		write(writer, http.StatusBadRequest, map[string]string{"error": "invalid_limit"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.timeout)
	defer cancel()
	checkpoint, err := handler.repository.CurrentPortfolioCheckpoint(ctx, portfolio)
	if err != nil {
		handler.repositoryError(writer, err)
		return
	}
	path := strings.TrimSuffix(request.URL.Path, "/")
	switch path {
	case "/api/v1/risk/portfolio/snapshot":
		handler.snapshot(writer, checkpoint)
	case "/api/v1/risk/capital-usage":
		handler.capital(writer, checkpoint, limit)
	case "/api/v1/risk/decisions", "/api/v1/risk/violations", "/api/v1/risk/rules":
		decisions, readErr := handler.repository.RecentDecisions(ctx, portfolio, limit)
		if readErr != nil {
			handler.repositoryError(writer, readErr)
			return
		}
		handler.decisionView(writer, path, decisions, limit)
	case "/api/v1/risk/kill-switch":
		write(writer, http.StatusOK, map[string]any{"items": killSwitches(checkpoint.Snapshot.Spec().KillSwitches, limit), "limit": limit})
	case "/api/v1/risk/circuit-breaker":
		write(writer, http.StatusOK, map[string]any{"items": circuitBreakers(checkpoint.Snapshot.Spec().CircuitBreakers, limit), "limit": limit})
	case "/api/v1/risk/configuration":
		handler.configuration(ctx, writer, portfolio)
	case "/api/v1/risk/runner":
		handler.runnerHealth(writer)
	default:
		write(writer, http.StatusNotFound, map[string]string{"error": "not_found"})
	}
}

func (handler Handler) snapshot(writer http.ResponseWriter, checkpoint riskstorage.PortfolioCheckpoint) {
	spec := checkpoint.Snapshot.Spec()
	write(writer, http.StatusOK, map[string]any{
		"portfolio_id": spec.PortfolioID.String(), "snapshot_id": checkpoint.Snapshot.ID().String(),
		"revision": spec.Revision, "state": spec.State, "trading_date": spec.TradingDate.String(),
		"as_of_exchange_time": spec.AsOfExchangeTime, "generated_at": spec.GeneratedAt,
		"configuration_id": spec.ConfigurationID.String(), "configuration_hash": spec.ConfigurationHash.String(),
		"source_checksum": spec.SourceStateChecksum.String(), "checkpoint_checksum": checkpoint.CheckpointChecksum.String(),
		"allocation_count": len(spec.StrategyAllocations), "exposure_count": len(spec.Exposures),
		"kill_switch_count": len(spec.KillSwitches), "circuit_breaker_count": len(spec.CircuitBreakers),
	})
}

func (handler Handler) capital(writer http.ResponseWriter, checkpoint riskstorage.PortfolioCheckpoint, limit int) {
	spec := checkpoint.Snapshot.Spec()
	allocations := spec.StrategyAllocations
	if len(allocations) > limit {
		allocations = allocations[:limit]
	}
	items := make([]map[string]any, len(allocations))
	for index, allocation := range allocations {
		value := allocation.Spec()
		items[index] = map[string]any{"allocation_id": value.ID.String(), "state": value.State,
			"limit_minor": value.Limit.MinorUnits(), "deployed_minor": value.Deployed.MinorUnits(),
			"reserved_minor": value.Reserved.MinorUnits(), "remaining_minor": value.Remaining.MinorUnits()}
	}
	capital := spec.Capital
	write(writer, http.StatusOK, map[string]any{"currency": spec.BaseCurrency,
		"total_minor": capital.Total.MinorUnits(), "available_minor": capital.Available.MinorUnits(),
		"reserved_minor": capital.Reserved.MinorUnits(), "deployed_minor": capital.Deployed.MinorUnits(),
		"strategy_allocations": items, "limit": limit})
}

func (handler Handler) decisionView(writer http.ResponseWriter, path string,
	decisions []riskmodel.PortfolioRiskDecision, limit int) {
	items := make([]map[string]any, 0)
	for _, decision := range decisions {
		spec := decision.Spec()
		if path == "/api/v1/risk/decisions" {
			items = append(items, map[string]any{"decision_id": decision.ID().String(), "outcome": spec.Outcome,
				"portfolio_revision": spec.ExpectedPortfolioRevision, "generated_at": spec.GeneratedAt,
				"expires_at": spec.ExpiresAt, "evidence_checksum": spec.SourceEvidenceChecksum.String()})
			continue
		}
		for _, result := range spec.RiskEvaluation.RuleResults() {
			value := result.Spec()
			if path == "/api/v1/risk/violations" && value.Status != riskmodel.RuleViolation && value.Status != riskmodel.RuleModificationRequired {
				continue
			}
			items = append(items, map[string]any{"decision_id": decision.ID().String(), "rule_id": value.RuleID,
				"rule_version": value.RuleVersion, "status": value.Status, "reason_code": value.ReasonCode,
				"severity": value.Severity, "effect": value.Effect, "evaluated_at": value.EvaluatedAt})
			if len(items) == limit {
				break
			}
		}
		if len(items) == limit {
			break
		}
	}
	write(writer, http.StatusOK, map[string]any{"items": items, "limit": limit})
}

func (handler Handler) configuration(ctx context.Context, writer http.ResponseWriter, portfolio portfoliomodel.PortfolioID) {
	if handler.configs == nil {
		write(writer, http.StatusServiceUnavailable, map[string]string{"error": "configuration_unavailable"})
		return
	}
	portfolioConfig, portfolioErr := handler.configs.CurrentPortfolioConfiguration(ctx, portfolio)
	riskConfig, riskErr := handler.configs.CurrentRiskConfiguration(ctx, portfolio)
	if portfolioErr != nil || riskErr != nil {
		write(writer, http.StatusServiceUnavailable, map[string]string{"error": "configuration_unavailable"})
		return
	}
	policy := riskConfig.Policy()
	rules := policy.Rules()
	items := make([]map[string]any, len(rules))
	for index, rule := range rules {
		items[index] = map[string]any{"id": rule.Descriptor.ID, "version": rule.Descriptor.Version,
			"order": rule.Order, "severity": rule.Severity, "effect": rule.Effect,
			"configuration_hash": rule.ConfigurationHash.String()}
	}
	write(writer, http.StatusOK, map[string]any{"portfolio_configuration_id": portfolioConfig.ID().String(),
		"portfolio_configuration_version": portfolioConfig.Version(), "portfolio_configuration_hash": portfolioConfig.Hash().String(),
		"risk_policy_id": policy.ID().String(), "risk_policy_version": policy.Version(),
		"risk_configuration_hash": riskConfig.Hash().String(), "rules": items})
}

func (handler Handler) runnerHealth(writer http.ResponseWriter) {
	if handler.runner == nil {
		write(writer, http.StatusServiceUnavailable, map[string]string{"error": "runner_unavailable"})
		return
	}
	closed, inFlight, keyed, maximum, timeout := handler.runner.Health()
	write(writer, http.StatusOK, map[string]any{"closed": closed, "in_flight": inFlight,
		"keyed_portfolios": keyed, "maximum_concurrency": maximum, "timeout_milliseconds": timeout.Milliseconds()})
}

func killSwitches(values []portfoliomodel.KillSwitch, limit int) []map[string]any {
	if len(values) > limit {
		values = values[:limit]
	}
	items := make([]map[string]any, len(values))
	for index, control := range values {
		value := control.Spec()
		items[index] = map[string]any{"id": value.ID.String(), "scope": value.Scope, "scope_subject": value.ScopeSubject,
			"state": value.State, "reason_code": value.ReasonCode, "state_revision": value.StateRevision,
			"activation_evidence": value.ActivationEvidence.String(), "activated_at": value.ActivatedAt, "expires_at": value.ExpiresAt}
	}
	return items
}

func circuitBreakers(values []portfoliomodel.CircuitBreaker, limit int) []map[string]any {
	if len(values) > limit {
		values = values[:limit]
	}
	items := make([]map[string]any, len(values))
	for index, control := range values {
		value := control.Spec()
		items[index] = map[string]any{"id": value.ID.String(), "scope": value.Scope, "scope_subject": value.ScopeSubject,
			"state": value.State, "reason_code": value.ReasonCode, "state_revision": value.StateRevision,
			"evidence": value.Evidence.String(), "changed_at": value.ChangedAt}
	}
	return items
}

func parseLimit(request *http.Request) (int, bool) {
	if request.URL.Query().Get("limit") == "" {
		return 50, true
	}
	value, err := strconv.Atoi(request.URL.Query().Get("limit"))
	return value, err == nil && value > 0 && value <= 100
}

func (handler Handler) repositoryError(writer http.ResponseWriter, err error) {
	if errors.Is(err, riskstorage.ErrNotFound) {
		write(writer, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	write(writer, http.StatusServiceUnavailable, map[string]string{"error": "repository_unavailable"})
}

func write(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
