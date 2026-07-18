package releasegate

import (
	"context"
	"testing"
	"time"
)

func TestRunProducesCompletePassingEvidence(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report, err := Run(ctx)
	if err != nil {
		t.Fatalf("Run() error = %v, failures = %#v", err, report.FailureReasons)
	}
	if !report.Passed || report.TotalEvaluations == 0 ||
		report.MaximumConcurrentEvaluations > report.ConfiguredMaximumConcurrentEvaluations ||
		report.MaximumSameInstanceConcurrency != 1 ||
		report.UnexpectedResultLoss != 0 ||
		report.UnexpectedDuplicatePublication != 0 ||
		report.EndingGoroutineCount > report.StartingGoroutineCount+report.EndingGoroutineTolerance ||
		report.EndingHeapAllocationBytes >
			report.StartingHeapAllocationBytes+report.EndingHeapGrowthLimitBytes ||
		report.MaximumCancellationShutdownNanoseconds >
			report.CancellationShutdownLimitNanoseconds ||
		!report.ReplayDeterminismPassed ||
		!report.CheckpointContinuationEquivalencePassed {
		t.Fatalf("report = %#v", report)
	}
}
