package app

import (
	"context"
	"testing"

	"github.com/bibhuyash/tradeedge/internal/adapters/broker/paper"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
)

func TestZeroStrategyProductionStagesCannotTrade(t *testing.T) {
	proposals, err := (zeroStrategyStage{}).Evaluate(context.Background(), nil, nil)
	if err != nil || len(proposals) != 0 {
		t.Fatalf("unexpected proposals: %v %v", proposals, err)
	}
	broker := paper.NewObserved()
	execution := &sealedExecutionStage{broker: broker}
	if _, err := execution.Execute(context.Background(), riskmodel.PortfolioRiskDecision{}); err == nil {
		t.Fatal("sealed execution stage accepted a trade")
	}
	checkpoint := broker.Checkpoint()
	if len(checkpoint.Orders) != 0 || len(checkpoint.Events) != 0 {
		t.Fatal("zero-strategy path mutated paper broker")
	}
}
