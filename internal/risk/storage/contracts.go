package storage

import (
	"context"
	"errors"
	"fmt"

	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

var (
	ErrNotFound          = errors.New("risk repository record not found")
	ErrIdentityCollision = errors.New("risk repository identity collision")
	ErrCapacityExhausted = errors.New("risk repository capacity exhausted")
	ErrStaleRevision     = errors.New("stale portfolio revision")
	ErrInternal          = errors.New("risk repository internal failure")
)

type IdentityCollisionError struct {
	Kind     string
	Identity string
}

func (value *IdentityCollisionError) Error() string {
	return fmt.Sprintf("%v: %s %s", ErrIdentityCollision, value.Kind, value.Identity)
}
func (value *IdentityCollisionError) Unwrap() error { return ErrIdentityCollision }

type RegistrationStatus string

const (
	RegistrationCommitted  RegistrationStatus = "COMMITTED"
	RegistrationIdempotent RegistrationStatus = "IDEMPOTENT_REPLAY"
)

type RegistrationOutcome struct{ Status RegistrationStatus }

type RiskPolicyRepository interface {
	RegisterPolicy(context.Context, riskmodel.RiskPolicy) (RegistrationOutcome, error)
	Policy(context.Context, riskmodel.RiskPolicyID) (riskmodel.RiskPolicy, error)
	Policies(context.Context) ([]riskmodel.RiskPolicy, error)
}

type PortfolioRiskDecisionRepository interface {
	AppendDecision(context.Context, riskmodel.PortfolioRiskDecision) (RegistrationOutcome, error)
	Decision(context.Context, riskmodel.PortfolioRiskDecisionID) (riskmodel.PortfolioRiskDecision, error)
	DecisionByProposal(context.Context, strategymodel.ProposalID,
		portfoliomodel.PortfolioRevision) (riskmodel.PortfolioRiskDecision, error)
	Decisions(context.Context, portfoliomodel.PortfolioID) ([]riskmodel.PortfolioRiskDecision, error)
}

type KillSwitchStateRepository interface {
	RegisterKillSwitchState(context.Context, portfoliomodel.KillSwitch) (RegistrationOutcome, error)
	KillSwitchState(context.Context, portfoliomodel.KillSwitchID, uint64) (portfoliomodel.KillSwitch, error)
	KillSwitchStates(context.Context, portfoliomodel.KillSwitchID) ([]portfoliomodel.KillSwitch, error)
}

type CircuitBreakerStateRepository interface {
	RegisterCircuitBreakerState(context.Context, portfoliomodel.CircuitBreaker) (RegistrationOutcome, error)
	CircuitBreakerState(context.Context, portfoliomodel.CircuitBreakerID, uint64) (portfoliomodel.CircuitBreaker, error)
	CircuitBreakerStates(context.Context, portfoliomodel.CircuitBreakerID) ([]portfoliomodel.CircuitBreaker, error)
}
