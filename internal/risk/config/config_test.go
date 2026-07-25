package config

import (
	"bytes"
	"errors"
	"testing"

	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
)

func TestDecodeRiskConfiguration(t *testing.T) {
	registry := testRegistry(t)
	first, err := Decode([]byte(validRiskDocument), registry, []string{"INDEX"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Decode(first.CanonicalJSON(), registry, []string{"INDEX"})
	if err != nil || first.Hash() != second.Hash() ||
		first.Policy().ID() != second.Policy().ID() ||
		!bytes.Equal(first.CanonicalJSON(), second.CanonicalJSON()) {
		t.Fatalf("canonical risk configuration was unstable: %v", err)
	}
	scopes := first.KillSwitch().AllowedScopes
	scopes[0] = "MUTATED"
	if first.KillSwitch().AllowedScopes[0] == "MUTATED" {
		t.Fatal("risk configuration returned mutable control scopes")
	}
}

func TestDecodeRejectsUnknownDuplicateAndContradictoryRules(t *testing.T) {
	registry := testRegistry(t)
	tests := []struct {
		raw  string
		want error
	}{
		{replace(validRiskDocument, `"MAX_DAILY_LOSS"`, `"UNKNOWN_RULE"`), ErrUnknownRule},
		{replace(validRiskDocument, `"KILL_SWITCH","version":1,"order":2`,
			`"MAX_DAILY_LOSS","version":1,"order":2`), ErrInvalidConfiguration},
		{replace(validRiskDocument, `"loss_limit_minor":100`, `"loss_limit_minor":1.5`), ErrInvalidConfiguration},
		{replace(validRiskDocument, `"reset_threshold":2`, `"reset_threshold":5`), ErrInvalidConfiguration},
		{replace(validRiskDocument, `"exposure_group":"INDEX"`, `"exposure_group":"UNKNOWN"`), ErrInvalidConfiguration},
		{replace(validRiskDocument, `"loss_limit_minor":100`, `"loss_limit_minor":100,"unknown":1`), ErrInvalidConfiguration},
	}
	for _, test := range tests {
		if _, err := Decode([]byte(test.raw), registry, []string{"INDEX"}); !errors.Is(err, test.want) {
			t.Fatalf("Decode error = %v, want %v", err, test.want)
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(validRiskDocument))
	f.Add([]byte(`{"schema_version":"risk/v1","version":1.0}`))
	registry := testRegistry(f)
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > MaximumConfigurationBytes+1 {
			raw = raw[:MaximumConfigurationBytes+1]
		}
		value, err := Decode(raw, registry, []string{"INDEX"})
		if err == nil {
			repeated, repeatErr := Decode(value.CanonicalJSON(), registry, []string{"INDEX"})
			if repeatErr != nil || repeated.Hash() != value.Hash() ||
				repeated.Policy().ID() != value.Policy().ID() {
				t.Fatalf("valid configuration was not stable: %v", repeatErr)
			}
		}
	})
}

type testHelper interface {
	Helper()
	Fatal(...any)
}

func testRegistry(t testHelper) map[riskmodel.RiskRuleID]riskmodel.RiskRuleDescriptor {
	t.Helper()
	result := make(map[riskmodel.RiskRuleID]riskmodel.RiskRuleDescriptor)
	for _, value := range []string{"MAX_DAILY_LOSS", "KILL_SWITCH"} {
		id, err := riskmodel.NewRiskRuleID(value)
		if err != nil {
			t.Fatal(err)
		}
		result[id] = riskmodel.RiskRuleDescriptor{
			ID: id, Version: 1, Name: value, Description: "test rule",
			SchemaVersion: "risk-rule/v1",
		}
	}
	return result
}

func replace(value, old, next string) string {
	return string(bytes.ReplaceAll([]byte(value), []byte(old), []byte(next)))
}

const validRiskDocument = `{
	"schema_version":"risk-configuration/v1",
	"version":1,
	"lifecycle":"ACTIVE",
	"effective_from":"2026-07-18T00:00:00Z",
	"effective_until":"2027-07-18T00:00:00Z",
	"rules":[
		{"id":"MAX_DAILY_LOSS","version":1,"order":1,"severity":"BLOCKING","effect":"REJECT","config":{"loss_limit_minor":100,"currency":"INR","exposure_group":"INDEX"}},
		{"id":"KILL_SWITCH","version":1,"order":2,"severity":"CRITICAL","effect":"REJECT","config":{"threshold":1}}
	],
	"kill_switch":{"enabled":true,"allowed_scopes":["GLOBAL","PORTFOLIO"]},
	"circuit_breaker":{"enabled":true,"threshold":5,"reset_threshold":2}
}`
