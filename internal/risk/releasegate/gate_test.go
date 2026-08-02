package releasegate

import (
	"context"
	"testing"
)

func TestRunProducesPassingBoundedEvidence(t *testing.T) {
	report, err := Run(context.Background())
	if err != nil || !report.Passed || report.ProductionRuleCount != 10 ||
		len(report.FailureReasons) != 0 || report.ConfiguredMaximumConcurrency != 4 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
