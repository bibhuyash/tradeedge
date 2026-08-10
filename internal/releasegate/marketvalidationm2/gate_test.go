package marketvalidationm2

import "testing"

func TestGateFailsClosedAndNeverClaimsSessionReadiness(t *testing.T) {
	r := Run()
	if r.Passed || r.Day0Ready || r.Day1Ready || r.LiveTradingEnabled || r.Strategy != "NONE" {
		t.Fatalf("unsafe report: %#v", r)
	}
}
