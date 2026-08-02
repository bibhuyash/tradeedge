package rules

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/bibhuyash/tradeedge/internal/domain"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
)

const productionRuleVersion riskmodel.RiskRuleVersion = 1

var ErrInvalidProductionPolicy = errors.New("invalid Phase 3 production risk policy")

const (
	PortfolioCapitalLimitID    riskmodel.RiskRuleID = "PORTFOLIO_CAPITAL_LIMIT"
	StrategyAllocationLimitID  riskmodel.RiskRuleID = "STRATEGY_ALLOCATION_LIMIT"
	DailyLossLimitID           riskmodel.RiskRuleID = "DAILY_LOSS_LIMIT"
	DrawdownLimitID            riskmodel.RiskRuleID = "DRAWDOWN_LIMIT"
	InstrumentExposureLimitID  riskmodel.RiskRuleID = "INSTRUMENT_EXPOSURE_LIMIT"
	UnderlyingExposureLimitID  riskmodel.RiskRuleID = "UNDERLYING_EXPOSURE_LIMIT"
	MaximumOpenExposureLimitID riskmodel.RiskRuleID = "MAXIMUM_OPEN_EXPOSURE"
	ReserveCapitalLimitID      riskmodel.RiskRuleID = "RESERVE_CAPITAL"
	KillSwitchRuleID           riskmodel.RiskRuleID = "KILL_SWITCH"
	CircuitBreakerRuleID       riskmodel.RiskRuleID = "CIRCUIT_BREAKER"
)

// ProductionCatalog returns the complete, statically versioned Phase 3 rule set.
func ProductionCatalog() []Rule {
	return []Rule{
		newProductionRule(PortfolioCapitalLimitID, "Portfolio capital limit"),
		newProductionRule(StrategyAllocationLimitID, "Strategy allocation limit"),
		newProductionRule(DailyLossLimitID, "Daily loss limit"),
		newProductionRule(DrawdownLimitID, "Drawdown limit"),
		newProductionRule(InstrumentExposureLimitID, "Instrument exposure limit"),
		newProductionRule(UnderlyingExposureLimitID, "Underlying exposure limit"),
		newProductionRule(MaximumOpenExposureLimitID, "Maximum open exposure"),
		newProductionRule(ReserveCapitalLimitID, "Reserve capital"),
		newProductionRule(KillSwitchRuleID, "Kill switch"),
		newProductionRule(CircuitBreakerRuleID, "Circuit breaker"),
	}
}

// RegisterProduction registers the reviewed catalog in deterministic catalog order.
func RegisterProduction(registry *Registry) error {
	for _, rule := range ProductionCatalog() {
		if err := registry.Register(rule); err != nil {
			return err
		}
	}
	return nil
}

// ValidateProductionPolicy requires the reviewed catalog exactly once and in catalog order.
func ValidateProductionPolicy(policy riskmodel.RiskPolicy) error {
	configured := policy.Rules()
	catalog := ProductionCatalog()
	if len(configured) != len(catalog) {
		return ErrInvalidProductionPolicy
	}
	for index := range catalog {
		if configured[index].Order != uint16(index+1) ||
			configured[index].Descriptor != catalog[index].Descriptor() {
			return ErrInvalidProductionPolicy
		}
	}
	return nil
}

type productionRule struct{ descriptor riskmodel.RiskRuleDescriptor }

func newProductionRule(id riskmodel.RiskRuleID, name string) productionRule {
	return productionRule{descriptor: riskmodel.RiskRuleDescriptor{
		ID: id, Version: productionRuleVersion, Name: name,
		Description:   "Pure deterministic Phase 3 production-style risk control",
		SchemaVersion: "risk-rule/v1",
	}}
}

func (rule productionRule) Descriptor() riskmodel.RiskRuleDescriptor { return rule.descriptor }

type productionConfig struct {
	LimitMinor     *int64  `json:"limit_minor,omitempty"`
	LossLimitMinor *int64  `json:"loss_limit_minor,omitempty"`
	LimitBPS       *int32  `json:"limit_bps,omitempty"`
	Threshold      *uint64 `json:"threshold,omitempty"`
	ResetThreshold *uint64 `json:"reset_threshold,omitempty"`
	Currency       string  `json:"currency,omitempty"`
	ExposureGroup  string  `json:"exposure_group,omitempty"`
}

func (rule productionRule) Evaluate(ctx context.Context, input riskmodel.RiskRuleInput) riskmodel.RuleResult {
	spec := input.Spec()
	if ctx.Err() != nil {
		return rule.result(spec, riskmodel.RuleDefer, "CONTEXT_CANCELLED", riskmodel.EffectDefer, nil, nil)
	}
	var config productionConfig
	if err := json.Unmarshal(spec.RuleConfiguration.Canonical(), &config); err != nil {
		return rule.result(spec, riskmodel.RuleError, "INVALID_RULE_CONFIGURATION", riskmodel.EffectDefer, nil, nil)
	}
	switch rule.descriptor.ID {
	case PortfolioCapitalLimitID:
		return rule.portfolioCapital(spec, config)
	case StrategyAllocationLimitID:
		return rule.strategyAllocation(spec, config)
	case DailyLossLimitID:
		return rule.dailyLoss(spec, config)
	case DrawdownLimitID:
		return rule.drawdown(spec, config)
	case InstrumentExposureLimitID:
		return rule.exposure(spec, config, portfoliomodel.ExposureInstrument)
	case UnderlyingExposureLimitID:
		return rule.exposure(spec, config, portfoliomodel.ExposureUnderlying)
	case MaximumOpenExposureLimitID:
		return rule.exposure(spec, config, portfoliomodel.ExposurePortfolioWide)
	case ReserveCapitalLimitID:
		return rule.reserve(spec, config)
	case KillSwitchRuleID:
		return rule.killSwitch(spec)
	default:
		return rule.circuitBreaker(spec)
	}
}

func (rule productionRule) portfolioCapital(input riskmodel.RiskRuleInputSpec, config productionConfig) riskmodel.RuleResult {
	limit, ok := configuredMoney(config, input.PortfolioSnapshot.Spec().BaseCurrency, false)
	if !ok {
		return rule.failClosed(input, "CAPITAL_LIMIT_UNAVAILABLE")
	}
	capital := input.PortfolioSnapshot.Capital()
	used, err := portfoliomodel.CheckedMoneyAdd(capital.Deployed, capital.Reserved)
	if err != nil {
		return rule.failClosed(input, "CAPITAL_ARITHMETIC_OVERFLOW")
	}
	projected, err := portfoliomodel.CheckedMoneyAdd(used, input.AllocationCandidate.CandidateCapital())
	if err != nil {
		return rule.failClosed(input, "CAPITAL_ARITHMETIC_OVERFLOW")
	}
	return rule.boundedMoney(input, "PORTFOLIO_CAPITAL", riskmodel.SubjectPortfolio,
		input.PortfolioSnapshot.PortfolioID().String(), used, projected, limit,
		portfoliomodel.ReasonPortfolioAllocationExhausted)
}

func (rule productionRule) strategyAllocation(input riskmodel.RiskRuleInputSpec, config productionConfig) riskmodel.RuleResult {
	allocation := input.StrategyAllocation.Spec()
	limit := allocation.Limit
	if config.LimitMinor != nil {
		configured, ok := configuredMoney(config, limit.Currency(), false)
		if !ok {
			return rule.failClosed(input, "STRATEGY_LIMIT_UNAVAILABLE")
		}
		if configured.MinorUnits() < limit.MinorUnits() {
			limit = configured
		}
	}
	used, err := portfoliomodel.CheckedMoneyAdd(allocation.Deployed, allocation.Reserved)
	if err != nil {
		return rule.failClosed(input, "STRATEGY_ARITHMETIC_OVERFLOW")
	}
	projected, err := portfoliomodel.CheckedMoneyAdd(used, input.AllocationCandidate.CandidateCapital())
	if err != nil {
		return rule.failClosed(input, "STRATEGY_ARITHMETIC_OVERFLOW")
	}
	return rule.boundedMoney(input, "STRATEGY_ALLOCATION", riskmodel.SubjectStrategy,
		allocation.ID.String(), used, projected, limit, portfoliomodel.ReasonStrategyAllocationExhausted)
}

func (rule productionRule) dailyLoss(input riskmodel.RiskRuleInputSpec, config productionConfig) riskmodel.RuleResult {
	limit, ok := configuredMoney(config, input.PortfolioSnapshot.Spec().BaseCurrency, true)
	if !ok {
		return rule.failClosed(input, "DAILY_LOSS_LIMIT_UNAVAILABLE")
	}
	snapshot := input.PortfolioSnapshot.Spec()
	pnl, err := portfoliomodel.CheckedMoneyAdd(snapshot.DailyRealizedPnL, snapshot.DailyUnrealizedPnL)
	if err != nil || pnl.MinorUnits() == math.MinInt64 {
		return rule.failClosed(input, "DAILY_LOSS_ARITHMETIC_OVERFLOW")
	}
	lossMinor := int64(0)
	if pnl.MinorUnits() < 0 {
		lossMinor = -pnl.MinorUnits()
	}
	loss, _ := domain.NewMoney(lossMinor, snapshot.BaseCurrency.String())
	evidence := rule.moneyEvidence(input, "DAILY_LOSS", riskmodel.SubjectPortfolio,
		input.PortfolioSnapshot.PortfolioID().String(), loss, loss, limit)
	if loss.MinorUnits() >= limit.MinorUnits() {
		return rule.result(input, riskmodel.RuleViolation, "DAILY_LOSS_LIMIT_REACHED",
			violationEffect(input.RuleConfiguration), evidence, nil)
	}
	return rule.result(input, riskmodel.RulePass, "DAILY_LOSS_WITHIN_LIMIT", riskmodel.EffectNone, evidence, nil)
}

func (rule productionRule) drawdown(input riskmodel.RiskRuleInputSpec, config productionConfig) riskmodel.RuleResult {
	drawdown := input.PortfolioSnapshot.Drawdown()
	snapshot := input.PortfolioSnapshot.Spec()
	violation := false
	evidence := make([]riskmodel.RiskEvidence, 0, 2)
	if config.LossLimitMinor != nil {
		limit, ok := configuredMoney(config, snapshot.BaseCurrency, true)
		if !ok {
			return rule.failClosed(input, "DRAWDOWN_LIMIT_UNAVAILABLE")
		}
		evidence = append(evidence, rule.moneyEvidence(input, "DRAWDOWN_AMOUNT", riskmodel.SubjectPortfolio,
			input.PortfolioSnapshot.PortfolioID().String(), drawdown.Amount, drawdown.Amount, limit)...)
		violation = drawdown.Amount.MinorUnits() >= limit.MinorUnits()
	}
	if config.LimitBPS != nil {
		item, err := rule.bpsEvidence(input, "DRAWDOWN_BPS", int64(drawdown.BPS), int64(*config.LimitBPS))
		if err != nil {
			return rule.failClosed(input, "DRAWDOWN_EVIDENCE_INVALID")
		}
		evidence = append(evidence, item)
		violation = violation || int32(drawdown.BPS) >= *config.LimitBPS
	}
	if len(evidence) == 0 {
		return rule.failClosed(input, "DRAWDOWN_LIMIT_UNAVAILABLE")
	}
	if violation {
		return rule.result(input, riskmodel.RuleViolation, "DRAWDOWN_LIMIT_REACHED",
			violationEffect(input.RuleConfiguration), evidence, nil)
	}
	return rule.result(input, riskmodel.RulePass, "DRAWDOWN_WITHIN_LIMIT", riskmodel.EffectNone, evidence, nil)
}

func (rule productionRule) exposure(input riskmodel.RiskRuleInputSpec, config productionConfig,
	dimension portfoliomodel.ExposureDimension) riskmodel.RuleResult {
	limit, ok := configuredMoney(config, input.PortfolioSnapshot.Spec().BaseCurrency, false)
	if !ok {
		return rule.failClosed(input, "EXPOSURE_LIMIT_UNAVAILABLE")
	}
	projected := input.AllocationCandidate.Spec().ProjectedExposure
	matching := make([]portfoliomodel.ExposureRecord, 0)
	for _, value := range projected {
		if value.Dimension() == dimension {
			matching = append(matching, value)
		}
	}
	if len(matching) == 0 {
		return rule.failClosed(input, "PROJECTED_EXPOSURE_MISSING")
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].Subject() < matching[j].Subject() })
	var worst domain.Money
	worstSubject := ""
	for _, value := range matching {
		gross, known := value.Gross().Value()
		if !known || gross.Currency() != limit.Currency() {
			return rule.failClosed(input, "PROJECTED_EXPOSURE_UNKNOWN")
		}
		if worst.IsZeroValue() || gross.MinorUnits() > worst.MinorUnits() {
			worst, worstSubject = gross, value.Subject()
		}
	}
	subjectType := riskmodel.SubjectInstrument
	reason := portfoliomodel.ReasonInstrumentLimitExceeded
	if dimension == portfoliomodel.ExposureUnderlying {
		subjectType, reason = riskmodel.SubjectUnderlying, portfoliomodel.ReasonUnderlyingLimitExceeded
	} else if dimension == portfoliomodel.ExposurePortfolioWide {
		subjectType = riskmodel.SubjectPortfolio
		reason = portfoliomodel.ReasonPortfolioAllocationExhausted
	}
	zero, _ := domain.NewMoney(0, limit.Currency().String())
	return rule.boundedMoney(input, strings.TrimSuffix(string(rule.descriptor.ID), "_LIMIT"),
		subjectType, worstSubject, zero, worst, limit, reason)
}

func (rule productionRule) reserve(input riskmodel.RiskRuleInputSpec, config productionConfig) riskmodel.RuleResult {
	if config.LimitBPS == nil || *config.LimitBPS < 0 || *config.LimitBPS > 10000 {
		return rule.failClosed(input, "RESERVE_LIMIT_UNAVAILABLE")
	}
	capital := input.PortfolioSnapshot.Capital()
	if capital.Total.MinorUnits() > math.MaxInt64/int64(*config.LimitBPS) && *config.LimitBPS != 0 {
		return rule.failClosed(input, "RESERVE_ARITHMETIC_OVERFLOW")
	}
	requiredMinor := capital.Total.MinorUnits() * int64(*config.LimitBPS)
	requiredMinor = (requiredMinor + 9999) / 10000
	required, _ := domain.NewMoney(requiredMinor, capital.Total.Currency().String())
	projected, err := portfoliomodel.CheckedMoneySubtract(capital.Available,
		input.AllocationCandidate.CandidateCapital())
	if err != nil {
		return rule.failClosed(input, "RESERVE_ARITHMETIC_OVERFLOW")
	}
	return rule.boundedAvailable(input, projected, required)
}

func (rule productionRule) killSwitch(input riskmodel.RiskRuleInputSpec) riskmodel.RuleResult {
	controls := input.PortfolioSnapshot.Spec().KillSwitches
	applicable := 0
	if len(controls) == 0 {
		return rule.failClosed(input, "KILL_SWITCH_STATE_MISSING")
	}
	for _, control := range controls {
		if !controlApplies(control.Spec().Scope, control.Spec().ScopeSubject, input) {
			continue
		}
		applicable++
		if control.Blocks() {
			item, err := rule.stateEvidence(input, "KILL_SWITCH_STATE", string(control.Spec().State),
				string(control.Spec().Scope), control.Spec().ScopeSubject)
			if err != nil {
				return rule.failClosed(input, "KILL_SWITCH_EVIDENCE_INVALID")
			}
			return rule.result(input, riskmodel.RuleViolation, "KILL_SWITCH_BLOCKING",
				violationEffect(input.RuleConfiguration), []riskmodel.RiskEvidence{item}, nil)
		}
	}
	if applicable == 0 {
		return rule.failClosed(input, "KILL_SWITCH_STATE_MISSING")
	}
	item, err := rule.stateEvidence(input, "KILL_SWITCH_STATE", "INACTIVE", "ALL", "portfolio-controls")
	if err != nil {
		return rule.failClosed(input, "KILL_SWITCH_EVIDENCE_INVALID")
	}
	return rule.result(input, riskmodel.RulePass, "KILL_SWITCH_CLEAR", riskmodel.EffectNone,
		[]riskmodel.RiskEvidence{item}, nil)
}

func (rule productionRule) circuitBreaker(input riskmodel.RiskRuleInputSpec) riskmodel.RuleResult {
	controls := input.PortfolioSnapshot.Spec().CircuitBreakers
	applicable := 0
	if len(controls) == 0 {
		return rule.failClosed(input, "CIRCUIT_BREAKER_STATE_MISSING")
	}
	for _, control := range controls {
		if !controlApplies(control.Spec().Scope, control.Spec().ScopeSubject, input) {
			continue
		}
		applicable++
		if control.Blocks() {
			item, err := rule.stateEvidence(input, "CIRCUIT_BREAKER_STATE", string(control.Spec().State),
				string(control.Spec().Scope), control.Spec().ScopeSubject)
			if err != nil {
				return rule.failClosed(input, "CIRCUIT_BREAKER_EVIDENCE_INVALID")
			}
			return rule.result(input, riskmodel.RuleViolation, "CIRCUIT_BREAKER_BLOCKING",
				violationEffect(input.RuleConfiguration), []riskmodel.RiskEvidence{item}, nil)
		}
	}
	if applicable == 0 {
		return rule.failClosed(input, "CIRCUIT_BREAKER_STATE_MISSING")
	}
	item, err := rule.stateEvidence(input, "CIRCUIT_BREAKER_STATE", "CLOSED", "ALL", "portfolio-controls")
	if err != nil {
		return rule.failClosed(input, "CIRCUIT_BREAKER_EVIDENCE_INVALID")
	}
	return rule.result(input, riskmodel.RulePass, "CIRCUIT_BREAKER_CLEAR", riskmodel.EffectNone,
		[]riskmodel.RiskEvidence{item}, nil)
}

func controlApplies(scope portfoliomodel.ControlScope, subject string, input riskmodel.RiskRuleInputSpec) bool {
	switch scope {
	case portfoliomodel.ScopeGlobal:
		return true
	case portfoliomodel.ScopePortfolio:
		return subject == input.PortfolioSnapshot.PortfolioID().String()
	case portfoliomodel.ScopeStrategyDefinition:
		return subject == input.Proposal.Metadata().DefinitionID.String()
	case portfoliomodel.ScopeStrategyInstance:
		return subject == string(input.Proposal.Metadata().InstanceID)
	case portfoliomodel.ScopeInstrument:
		for _, leg := range input.Proposal.Draft().Legs {
			if subject == leg.InstrumentID.String() {
				return true
			}
		}
	case portfoliomodel.ScopeUnderlying, portfoliomodel.ScopeExposureGroup:
		dimension := portfoliomodel.ExposureUnderlying
		if scope == portfoliomodel.ScopeExposureGroup {
			dimension = portfoliomodel.ExposureGroup
		}
		for _, exposure := range input.AllocationCandidate.Spec().ProjectedExposure {
			if exposure.Dimension() == dimension && exposure.Subject() == subject {
				return true
			}
		}
	}
	return false
}

func (rule productionRule) boundedMoney(input riskmodel.RiskRuleInputSpec, code string,
	subjectType riskmodel.EvidenceSubject, subject string, observed, projected, limit domain.Money,
	reason portfoliomodel.AllocationReason) riskmodel.RuleResult {
	evidence := rule.moneyEvidence(input, code, subjectType, subject, observed, projected, limit)
	if projected.MinorUnits() <= limit.MinorUnits() {
		return rule.result(input, riskmodel.RulePass, code+"_WITHIN_LIMIT", riskmodel.EffectNone, evidence, nil)
	}
	headroom := limit.MinorUnits() - observed.MinorUnits()
	if headroom > 0 && headroom < input.AllocationCandidate.CandidateCapital().MinorUnits() {
		if adjustment, ok := modification(input, headroom, reason); ok {
			return rule.result(input, riskmodel.RuleModificationRequired, code+"_REDUCTION_REQUIRED",
				riskmodel.EffectModify, evidence, adjustment)
		}
	}
	return rule.result(input, riskmodel.RuleViolation, code+"_LIMIT_EXCEEDED",
		violationEffect(input.RuleConfiguration), evidence, nil)
}

func (rule productionRule) boundedAvailable(input riskmodel.RiskRuleInputSpec,
	projected, required domain.Money) riskmodel.RuleResult {
	headroom, err := portfoliomodel.CheckedMoneySubtract(projected, required)
	if err != nil {
		return rule.failClosed(input, "RESERVE_ARITHMETIC_OVERFLOW")
	}
	evidenceValue, err := riskmodel.NewRiskEvidence(riskmodel.RiskEvidenceSpec{
		Code: "RESERVE_CAPITAL", Observed: knownMoney(input.PortfolioSnapshot.Capital().Available),
		Limit: knownMoney(required), Projected: knownMoney(projected), RemainingHeadroom: knownMoney(headroom),
		Unit: "MINOR_UNITS", Comparison: riskmodel.CompareGreaterOrEqual,
		SubjectType: riskmodel.SubjectPortfolio, SubjectIdentity: input.PortfolioSnapshot.PortfolioID().String(),
		SourceSnapshotID: input.PortfolioSnapshot.ID(), SourceProposalID: input.Proposal.ID(),
		FormulaVersion: "phase-3-m3/v1", EvidenceAt: input.EvaluatedAt,
		Explanation: "projected available capital must preserve configured reserve",
	})
	if err != nil {
		return rule.failClosed(input, "RESERVE_EVIDENCE_INVALID")
	}
	evidence := []riskmodel.RiskEvidence{evidenceValue}
	if projected.MinorUnits() >= required.MinorUnits() {
		return rule.result(input, riskmodel.RulePass, "RESERVE_CAPITAL_PRESERVED", riskmodel.EffectNone, evidence, nil)
	}
	maximum := input.PortfolioSnapshot.Capital().Available.MinorUnits() - required.MinorUnits()
	if maximum > 0 && maximum < input.AllocationCandidate.CandidateCapital().MinorUnits() {
		if adjustment, ok := modification(input, maximum, portfoliomodel.ReasonReserveRequirementNotMet); ok {
			return rule.result(input, riskmodel.RuleModificationRequired, "RESERVE_CAPITAL_REDUCTION_REQUIRED",
				riskmodel.EffectModify, evidence, adjustment)
		}
	}
	return rule.result(input, riskmodel.RuleViolation, "RESERVE_CAPITAL_NOT_PRESERVED",
		violationEffect(input.RuleConfiguration), evidence, nil)
}

func modification(input riskmodel.RiskRuleInputSpec, maximum int64,
	reason portfoliomodel.AllocationReason) (*riskmodel.RuleAdjustment, bool) {
	candidate := input.AllocationCandidate.Spec()
	if maximum <= 0 || candidate.CandidateCapital.MinorUnits() <= 0 {
		return nil, false
	}
	maximumMoney, _ := domain.NewMoney(maximum, candidate.CandidateCapital.Currency().String())
	legs := append([]portfoliomodel.AllocationLegBound(nil), candidate.LegBounds...)
	strict := false
	for index, leg := range legs {
		if leg.Resolution != portfoliomodel.QuantityResolved {
			return nil, false
		}
		scaled := leg.MaximumUnits.Int64() * maximum / candidate.CandidateCapital.MinorUnits()
		scaled -= scaled % leg.LotSize.Int64()
		if scaled <= 0 {
			return nil, false
		}
		quantity, _ := domain.NewQuantity(scaled)
		legs[index].MaximumUnits = quantity
		strict = strict || scaled < leg.MaximumUnits.Int64()
	}
	if !strict {
		return nil, false
	}
	constraint := portfoliomodel.AllocationConstraint{Code: reason, Before: candidate.CandidateCapital,
		After: maximumMoney, Explanation: "risk rule reduced candidate to deterministic safe headroom"}
	return &riskmodel.RuleAdjustment{MaximumCapital: maximumMoney, LegBounds: legs,
		Constraints: []portfoliomodel.AllocationConstraint{constraint}, ValidUntil: candidate.ExpiresAt}, true
}

func configuredMoney(config productionConfig, currency domain.Currency, loss bool) (domain.Money, bool) {
	pointer := config.LimitMinor
	if loss {
		pointer = config.LossLimitMinor
	}
	if pointer == nil || *pointer < 0 || (loss && *pointer == 0) || config.Currency != currency.String() {
		return domain.Money{}, false
	}
	value, err := domain.NewMoney(*pointer, currency.String())
	return value, err == nil
}

func (rule productionRule) failClosed(input riskmodel.RiskRuleInputSpec, reason string) riskmodel.RuleResult {
	return rule.result(input, riskmodel.RuleDefer, reason, riskmodel.EffectDefer, nil, nil)
}

func (rule productionRule) result(input riskmodel.RiskRuleInputSpec, status riskmodel.RuleResultStatus,
	reason string, effect riskmodel.RuleEffect, evidence []riskmodel.RiskEvidence,
	adjustment *riskmodel.RuleAdjustment) riskmodel.RuleResult {
	value, err := riskmodel.NewRuleResult(riskmodel.RuleResultSpec{
		RuleID: rule.descriptor.ID, RuleVersion: rule.descriptor.Version,
		ConfigurationHash: input.RuleConfiguration.ConfigurationHash, Status: status,
		ReasonCode: reason, Severity: input.RuleConfiguration.Severity, Effect: effect,
		Evidence: evidence, Adjustment: adjustment, EvaluatedAt: input.EvaluatedAt,
	})
	if err == nil {
		return value
	}
	value, _ = riskmodel.NewRuleResult(riskmodel.RuleResultSpec{
		RuleID: rule.descriptor.ID, RuleVersion: rule.descriptor.Version,
		ConfigurationHash: input.RuleConfiguration.ConfigurationHash, Status: riskmodel.RuleError,
		ReasonCode: "RULE_RESULT_INVALID", Severity: input.RuleConfiguration.Severity,
		Effect: riskmodel.EffectDefer, EvaluatedAt: input.EvaluatedAt,
	})
	return value
}

func violationEffect(configuration riskmodel.RiskRuleConfiguration) riskmodel.RuleEffect {
	switch configuration.Effect {
	case riskmodel.EffectReject, riskmodel.EffectTripCircuitBreaker, riskmodel.EffectActivateKillSwitch:
		return configuration.Effect
	default:
		return riskmodel.EffectReject
	}
}

func knownMoney(value domain.Money) riskmodel.EvidenceValue {
	return riskmodel.EvidenceValue{Kind: riskmodel.EvidenceMoney,
		Availability: portfoliomodel.AvailabilityKnown, Money: value}
}

func (rule productionRule) moneyEvidence(input riskmodel.RiskRuleInputSpec, code string,
	subjectType riskmodel.EvidenceSubject, subject string, observed, projected, limit domain.Money) []riskmodel.RiskEvidence {
	remainingMinor := limit.MinorUnits() - projected.MinorUnits()
	remaining, err := domain.NewMoney(remainingMinor, limit.Currency().String())
	if err != nil {
		return nil
	}
	evidence, err := riskmodel.NewRiskEvidence(riskmodel.RiskEvidenceSpec{
		Code: code, Observed: knownMoney(observed), Limit: knownMoney(limit), Projected: knownMoney(projected),
		RemainingHeadroom: knownMoney(remaining), Unit: "MINOR_UNITS",
		Comparison: riskmodel.CompareLessOrEqual, SubjectType: subjectType, SubjectIdentity: subject,
		SourceSnapshotID: input.PortfolioSnapshot.ID(), SourceProposalID: input.Proposal.ID(),
		FormulaVersion: "phase-3-m3/v1", EvidenceAt: input.EvaluatedAt,
		Explanation: "deterministic checked integer comparison against configured risk limit",
	})
	if err != nil {
		return nil
	}
	return []riskmodel.RiskEvidence{evidence}
}

func (rule productionRule) bpsEvidence(input riskmodel.RiskRuleInputSpec, code string,
	observed, limit int64) (riskmodel.RiskEvidence, error) {
	return riskmodel.NewRiskEvidence(riskmodel.RiskEvidenceSpec{
		Code:              code,
		Observed:          riskmodel.EvidenceValue{Kind: riskmodel.EvidenceBasisPoints, Availability: portfoliomodel.AvailabilityKnown, Integer: observed},
		Limit:             riskmodel.EvidenceValue{Kind: riskmodel.EvidenceBasisPoints, Availability: portfoliomodel.AvailabilityKnown, Integer: limit},
		Projected:         riskmodel.EvidenceValue{Kind: riskmodel.EvidenceBasisPoints, Availability: portfoliomodel.AvailabilityKnown, Integer: observed},
		RemainingHeadroom: riskmodel.EvidenceValue{Kind: riskmodel.EvidenceBasisPoints, Availability: portfoliomodel.AvailabilityKnown, Integer: max64(0, limit-observed)},
		Unit:              "BASIS_POINTS", Comparison: riskmodel.CompareLessOrEqual,
		SubjectType: riskmodel.SubjectPortfolio, SubjectIdentity: input.PortfolioSnapshot.PortfolioID().String(),
		SourceSnapshotID: input.PortfolioSnapshot.ID(), SourceProposalID: input.Proposal.ID(),
		FormulaVersion: "phase-3-m3/v1", EvidenceAt: input.EvaluatedAt,
		Explanation: "deterministic integer basis-point drawdown comparison",
	})
}

func (rule productionRule) stateEvidence(input riskmodel.RiskRuleInputSpec, code, state, scope,
	subject string) (riskmodel.RiskEvidence, error) {
	known := func(text string) riskmodel.EvidenceValue {
		return riskmodel.EvidenceValue{Kind: riskmodel.EvidenceState,
			Availability: portfoliomodel.AvailabilityKnown, Text: text}
	}
	return riskmodel.NewRiskEvidence(riskmodel.RiskEvidenceSpec{
		Code: code, Observed: known(state), Limit: known("NON_BLOCKING"), Projected: known(state),
		RemainingHeadroom: known("NOT_APPLICABLE"), Unit: "CONTROL_STATE",
		Comparison: riskmodel.CompareEqual, SubjectType: riskmodel.SubjectPortfolio,
		SubjectIdentity: scope + ":" + subject, SourceSnapshotID: input.PortfolioSnapshot.ID(),
		SourceProposalID: input.Proposal.ID(), FormulaVersion: "phase-3-m3/v1",
		EvidenceAt: input.EvaluatedAt, Explanation: "immutable portfolio control state enforced fail closed",
	})
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
