package calendarfile

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
)

func TestDecodeStrictVersionedFixture(t *testing.T) {
	data := []byte(`{
		"schema_version":1,
		"source":"test",
		"published_at":"2026-07-01T00:00:00Z",
		"timezone":"Asia/Kolkata",
		"effective_from":"2026-07-20",
		"effective_to":"2026-07-21",
		"days":[
			{"exchange":"NSE","date":"2026-07-20","status":"TRADING","sessions":[
				{"open":"09:15:00","close":"15:30:00","kind":"REGULAR"}
			]},
			{"exchange":"NSE","date":"2026-07-21","status":"HOLIDAY"}
		]
	}`)
	schedule, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	holiday, _ := domain.NewCivilDate(2026, 7, 21)
	day, err := schedule.Day(context.Background(), domain.ExchangeNSE, holiday)
	if err != nil || day.Status != calendar.DayHoliday || schedule.Version() == "" {
		t.Fatalf("holiday = %#v, version=%q, error=%v", day, schedule.Version(), err)
	}
}

func TestDecodeVersionTwoCASRegimes(t *testing.T) {
	data := []byte(`{"schema_version":2,"source":"test","published_at":"2026-07-01T00:00:00Z","timezone":"Asia/Kolkata","effective_from":"2026-07-20","effective_to":"2026-07-20","days":[{"exchange":"NSE","date":"2026-07-20","status":"TRADING","sessions":[{"open":"09:15:00","close":"15:30:00","kind":"REGULAR"}],"regimes":[{"open":"14:55:00","close":"15:00:00","regime":"PRE_CAS"},{"open":"15:00:00","close":"15:10:00","regime":"CAS_ACTIVE"},{"open":"15:10:00","close":"15:20:00","regime":"POST_CAS"}]}]}`)
	schedule, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	location, _ := time.LoadLocation("Asia/Kolkata")
	regime, inside, err := schedule.RegimeAt(context.Background(), domain.ExchangeNSE, time.Date(2026, 7, 20, 15, 1, 0, 0, location))
	if err != nil || !inside || regime != calendar.RegimeCAS {
		t.Fatalf("regime=%s inside=%v err=%v", regime, inside, err)
	}
}

func TestLoadRejectsOverlappingSessionFixture(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "tests", "testdata", "marketdata",
		"calendar-corrupt-overlap.json")
	if _, err := Load(path); !errors.Is(err, calendar.ErrInvalidCalendar) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadFallbackRequiresExplicitPreviousCoverage(t *testing.T) {
	data := []byte(`{
		"schema_version":1,"source":"test","published_at":"2026-07-01T00:00:00Z",
		"timezone":"Asia/Kolkata","effective_from":"2026-07-20","effective_to":"2026-07-20",
		"days":[{"exchange":"NSE","date":"2026-07-20","status":"HOLIDAY"}]
	}`)
	previous, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	covered, _ := domain.NewCivilDate(2026, 7, 20)
	fallback, err := LoadWithFallback(context.Background(), filepath.Join(t.TempDir(), "missing.json"),
		previous, domain.ExchangeNSE, covered)
	if err != nil || fallback.Version() != previous.Version() {
		t.Fatalf("fallback = %#v, %v", fallback, err)
	}
	outside, _ := domain.NewCivilDate(2026, 7, 21)
	if _, err := LoadWithFallback(context.Background(), filepath.Join(t.TempDir(), "missing.json"),
		previous, domain.ExchangeNSE, outside); !errors.Is(err, calendar.ErrCalendarOutOfRange) {
		t.Fatalf("out-of-range fallback error = %v", err)
	}
}

func TestDecodeRejectsUnknownAndMissingDates(t *testing.T) {
	_, err := Decode([]byte(`{
		"schema_version":1,
		"source":"test",
		"published_at":"2026-07-01T00:00:00Z",
		"timezone":"Asia/Kolkata",
		"effective_from":"2026-07-20",
		"effective_to":"2026-07-21",
		"days":[{"exchange":"NSE","date":"2026-07-20","status":"HOLIDAY"}],
		"unexpected":true
	}`))
	if !errors.Is(err, calendar.ErrInvalidCalendar) {
		t.Fatalf("Decode() error = %v", err)
	}
}
