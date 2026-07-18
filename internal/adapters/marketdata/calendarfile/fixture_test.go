package calendarfile

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

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
