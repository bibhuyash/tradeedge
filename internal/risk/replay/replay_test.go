package replay

import (
	"context"
	"testing"

	"github.com/bibhuyash/tradeedge/internal/risk/runner"
)

type evaluator struct{ receipts []runner.Receipt }

func (value *evaluator) EvaluateProposal(_ context.Context, _ runner.Request) (runner.Receipt, error) {
	receipt := runner.Receipt{Outcome: runner.OutcomeApproved}
	value.receipts = append(value.receipts, receipt)
	return receipt, nil
}

func TestReplayIsSerialAndComplete(t *testing.T) {
	value := &evaluator{}
	engine, err := New(value)
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := engine.Run(context.Background(), []runner.Request{{}, {}, {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 3 || len(value.receipts) != 3 {
		t.Fatalf("receipts = %d/%d", len(receipts), len(value.receipts))
	}
}
