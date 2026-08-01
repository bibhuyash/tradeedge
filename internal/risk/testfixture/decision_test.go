package testfixture

import (
	"testing"

	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
)

func TestDecisionFixturesRemainValid(t *testing.T) {
	tests := []struct {
		name    string
		build   func() (riskmodel.PortfolioRiskDecision, error)
		outcome riskmodel.DecisionOutcome
	}{
		{name: "approved", build: ApprovedDecision, outcome: riskmodel.DecisionApproved},
		{name: "modified", build: ModifiedDecision, outcome: riskmodel.DecisionModified},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := test.build()
			if err != nil {
				t.Fatal(err)
			}
			if decision.Outcome() != test.outcome {
				t.Fatalf("outcome = %s", decision.Outcome())
			}
		})
	}
}
