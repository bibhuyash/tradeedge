package phase7closure

import (
	"context"
	"strings"
	"testing"
)

func TestGateFailsClosedWithoutWorkflowEvidence(t *testing.T) {
	for _, name := range []string{"GITHUB_SHA", "GITHUB_WORKFLOW", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"} {
		t.Setenv(name, "")
	}
	r := Run(context.Background())
	if r.Passed || r.FinalResult != "FAILED" || len(r.FailureReasons) == 0 {
		t.Fatalf("gate did not fail closed: %+v", r)
	}
	if !strings.Contains(strings.Join(r.FailureReasons, "|"), "missing commit SHA") {
		t.Fatal("missing identity was not evidenced")
	}
}

func TestGatePassesWithCompleteExternalEvidenceAndIsDeterministic(t *testing.T) {
	t.Setenv("GITHUB_SHA", "0123456789abcdef")
	t.Setenv("GITHUB_WORKFLOW", "Phase 7 Closure")
	t.Setenv("GITHUB_RUN_ID", "7")
	t.Setenv("GITHUB_RUN_ATTEMPT", "1")
	for _, n := range []string{"ORDINARY", "RACE", "STRESS", "SOAK", "SECURITY", "PHASE1", "PHASE2", "PHASE3", "PHASE4", "PHASE5", "PHASE6", "PHASE7_M1", "PHASE7_M2", "ARTIFACT_UPLOAD"} {
		t.Setenv("TRADEEDGE_GATE_"+n, "passed")
	}
	a, b := Run(context.Background()), Run(context.Background())
	if !a.Passed || !Verify(a) {
		t.Fatalf("complete evidence failed: %+v", a)
	}
	if a.Checksum != b.Checksum {
		t.Fatalf("non-deterministic reports: %s %s", a.Checksum, b.Checksum)
	}
	if len(a.Scenarios) != 7 || len(a.RestartDrills) != 9 || len(a.FailureDrills) < 40 {
		t.Fatal("mandatory evidence matrix incomplete")
	}
}

func TestTamperedReportFailsVerification(t *testing.T) {
	r := Report{Passed: true, FinalResult: "PASSED", Checksum: "not-a-checksum"}
	if Verify(r) {
		t.Fatal("tampered report verified")
	}
}
