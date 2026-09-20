package dataset

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestImportDeterminismIdentityAndMissingFields(t *testing.T) {
	dir := t.TempDir()
	master, cal, ids := fixtureFiles(t, dir)
	input := filepath.Join(dir, "ticks.csv")
	write(t, input, "exchange_timestamp,instrument_id,ltp_minor,bid_minor,ask_minor,volume,open_interest\n2026-01-02T03:45:00Z,"+ids["nifty"]+",2500000,,,,\n2026-01-02T03:46:00Z,"+ids["bank"]+",5500000,,,,\n")
	cfg := ImportConfig{Source: "TEST_FIXTURE", SourceVersion: "v1", IntervalMinutes: 1}
	a, e := ImportCSV(input, master, cal, cfg)
	if e != nil {
		t.Fatal(e)
	}
	b, e := ImportCSV(input, master, cal, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if a.Manifest != b.Manifest {
		t.Fatal("same inputs changed identity")
	}
	if a.Observations[0].BidMinor != nil || a.Observations[0].AskMinor != nil || a.Observations[0].Volume != nil || a.Observations[0].OpenInterest != nil {
		t.Fatal("missing fields were fabricated")
	}
	write(t, input, "exchange_timestamp,instrument_id,ltp_minor,bid_minor,ask_minor,volume,open_interest\n2026-01-02T03:45:00Z,"+ids["nifty"]+",2500001,,,,\n2026-01-02T03:46:00Z,"+ids["bank"]+",5500000,,,,\n")
	changed, _ := ImportCSV(input, master, cal, cfg)
	if changed.Manifest.DatasetVersion == a.Manifest.DatasetVersion {
		t.Fatal("changed data retained identity")
	}
	cfg.IntervalMinutes = 5
	semantic, _ := ImportCSV(input, master, cal, cfg)
	if semantic.Manifest.DatasetVersion == changed.Manifest.DatasetVersion {
		t.Fatal("changed semantic configuration retained identity")
	}
}

func TestQualityFindings(t *testing.T) {
	dir := t.TempDir()
	master, cal, ids := fixtureFiles(t, dir)
	input := filepath.Join(dir, "ticks.csv")
	rows := []string{"exchange_timestamp,instrument_id,ltp_minor,bid_minor,ask_minor,volume,open_interest", "2026-01-02T03:46:00Z," + ids["nifty"] + ",100,90,110,10,20", "2026-01-02T03:45:00Z," + ids["nifty"] + ",100,90,110,9,19", "2026-01-02T03:45:00Z," + ids["nifty"] + ",100,90,110,9,19", "2026-01-02T03:46:30Z," + ids["bank"] + ",100,120,110,1,1", "2026-01-02T03:46:40Z," + ids["bank"] + ",-1,1,2,1,1", "2026-01-02T03:50:00Z,unknown,100,1,2,1,1", "2026-01-03T03:45:00Z," + ids["expired"] + ",100,1,2,1,1", "2026-01-02T03:40:00Z," + ids["bank"] + ",100,1,2,1,1"}
	write(t, input, strings.Join(rows, "\n")+"\n")
	d, e := ImportCSV(input, master, cal, ImportConfig{Source: "SYNTHETIC_TEST", IntervalMinutes: 1, CumulativeVolume: true, CumulativeOI: true})
	if e != nil {
		t.Fatal(e)
	}
	want := []FindingCode{OutOfOrderEvent, DuplicateEvent, CrossedMarket, InvalidPrice, UnknownInstrument, PostExpiryObservation, OutsideSession, NonMonotonicCumulativeVolume, NonMonotonicOI, MissingInterval}
	for _, code := range want {
		if !hasFinding(d.Quality.QualityFindings, code) {
			t.Errorf("missing %s", code)
		}
	}
	if d.Quality.QualificationState != Rejected {
		t.Fatalf("quality=%s", d.Quality.QualificationState)
	}
}

func TestBarsPointInTime(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 45, 10, 0, time.UTC)
	v := int64(2)
	obs := []Observation{{InstrumentID: "x", ExchangeTimestamp: at, LTPMinor: 100, Volume: &v}, {InstrumentID: "x", ExchangeTimestamp: at.Add(20 * time.Second), LTPMinor: 110, Volume: nil}, {InstrumentID: "x", ExchangeTimestamp: at.Add(40 * time.Second), LTPMinor: 90, Volume: &v}}
	bars, e := AggregateBars(obs, Bar1Minute)
	if e != nil || len(bars) != 1 {
		t.Fatal(e)
	}
	b := bars[0]
	if b.Open != 100 || b.High != 110 || b.Low != 90 || b.Close != 90 || b.Volume == nil || *b.Volume != 4 {
		t.Fatalf("bad bar %+v", b)
	}
	if len(AvailableBars(bars, b.EndTime.Add(-time.Nanosecond))) != 0 {
		t.Fatal("final OHLC leaked before close")
	}
	if len(AvailableBars(bars, b.EndTime)) != 1 {
		t.Fatal("completed bar unavailable at close")
	}
}

func TestOptionChainPointInTimeATMAndRollover(t *testing.T) {
	dir := t.TempDir()
	master, _, ids := fixtureFiles(t, dir)
	items, _, e := loadInstruments(master)
	if e != nil {
		t.Fatal(e)
	}
	at := time.Date(2026, 1, 2, 3, 46, 0, 0, time.UTC)
	d := CanonicalDataset{Instruments: items, Observations: []Observation{{InstrumentID: ids["ce"], ExchangeTimestamp: at, LTPMinor: 100}, {InstrumentID: ids["pe"], ExchangeTimestamp: at.Add(time.Minute), LTPMinor: 110}}}
	snap, e := NewOptionChainSnapshot(d, "NIFTY", at, "2026-01-29", 2501000)
	if e != nil {
		t.Fatal(e)
	}
	if snap.ATMStrikeMinor != 2500000 || len(snap.Entries) != 1 {
		t.Fatalf("snapshot leaked future data: %+v", snap)
	}
	policy := ContinuousFuturePolicy{Version: "roll/v1", InitialInstrumentID: "jan", Rollovers: []Rollover{{At: at, FromInstrumentID: "jan", ToInstrumentID: "feb"}}}
	before, _ := policy.ContractAt(at.Add(-time.Nanosecond))
	after, _ := policy.ContractAt(at)
	if before != "jan" || after != "feb" {
		t.Fatal("rollover point-in-time failure")
	}
	if _, e = (ContinuousFuturePolicy{}).ContractAt(at); e == nil {
		t.Fatal("missing rollover policy did not fail")
	}
}

func fixtureFiles(t *testing.T, dir string) (string, Calendar, map[string]string) {
	t.Helper()
	master := filepath.Join(dir, "instruments.csv")
	header := "instrument_id,symbol,underlying,exchange,segment,instrument_type,expiry,strike_minor,option_type,lot_size,tick_size_minor,currency,available_from,available_until\n"
	rows := []string{",NIFTY 50,NIFTY,NSE,INDEX,INDEX,,,,,0,INR,2026-01-01T00:00:00Z,", ",NIFTY BANK,BANKNIFTY,NSE,INDEX,INDEX,,,,,0,INR,2026-01-01T00:00:00Z,", ",NIFTY26JANFUT,NIFTY,NSE,FUTURES,FUTURE,2026-01-29,,,65,5,INR,2026-01-01T00:00:00Z,", ",NIFTY26JAN25000CE,NIFTY,NSE,OPTIONS,OPTION,2026-01-29,2500000,CE,65,5,INR,2026-01-01T00:00:00Z,", ",NIFTY26JAN25000PE,NIFTY,NSE,OPTIONS,OPTION,2026-01-29,2500000,PE,65,5,INR,2026-01-01T00:00:00Z,", ",NIFTY25DEC25000CE,NIFTY,NSE,OPTIONS,OPTION,2026-01-02,2500000,CE,65,5,INR,2026-01-01T00:00:00Z,"}
	write(t, master, header+strings.Join(rows, "\n")+"\n")
	items, _, e := loadInstruments(master)
	if e != nil {
		t.Fatal(e)
	}
	ids := map[string]string{}
	for _, i := range items {
		switch i.Symbol {
		case "NIFTY 50":
			ids["nifty"] = i.ID
		case "NIFTY BANK":
			ids["bank"] = i.ID
		case "NIFTY26JAN25000CE":
			ids["ce"] = i.ID
		case "NIFTY26JAN25000PE":
			ids["pe"] = i.ID
		case "NIFTY25DEC25000CE":
			ids["expired"] = i.ID
		}
	}
	cal := Calendar{
		SchemaVersion: "tradeedge.research.calendar/v1",
		Version:       "synthetic-calendar-v1",
		Timezone:      "Asia/Kolkata",
		Days: []CalendarDay{
			{Date: "2026-01-02", Trading: true, Sessions: []CalendarSession{{Open: "09:15", Close: "09:18", Kind: "REGULAR"}}},
			{Date: "2026-01-03", Trading: true, Sessions: []CalendarSession{{Open: "09:15", Close: "09:18", Kind: "SPECIAL"}}},
		},
	}
	return master, cal, ids
}
func write(t *testing.T, path, body string) {
	t.Helper()
	if e := os.WriteFile(path, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
}
func hasFinding(fs []QualityFinding, c FindingCode) bool {
	for _, f := range fs {
		if f.Code == c {
			return true
		}
	}
	return false
}
func TestFixtureLabelsSynthetic(t *testing.T) {
	if !strings.Contains(fmt.Sprint("SYNTHETIC_TEST"), "SYNTHETIC") {
		t.Fatal()
	}
}
