package marketvalidationm1

import "testing"

func TestGateFailsClosedAndPassesOnlyWithAllEvidence(t *testing.T) {
	if Run().Passed {
		t.Fatal("missing CI evidence must fail closed")
	}
	for _, name := range required {
		t.Setenv("TRADEEDGE_M1_GATE_"+name, "passed")
	}
	r := Run()
	if !r.Passed || r.LiveTradingEnabled || r.Mode != "PAPER" || r.ConfiguredStrategies != 0 {
		t.Fatalf("unexpected report: %+v", r)
	}
}
