package dataset

import (
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

func TestV2BarQualityAndAvailability(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 45, 0, 0, time.UTC)
	until := start.Add(24 * time.Hour)
	instrument := Instrument{ID: "future", Symbol: "NIFTY26JANFUT", Underlying: "NIFTY", Exchange: domain.ExchangeNSE, Segment: domain.SegmentFutures, Type: domain.InstrumentFuture, Expiry: "2026-01-29", LotSize: intPtr(65), TickSizeMinor: 5, Currency: "INR", AvailableFrom: start.Add(-time.Hour), AvailableUntil: &until}
	calendar := Calendar{SchemaVersion: "tradeedge.research.calendar/v1", Version: "test", Timezone: "Asia/Kolkata", Days: []CalendarDay{{Date: "2026-01-02", Trading: true, Sessions: []CalendarSession{{Open: "09:15", Close: "09:17", Kind: "REGULAR"}}}}}
	volume, oi := int64(10), int64(20)
	bars := []HistoricalBar{{SourceSequence: 1, InstrumentID: "future", StartTime: start, EndTime: start.Add(time.Minute), OpenMinor: 100, HighMinor: 110, LowMinor: 90, CloseMinor: 105, Volume: &volume, OpenInterest: &oi}}
	findings := ValidateBars([]Instrument{instrument}, bars, calendar, time.Minute)
	if !hasFinding(findings, MissingInterval) {
		t.Fatal("missing one-minute bar was not detected")
	}
	d := CanonicalDataset{Bars: bars}
	if got := d.CompletedObservations(start.Add(time.Minute - time.Nanosecond)); len(got) != 0 {
		t.Fatal("bar leaked before close")
	}
	got := d.CompletedObservations(start.Add(time.Minute))
	if len(got) != 1 || got[0].LTPMinor != 105 || got[0].BidMinor != nil || got[0].AskMinor != nil {
		t.Fatalf("bad completed observation: %+v", got)
	}

	bad := bars[0]
	bad.LowMinor = 106
	bad.Volume = intPtr(-1)
	bad.OpenInterest = intPtr(-1)
	findings = ValidateBars([]Instrument{instrument}, []HistoricalBar{bad}, calendar, time.Minute)
	for _, code := range []FindingCode{InvalidOHLC, InvalidVolume, InvalidOpenInterest} {
		if !hasFinding(findings, code) {
			t.Errorf("missing %s", code)
		}
	}
}

func TestV2DuplicateContractAndExpiryBoundaries(t *testing.T) {
	start := time.Date(2026, 1, 30, 3, 45, 0, 0, time.UTC)
	i := Instrument{ID: "a", Symbol: "NIFTY26JANFUT", Underlying: "NIFTY", Exchange: domain.ExchangeNSE, Segment: domain.SegmentFutures, Type: domain.InstrumentFuture, Expiry: "2026-01-29", LotSize: intPtr(65), TickSizeMinor: 5, Currency: "INR", AvailableFrom: start.Add(-48 * time.Hour)}
	j := i
	j.ID = "b"
	c := Calendar{SchemaVersion: "tradeedge.research.calendar/v1", Version: "test", Timezone: "Asia/Kolkata", Days: []CalendarDay{{Date: "2026-01-30", Trading: true, Sessions: []CalendarSession{{Open: "09:15", Close: "09:16", Kind: "REGULAR"}}}}}
	b := HistoricalBar{InstrumentID: "a", StartTime: start, EndTime: start.Add(time.Minute), OpenMinor: 1, HighMinor: 1, LowMinor: 1, CloseMinor: 1}
	f := ValidateBars([]Instrument{i, j}, []HistoricalBar{b}, c, time.Minute)
	if !hasFinding(f, DuplicateContract) || !hasFinding(f, ExpiryInconsistency) {
		t.Fatalf("missing contract findings: %+v", f)
	}
}

func TestDegradedAndRejectedDatasetsFailClosed(t *testing.T) {
	for _, state := range []QualificationState{Degraded, Rejected} {
		d := CanonicalDataset{Quality: DatasetQualityReport{QualificationState: state}}
		if _, err := d.HistoricalSource(); err == nil {
			t.Fatalf("%s dataset entered normal research", state)
		}
	}
}

func intPtr(v int64) *int64 { return &v }
