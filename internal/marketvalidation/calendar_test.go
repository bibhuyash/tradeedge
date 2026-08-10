package marketvalidation

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestCalendarApprovalRequiresExactCoverageAndCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calendar.json")
	raw := []byte(`{"schema_version":2,"source":"reviewed","published_at":"2026-08-09T00:00:00Z","timezone":"Asia/Kolkata","effective_from":"2026-08-10","effective_to":"2026-08-10","days":[{"exchange":"NSE","date":"2026-08-10","status":"TRADING","sessions":[{"open":"09:15:00","close":"15:30:00","kind":"REGULAR"}],"regimes":[{"open":"14:55:00","close":"15:00:00","regime":"PRE_CAS"},{"open":"15:00:00","close":"15:10:00","regime":"CAS_ACTIVE"},{"open":"15:10:00","close":"15:20:00","regime":"POST_CAS"}]}]}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sourceRaw := []byte("reviewed NSE circular")
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "circular.pdf"), sourceRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(sourceRaw)
	sources := CalendarSourceSet{SchemaVersion: CalendarSourcesSchemaVersion, Sources: []CalendarSourceReference{{CircularID: "NSE-CAS-2026", Segment: "CAPITAL_MARKET", URL: "https://nsearchives.nseindia.com/circular.pdf", Path: "circular.pdf", SHA256: hex.EncodeToString(sum[:])}}}
	approval, err := ApproveCalendar(path, sources, "2026-08-10", "2026-08-10", true)
	if err != nil || !approval.Approved || approval.LiveTradingAuthorized || approval.TradingDays != 1 {
		t.Fatalf("approval=%#v err=%v", approval, err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "circular.pdf"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApproveCalendar(path, sources, "2026-08-10", "2026-08-10", true); err == nil {
		t.Fatal("source checksum mismatch accepted")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "circular.pdf"), sourceRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ApproveCalendar(path, sources, "2026-08-10", "2026-08-11", true); err == nil {
		t.Fatal("coverage mismatch accepted")
	}
}
