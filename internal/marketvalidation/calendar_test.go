package marketvalidation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestGenerateTradingCalendarUsesVerifiedPolicyAndExactDateCoverage(t *testing.T) {
	root := t.TempDir()
	sourceRaw := []byte("authoritative NSE 2026 holiday and CAS policy")
	sourcePath := filepath.Join(root, "nse-policy.pdf")
	if err := os.WriteFile(sourcePath, sourceRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(sourceRaw)
	policy := TradingCalendarPolicy{
		SchemaVersion: TradingCalendarPolicySchemaVersion,
		PolicyID:      "nse-2026-reviewed",
		Exchange:      "NSE",
		Timezone:      "Asia/Kolkata",
		PublishedAt:   time.Date(2025, 12, 23, 0, 0, 0, 0, time.UTC),
		EffectiveFrom: "2026-01-01",
		EffectiveTo:   "2026-12-31",
		Holidays:      []string{"2026-08-26"},
		Sessions: []CalendarPolicySession{
			{Open: "09:15:00", Close: "15:15:00", Kind: "REGULAR"},
			{Open: "15:15:00", Close: "15:35:00", Kind: "MODIFIED"},
			{Open: "15:35:00", Close: "16:00:00", Kind: "SPECIAL"},
		},
		Regimes: []CalendarPolicyRegime{
			{Open: "15:15:00", Close: "15:20:00", Regime: "PRE_CAS"},
			{Open: "15:20:00", Close: "15:35:00", Regime: "CAS_ACTIVE"},
			{Open: "15:35:00", Close: "16:00:00", Regime: "POST_CAS"},
		},
		Sources: []CalendarSourceReference{{CircularID: "NSE/FAOP/71777", Segment: "FUTURES_OPTIONS", URL: "https://nsearchives.nseindia.com/content/circulars/FAOP71777.pdf", Path: "nse-policy.pdf", SHA256: hex.EncodeToString(sum[:])}},
	}
	policyRaw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(root, "policy.json")
	if err = os.WriteFile(policyPath, policyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	calendarPath := filepath.Join(root, "calendar.json")
	calendarRaw, sources, classification, err := GenerateTradingCalendar(policyPath, calendarPath, "2026-08-13")
	if err != nil || classification != CalendarTradingDay {
		t.Fatalf("classification=%s err=%v", classification, err)
	}
	if err = os.WriteFile(calendarPath, calendarRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	approval, err := ApproveCalendar(calendarPath, sources, "2026-08-13", "2026-08-13", true)
	if err != nil || !approval.Approved || approval.TradingDays != 1 || approval.Holidays != 0 {
		t.Fatalf("approval=%+v err=%v", approval, err)
	}
	_, _, classification, err = GenerateTradingCalendar(policyPath, filepath.Join(root, "holiday.json"), "2026-08-26")
	if err != nil || classification != CalendarNonTradingDay {
		t.Fatalf("holiday classification=%s err=%v", classification, err)
	}
	if _, _, _, err = GenerateTradingCalendar(policyPath, calendarPath, "2027-01-01"); err == nil {
		t.Fatal("out-of-policy date accepted")
	}
	if err = os.WriteFile(sourcePath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = GenerateTradingCalendar(policyPath, calendarPath, "2026-08-13"); err == nil {
		t.Fatal("tampered policy source accepted")
	}
}
