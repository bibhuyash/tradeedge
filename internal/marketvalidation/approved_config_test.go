package marketvalidation

import (
	"os"
	"path/filepath"
	"testing"

	portfolioconfig "github.com/bibhuyash/tradeedge/internal/portfolio/config"
	riskconfig "github.com/bibhuyash/tradeedge/internal/risk/config"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	"github.com/bibhuyash/tradeedge/internal/risk/rules"
)

func TestApprovedPaperCapitalAndRiskPolicy(t *testing.T) {
	root := filepath.Join("..", "..", "configs", "validation")
	portfolioRaw, err := os.ReadFile(filepath.Join(root, "portfolio.paper.json"))
	if err != nil {
		t.Fatal(err)
	}
	portfolio, err := portfolioconfig.Decode(portfolioRaw)
	if err != nil {
		t.Fatal(err)
	}
	if portfolio.AllocationPolicy().Limits.TotalCapital.MinorUnits() != 100000000 || portfolio.AllocationPolicy().Limits.MaximumStrategies != 1 {
		t.Fatal("approved PAPER capital drift")
	}
	riskRaw, err := os.ReadFile(filepath.Join(root, "risk.paper.json"))
	if err != nil {
		t.Fatal(err)
	}
	descriptors := map[riskmodel.RiskRuleID]riskmodel.RiskRuleDescriptor{}
	for _, rule := range rules.ProductionCatalog() {
		descriptors[rule.Descriptor().ID] = rule.Descriptor()
	}
	risk, err := riskconfig.Decode(riskRaw, descriptors, portfolio.AllocationPolicy().Limits.ExposureGroups)
	if err != nil {
		t.Fatal(err)
	}
	if err := rules.ValidateProductionPolicy(risk.Policy()); err != nil {
		t.Fatal(err)
	}
}
