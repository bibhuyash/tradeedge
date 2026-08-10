package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRuntimeBundlePinsZeroStrategyMarketConfiguration(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"calendar.json":          `{}`,
		"instrument-master.json": `{"schema_version":1,"as_of":"2026-08-10T03:45:00Z","source_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","instruments":[{"key":"nifty","exchange":"NSE","segment":"INDEX","underlying":"NIFTY","type":"INDEX","symbol":"NIFTY 50","lot_size":1,"tick_size_minor":5,"currency":"INR"}],"mappings":[{"provider":"zerodha","token":"256265","trading_symbol":"NIFTY 50","instrument_key":"nifty","valid_from":"2026-08-10T00:00:00Z","valid_until":"2026-08-11T00:00:00Z"}]}`,
		"watchlist.json":         `{"schema_version":1,"id":"day1","requirements":[{"provider":"zerodha","instrument_key":"nifty","exchange":"NSE","segment":"INDEX","event_kind":"QUOTE","required":true}]}`,
		"strategies.json":        `{"schema_version":"market-validation-strategies/v1","instances":[]}`,
		"portfolio.json":         `{}`,
		"risk.json":              `{}`,
	}
	references := map[string]FileReference{}
	for name, value := range files {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(value), 0o640); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(value))
		references[name] = FileReference{Path: name, SHA256: hex.EncodeToString(sum[:])}
	}
	manifest := fmt.Sprintf(`{"schema_version":"%s","mode":"PAPER","calendar":%s,"instrument_master":%s,"watchlist":%s,"strategies":%s,"portfolio":%s,"risk":%s}`,
		RuntimeBundleSchemaVersion, refJSON(references["calendar.json"]), refJSON(references["instrument-master.json"]), refJSON(references["watchlist.json"]), refJSON(references["strategies.json"]), refJSON(references["portfolio.json"]), refJSON(references["risk.json"]))
	path := filepath.Join(root, "bundle.json")
	if err := os.WriteFile(path, []byte(manifest), 0o640); err != nil {
		t.Fatal(err)
	}
	bundle, err := LoadRuntimeBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Tokens) != 1 || bundle.Tokens[0] != "256265" || len(bundle.Watchlist.Requirements) != 1 || bundle.Checksum == "" {
		t.Fatalf("bundle=%#v", bundle)
	}
	if err := os.WriteFile(filepath.Join(root, "watchlist.json"), []byte(`{}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuntimeBundle(path); err == nil {
		t.Fatal("changed pinned file accepted")
	}
}

func refJSON(value FileReference) string {
	return fmt.Sprintf(`{"path":%q,"sha256":%q}`, value.Path, value.SHA256)
}
