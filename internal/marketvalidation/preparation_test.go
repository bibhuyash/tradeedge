package marketvalidation

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPreparationFailsClosedAndNeverAuthorizes(t *testing.T) {
	now := time.Date(2026, 8, 18, 4, 0, 0, 0, time.UTC)
	commit := strings.Repeat("a", 40)
	good := AuthorizationManifest{Mode: "SHADOW", Scope: ScopeQualificationOnly, RealBrokerMutationProhibited: true, PaperExecutionProhibited: true, QualificationEnabled: true, ApplicationCommit: commit, TradingDate: "2026-08-18", AuthorizedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)}
	cases := []struct {
		name   string
		mutate func(*AuthorizationManifest)
		err    error
	}{
		{"valid", func(*AuthorizationManifest) {}, nil},
		{"corrupt", func(*AuthorizationManifest) {}, errors.New("unverified")},
		{"paper", func(v *AuthorizationManifest) { v.Mode = "PAPER" }, nil},
		{"live", func(v *AuthorizationManifest) { v.LiveTradingAuthorized = true }, nil},
		{"commit", func(v *AuthorizationManifest) { v.ApplicationCommit = strings.Repeat("b", 40) }, nil},
		{"date", func(v *AuthorizationManifest) { v.TradingDate = "2026-08-19" }, nil},
		{"expired", func(v *AuthorizationManifest) { v.ExpiresAt = now }, nil},
		{"future", func(v *AuthorizationManifest) { v.AuthorizedAt = now.Add(time.Second) }, nil},
		{"mutations", func(v *AuthorizationManifest) { v.RealBrokerMutationProhibited = false }, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			value := good
			c.mutate(&value)
			report := inspectPreparation(value, c.err, "2026-08-18", commit, now)
			if report.LiveTradingAuthorized {
				t.Fatal("authorized live trading")
			}
			if (report.Status != "BLOCKED") != (c.name == "valid") {
				t.Fatalf("unexpected status %s", report.Status)
			}
		})
	}
	report, err := InspectSessionPreparation("missing.json", "2026-08-18", commit, now)
	if err != nil || report.Status != "BLOCKED" {
		t.Fatalf("missing manifest: %v %v", report, err)
	}
	if _, err = InspectSessionPreparation("", "invalid", commit, now); err == nil {
		t.Fatal("bad date accepted")
	}
	if _, err = InspectSessionPreparation("", "2026-08-18", "HEAD", now); err == nil {
		t.Fatal("symbolic commit accepted")
	}
	if report := inspectPreparation(good, nil, "2026-08-18", commit, now.AddDate(0, 0, 1)); report.Status != "BLOCKED" {
		t.Fatal("stale date accepted")
	}
}
