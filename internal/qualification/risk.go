package qualification

import riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"

// WithReleasedRisk binds qualification evidence to an immutable Phase 3
// decision. Callers cannot translate deferred or modified authority into an
// accepted SHADOW observation.
func WithReleasedRisk(input SignalInput, decision riskmodel.PortfolioRiskDecision) (SignalInput, error) {
	if decision.ID().IsZero() || decision.ProposalID().String() != input.SignalID {
		return SignalInput{}, ErrInvalid
	}
	spec := decision.Spec()
	input.RiskDecisionID = decision.ID().String()
	input.RiskReason = string(spec.PrimaryReason)
	switch decision.Outcome() {
	case riskmodel.DecisionApproved:
		input.Risk = RiskApproved
	case riskmodel.DecisionRejected:
		input.Risk = RiskRejected
	default:
		return SignalInput{}, ErrBlocked
	}
	return input, nil
}
