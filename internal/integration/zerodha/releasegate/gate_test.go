package releasegate

import (
	"context"
	"testing"
)

func TestPhase5ReleaseHarness(t *testing.T) {
	report, err := Run(context.Background())
	if err != nil || !report.Passed || len(report.FailureReasons) != 0 {
		t.Fatalf("Run()=%#v,%v", report, err)
	}
}
