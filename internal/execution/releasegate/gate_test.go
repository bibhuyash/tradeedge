package releasegate_test

import (
	"context"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/execution/releasegate"
)

func TestPhase4ReleaseHarness(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report, err := releasegate.Run(ctx)
	if err != nil || !report.Passed || len(report.FailureReasons) != 0 || !report.AuthorityEnforcementPassed || !report.BuyBeforeSellPassed || !report.UnknownRecoveryPassed || !report.ReplayDeterminismPassed || !report.CheckpointContinuationPassed {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
