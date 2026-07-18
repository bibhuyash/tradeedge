package calendar

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

func TestScheduleHandlesHolidayBreaksAndExpectedWindows(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Kolkata")
	date, _ := domain.NewCivilDate(2026, time.July, 20)
	holiday, _ := domain.NewCivilDate(2026, time.July, 21)
	schedule, err := New(Spec{
		Source:   Source{Name: "fixture", PublishedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
		Timezone: "Asia/Kolkata", EffectiveFrom: date, EffectiveTo: holiday,
		Days: []TradingDay{
			{
				Exchange: domain.ExchangeNSE, Date: date, Status: DayTrading,
				Sessions: []Session{
					{
						Open:  time.Date(2026, 7, 20, 9, 15, 0, 0, location),
						Close: time.Date(2026, 7, 20, 9, 30, 0, 0, location),
						Kind:  SessionModified,
					},
					{
						Open:  time.Date(2026, 7, 20, 10, 0, 0, 0, location),
						Close: time.Date(2026, 7, 20, 10, 15, 0, 0, location),
						Kind:  SessionSpecial,
					},
				},
			},
			{Exchange: domain.ExchangeNSE, Date: holiday, Status: DayHoliday},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	windows, err := schedule.ExpectedWindows(context.Background(), domain.ExchangeNSE, date, model.Interval5Minutes)
	if err != nil {
		t.Fatalf("ExpectedWindows() error = %v", err)
	}
	if len(windows) != 6 {
		t.Fatalf("window count = %d, want 6", len(windows))
	}
	if !windows[2].Close.Equal(time.Date(2026, 7, 20, 9, 30, 0, 0, location)) ||
		!windows[3].Open.Equal(time.Date(2026, 7, 20, 10, 0, 0, 0, location)) {
		t.Fatalf("windows cross session break: %#v", windows)
	}
	holidayWindows, err := schedule.ExpectedWindows(context.Background(), domain.ExchangeNSE, holiday, model.Interval1Minute)
	if err != nil || len(holidayWindows) != 0 {
		t.Fatalf("holiday windows = %#v, %v", holidayWindows, err)
	}
}

func TestScheduleRejectsMissingCoverageAndReportsOutOfRange(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Kolkata")
	first, _ := domain.NewCivilDate(2026, time.July, 20)
	second, _ := domain.NewCivilDate(2026, time.July, 21)
	_, err := New(Spec{
		Source:   Source{Name: "fixture", PublishedAt: time.Now()},
		Timezone: "Asia/Kolkata", EffectiveFrom: first, EffectiveTo: second,
		Days: []TradingDay{{
			Exchange: domain.ExchangeNSE, Date: first, Status: DayTrading,
			Sessions: []Session{{
				Open:  time.Date(2026, 7, 20, 9, 15, 0, 0, location),
				Close: time.Date(2026, 7, 20, 15, 30, 0, 0, location),
				Kind:  SessionRegular,
			}},
		}},
	})
	if !errors.Is(err, ErrTradingDayNotFound) {
		t.Fatalf("New() error = %v, want ErrTradingDayNotFound", err)
	}
}

func TestRepositoryRetainsCorrectedCalendarVersions(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Kolkata")
	date, _ := domain.NewCivilDate(2026, time.July, 20)
	build := func(closeHour int) *Schedule {
		schedule, err := New(Spec{
			Source:   Source{Name: "fixture", PublishedAt: time.Date(2026, 7, closeHour, 0, 0, 0, 0, time.UTC)},
			Timezone: "Asia/Kolkata", EffectiveFrom: date, EffectiveTo: date,
			Days: []TradingDay{{
				Exchange: domain.ExchangeNSE, Date: date, Status: DayTrading,
				Sessions: []Session{{
					Open:  time.Date(2026, 7, 20, 9, 15, 0, 0, location),
					Close: time.Date(2026, 7, 20, closeHour, 30, 0, 0, location),
					Kind:  SessionModified,
				}},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return schedule
	}
	first, second := build(14), build(15)
	repository := NewMemoryRepository()
	if err := repository.Put(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := repository.Put(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	current, _ := repository.Current(context.Background())
	historical, _ := repository.Get(context.Background(), first.Version())
	if current.Version() != second.Version() || historical.Version() != first.Version() {
		t.Fatalf("current = %s, historical = %s", current.Version(), historical.Version())
	}
}
