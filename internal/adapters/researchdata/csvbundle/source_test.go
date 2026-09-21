package csvbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/research/dataset"
)

func TestMappedBundleImportIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	instruments := "source_instrument_id,instrument_id,symbol,underlying,exchange,segment,instrument_type,expiry,strike_minor,option_type,lot_size,tick_size_minor,currency,available_from,available_until\n" +
		"nifty-index,,NIFTY 50,NIFTY,NSE,INDEX,INDEX,,,,,0,INR,2025-12-01T00:00:00Z,2026-01-30T00:00:00Z\n" +
		"bank-index,,NIFTY BANK,BANKNIFTY,NSE,INDEX,INDEX,,,,,0,INR,2025-12-01T00:00:00Z,2026-01-30T00:00:00Z\n" +
		"nifty-fut,,NIFTY26JANFUT,NIFTY,NSE,FUTURES,FUTURE,2026-01-29,,,65,5,INR,2025-12-01T00:00:00Z,2026-01-30T00:00:00Z\n" +
		"bank-fut,,BANKNIFTY26JANFUT,BANKNIFTY,NSE,FUTURES,FUTURE,2026-01-29,,,30,5,INR,2025-12-01T00:00:00Z,2026-01-30T00:00:00Z\n"
	calendar := `{"schema_version":"tradeedge.research.calendar/v1","version":"nse-test","timezone":"Asia/Kolkata","days":[{"date":"2026-01-02","trading":true,"sessions":[{"open":"09:15","close":"09:17","kind":"REGULAR"}]}]}`
	bars := "ticker,when,o,h,l,c,vol,oi\n"
	for _, key := range []string{"nifty-index", "bank-index", "nifty-fut", "bank-fut"} {
		bars += key + ",2026-01-02T09:15:00+05:30,100,110,90,105,10,20\n" + key + ",2026-01-02T09:16:00+05:30,105,115,100,110,,21\n"
	}
	writeFile(t, filepath.Join(dir, "instruments.csv"), []byte(instruments))
	writeFile(t, filepath.Join(dir, "calendar.json"), []byte(calendar))
	writeFile(t, filepath.Join(dir, "bars.csv"), []byte(bars))
	m := Manifest{SchemaVersion: SchemaVersion, Provider: "LICENSED_VENDOR", SourceVersion: "export-v1", DataClass: "EXTERNAL_MARKET_DATA", AcquiredAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Market: "NSE", RequestedStart: "2026-01-02", RequestedEnd: "2026-01-02", Interval: "1m", Timezone: "Asia/Kolkata", TimestampSemantics: "START", PriceUnit: "MINOR", Files: Files{Bars: ref(t, dir, "bars.csv"), Instruments: ref(t, dir, "instruments.csv"), Calendar: ref(t, dir, "calendar.json")}, Columns: Columns{Instrument: "ticker", Timestamp: "when", Open: "o", High: "h", Low: "l", Close: "c", Volume: "vol", OpenInterest: "oi"}}
	manifestPath := filepath.Join(dir, "bundle.json")
	saveManifest(t, manifestPath, m)
	a, err := dataset.ImportSource(New(manifestPath))
	if err != nil {
		t.Fatal(err)
	}
	m.AcquiredAt = m.AcquiredAt.Add(time.Hour)
	saveManifest(t, manifestPath, m)
	b, err := dataset.ImportSource(New(manifestPath))
	if err != nil {
		t.Fatal(err)
	}
	if a.Manifest.DatasetVersion != b.Manifest.DatasetVersion || a.Manifest.RawChecksum != b.Manifest.RawChecksum || a.Manifest.NormalizedChecksum != b.Manifest.NormalizedChecksum {
		t.Fatal("acquisition time changed deterministic identity")
	}
	if !a.Manifest.RealData || len(a.Bars) != 8 {
		t.Fatalf("bad normalized bundle: %+v", a)
	}
	missingOptional := false
	for _, bar := range a.Bars {
		if bar.Volume == nil && bar.OpenInterest != nil {
			missingOptional = true
		}
	}
	if !missingOptional {
		t.Fatal("optional volume/OI fields were fabricated")
	}
	if a.Quality.QualificationState != dataset.ResearchReady {
		t.Fatalf("quality=%s", a.Quality.QualificationState)
	}
	if _, err = dataset.Revalidate(a); err != nil {
		t.Fatal(err)
	}
	a.Quality.QualificationState = dataset.Rejected
	recomputed, err := dataset.Revalidate(a)
	if err != nil || recomputed.QualificationState != dataset.ResearchReady {
		t.Fatal("stored quality status was trusted")
	}
	a.Manifest.RealData = false
	if _, err = dataset.Revalidate(a); err == nil {
		t.Fatal("tampered real-data provenance was accepted")
	}
}

func TestMappedBundleRejectsChecksumAndUnsupportedUnits(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "x"), []byte("x"))
	m := Manifest{SchemaVersion: SchemaVersion, Provider: "VENDOR", DataClass: "EXTERNAL_MARKET_DATA", AcquiredAt: time.Now().UTC(), Market: "NSE", Interval: "1m", Timezone: "Asia/Kolkata", TimestampSemantics: "START", PriceUnit: "RUPEES", Files: Files{Bars: FileRef{Path: "x", SourceID: "x", SHA256: hash([]byte("x"))}, Instruments: FileRef{Path: "x", SourceID: "x", SHA256: hash([]byte("x"))}, Calendar: FileRef{Path: "x", SourceID: "x", SHA256: hash([]byte("x"))}}, Columns: Columns{Instrument: "i", Timestamp: "t", Open: "o", High: "h", Low: "l", Close: "c"}}
	p := filepath.Join(dir, "bundle.json")
	saveManifest(t, p, m)
	if _, err := New(p).Load(); err == nil {
		t.Fatal("unsupported price unit accepted")
	}
	m.PriceUnit = "MINOR"
	m.Files.Bars.SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	saveManifest(t, p, m)
	if _, err := New(p).Load(); err == nil {
		t.Fatal("bad checksum accepted")
	}
}

func ref(t *testing.T, dir, name string) FileRef {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return FileRef{Path: name, SourceID: "vendor:" + name, SHA256: hash(raw)}
}
func hash(raw []byte) string { s := sha256.Sum256(raw); return hex.EncodeToString(s[:]) }
func writeFile(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func saveManifest(t *testing.T, path string, m Manifest) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, raw)
}
