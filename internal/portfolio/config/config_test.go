package config

import (
	"bytes"
	"errors"
	"testing"
)

func TestDecodeCanonicalConfiguration(t *testing.T) {
	first, err := Decode([]byte(validDocument))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Decode([]byte(`{
		"exposure_groups":["INDEX","BANK"],
		"maximum_strategies":20,
		"maximum_exposure_group_capital_minor":400000,
		"maximum_underlying_capital_minor":300000,
		"maximum_instrument_capital_minor":200000,
		"maximum_strategy_capital_minor":500000,
		"emergency_reserve_bps":500,
		"reserve_bps":1000,
		"total_capital_minor":1000000,
		"effective_until":"2027-07-18T00:00:00Z",
		"effective_from":"2026-07-18T00:00:00Z",
		"base_currency":"INR",
		"enabled":true,
		"version":1,
		"schema_version":"portfolio-configuration/v1"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() != second.ID() || first.Hash() != second.Hash() ||
		!bytes.Equal(first.CanonicalJSON(), second.CanonicalJSON()) {
		t.Fatal("canonical configuration is not stable")
	}
	groups := first.AllocationPolicy().Limits.ExposureGroups
	groups[0] = "MUTATED"
	if first.AllocationPolicy().Limits.ExposureGroups[0] == "MUTATED" {
		t.Fatal("configuration returned mutable group data")
	}
}

func TestDecodeRejectsUnsafeConfiguration(t *testing.T) {
	tests := []string{
		`{"schema_version":"x","schema_version":"y"}`,
		`{"schema_version":"x","version":1.5}`,
		`{"schema_version":"x","version":1,"unknown":1}`,
		replace(validDocument, `"reserve_bps":1000`, `"reserve_bps":10001`),
		replace(validDocument, `"maximum_strategy_capital_minor":500000`, `"maximum_strategy_capital_minor":2000000`),
		replace(validDocument, `"version":1`, `"version":0`),
		replace(validDocument, `["BANK","INDEX"]`, `["BANK","BANK"]`),
	}
	for _, raw := range tests {
		if _, err := Decode([]byte(raw)); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("Decode(%s) error = %v", raw, err)
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(validDocument))
	f.Add([]byte(`{"schema_version":"x","x":1.0}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > MaximumConfigurationBytes+1 {
			raw = raw[:MaximumConfigurationBytes+1]
		}
		value, err := Decode(raw)
		if err == nil {
			repeated, repeatErr := Decode(value.CanonicalJSON())
			if repeatErr != nil || repeated.ID() != value.ID() || repeated.Hash() != value.Hash() {
				t.Fatalf("canonical round trip failed: %v", repeatErr)
			}
		}
	})
}

func replace(value, old, next string) string {
	return string(bytes.ReplaceAll([]byte(value), []byte(old), []byte(next)))
}

const validDocument = `{
	"schema_version":"portfolio-configuration/v1",
	"version":1,
	"enabled":true,
	"base_currency":"INR",
	"effective_from":"2026-07-18T00:00:00Z",
	"effective_until":"2027-07-18T00:00:00Z",
	"total_capital_minor":1000000,
	"reserve_bps":1000,
	"emergency_reserve_bps":500,
	"maximum_strategy_capital_minor":500000,
	"maximum_instrument_capital_minor":200000,
	"maximum_underlying_capital_minor":300000,
	"maximum_exposure_group_capital_minor":400000,
	"maximum_strategies":20,
	"exposure_groups":["BANK","INDEX"]
}`
