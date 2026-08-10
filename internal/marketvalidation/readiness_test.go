package marketvalidation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeReadinessConfigFailsClosed(t *testing.T) {
	digest := strings.Repeat("a", 64)
	raw := []byte(`{
  "schema_version":"market-validation-readiness/v1",
  "trading_date":"2026-08-10",
  "expected_commit":"` + digest + `",
  "mode":"PAPER",
  "base_url":"http://127.0.0.1:8080",
  "evidence_root":".cache/market-validation/2026-08-10",
  "portfolio_id":"` + digest + `",
  "scope":"OPERATIONS_ONLY",
  "telegram_required":true,
  "files":{"calendar":"calendar.json","instrument_master":"master.json","watchlist":"watchlist.json","strategies":"strategies.json","portfolio":"portfolio.json","risk":"risk.json","authorization":"authorization.json","telegram_check":"telegram-check.json"}
}`)
	if _, err := DecodeReadinessConfig(raw); err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string]string{
		"live mode":         strings.Replace(string(raw), `"mode":"PAPER"`, `"mode":"LIVE_ENABLED"`, 1),
		"remote plain HTTP": strings.Replace(string(raw), "127.0.0.1", "example.com", 1),
		"missing config":    strings.Replace(string(raw), `"risk":"risk.json"`, `"risk":""`, 1),
		"unknown field":     strings.Replace(string(raw), `"telegram_required":true`, `"telegram_required":true,"bypass":true`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeReadinessConfig([]byte(mutation)); err == nil {
				t.Fatal("unsafe readiness configuration accepted")
			}
		})
	}
}

func TestReadinessValidatorsRejectDisabledOrMutableRuntime(t *testing.T) {
	ready := map[string]any{"status": "ready", "trading_permitted": false}
	if len(validateReady(ready)) == 0 {
		t.Fatal("disabled market data passed")
	}
	zerodha := map[string]any{"mode": "PAPER", "state": "READY", "session_state": "AUTHENTICATED", "mutation_permitted": true, "mapping_version": "v1", "stream": map[string]any{"state": "CONNECTED"}, "unknown_orders": float64(0)}
	if !contains(validateZerodha(zerodha, ReadinessConfig{Mode: "PAPER"}), "BROKER_MUTATION_PERMITTED") {
		t.Fatal("mutable broker runtime passed")
	}
}

func TestReadinessReportNeverAuthorizesLiveTrading(t *testing.T) {
	report := ReadinessReport{Ready: true}
	raw, err := json.Marshal(report)
	if err != nil || strings.Contains(string(raw), `"live_trading_authorized":true`) {
		t.Fatal("readiness report authorized live trading")
	}
}

func TestValidateTelegramEvidenceAcceptsWriterSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telegram-test.json")
	raw := []byte(`{"schema_version":"market-validation-telegram-check/v1","trading_date":"2026-08-11","mode":"PAPER","kind":"test","delivered":true,"checked_at":"2026-08-10T15:30:00Z"}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if hash, err := validateTelegramEvidence(path, "2026-08-11", "PAPER"); err != nil || hash == "" {
		t.Fatalf("validateTelegramEvidence() = %q, %v", hash, err)
	}

	withoutTimestamp := []byte(`{"schema_version":"market-validation-telegram-check/v1","trading_date":"2026-08-11","mode":"PAPER","kind":"test","delivered":true}`)
	if err := os.WriteFile(path, withoutTimestamp, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateTelegramEvidence(path, "2026-08-11", "PAPER"); err == nil {
		t.Fatal("Telegram evidence without checked_at accepted")
	}
}
