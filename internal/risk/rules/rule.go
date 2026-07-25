// Package rules defines the pure, provider-neutral Phase 3 rule boundary.
// Milestone 1 intentionally contains no production rule implementations or
// orchestration engine.
package rules

import (
	"context"

	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
)

type Rule interface {
	Descriptor() riskmodel.RiskRuleDescriptor
	Evaluate(context.Context, riskmodel.RiskRuleInput) riskmodel.RuleResult
}
