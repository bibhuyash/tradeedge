package marketvalidation

import (
	"encoding/hex"
	"errors"
	"time"
)

type PreparationCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// PreparationReport is a local diagnostic, never an authorization or session record.
type PreparationReport struct {
	SchemaVersion         string             `json:"schema_version"`
	TradingDate           string             `json:"trading_date"`
	ApplicationCommit     string             `json:"application_commit"`
	EvaluatedAt           time.Time          `json:"evaluated_at"`
	Status                string             `json:"status"`
	Checks                []PreparationCheck `json:"checks"`
	NextSteps             []string           `json:"next_steps"`
	LiveTradingAuthorized bool               `json:"live_trading_authorized"`
}

func InspectSessionPreparation(path, date, commit string, now time.Time) (PreparationReport, error) {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return PreparationReport{}, errors.New("prepare-session requires a valid YYYY-MM-DD date")
	}
	if _, err := hex.DecodeString(commit); err != nil || (len(commit) != 40 && len(commit) != 64) {
		return PreparationReport{}, errors.New("prepare-session requires a full hexadecimal application commit")
	}
	if now.IsZero() {
		return PreparationReport{}, errors.New("preparation time is required")
	}
	value, err := LoadAuthorization(path)
	return inspectPreparation(value, err, date, commit, now), nil
}

func inspectPreparation(value AuthorizationManifest, loadErr error, date, commit string, now time.Time) PreparationReport {
	report := PreparationReport{SchemaVersion: "session-preparation/v1", TradingDate: date, ApplicationCommit: commit, EvaluatedAt: now.UTC(), Status: "BLOCKED", NextSteps: []string{
		"Generate and approve the exact-date calendar from the reviewed source policy.",
		"Generate current bounded derivative mappings and build-shadow-bundle.",
		"Obtain fresh Telegram evidence and read-only Zerodha preflight under operator control.",
		"Finalize a date-, commit- and artifact-bound SHADOW authorization, then rerun prepare-session.",
		"Obtain explicit operator approval before startup. Runtime checks remain authoritative.",
	}}
	add := func(name string, ok bool, detail string) {
		report.Checks = append(report.Checks, PreparationCheck{name, ok, detail})
	}
	loaded := loadErr == nil
	add("authorization_and_artifacts", loaded, "Existing manifest, linked artifact checksums, calendar approval, mappings, risk and external evidence must verify. No credentials are read by this command.")
	add("shadow_only", loaded && value.Mode == "SHADOW" && value.Scope == ScopeQualificationOnly && value.RealBrokerMutationProhibited && value.PaperExecutionProhibited && value.QualificationEnabled && !value.LiveTradingAuthorized, "SHADOW qualification only; paper execution and broker mutations prohibited.")
	add("application_commit", loaded && value.ApplicationCommit == commit, "Authorization must bind the intended binary commit; this report does not verify a built binary.")
	add("trading_date", loaded && value.TradingDate == date && now.In(time.FixedZone("IST", 19800)).Format("2006-01-02") == date, "Authorization and today's IST date must match the requested session.")
	add("authorization_window", loaded && !now.Before(value.AuthorizedAt) && now.Before(value.ExpiresAt), "Current time must be within the authorization window; expiry is exclusive.")
	all := true
	for _, check := range report.Checks {
		all = all && check.Passed
	}
	if all {
		report.Status = "ARTIFACTS_VERIFIED_OPERATOR_APPROVAL_REQUIRED"
	}
	return report
}
