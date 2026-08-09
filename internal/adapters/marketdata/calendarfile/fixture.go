package calendarfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
)

type fixture struct {
	SchemaVersion int          `json:"schema_version"`
	Source        string       `json:"source"`
	PublishedAt   time.Time    `json:"published_at"`
	Timezone      string       `json:"timezone"`
	EffectiveFrom string       `json:"effective_from"`
	EffectiveTo   string       `json:"effective_to"`
	Days          []fixtureDay `json:"days"`
}

type fixtureDay struct {
	Exchange domain.Exchange    `json:"exchange"`
	Date     string             `json:"date"`
	Status   calendar.DayStatus `json:"status"`
	Sessions []fixtureSession   `json:"sessions,omitempty"`
	Regimes  []fixtureRegime    `json:"regimes,omitempty"`
}

type fixtureRegime struct {
	Open   string          `json:"open"`
	Close  string          `json:"close"`
	Regime calendar.Regime `json:"regime"`
}

type fixtureSession struct {
	Open  string               `json:"open"`
	Close string               `json:"close"`
	Kind  calendar.SessionKind `json:"kind"`
	Note  string               `json:"note,omitempty"`
}

func Load(path string) (*calendar.Schedule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Decode(data)
}

// LoadWithFallback permits a previously verified calendar only when it has an
// explicit entry for the requested date. It never treats absent coverage as a
// closed market.
func LoadWithFallback(
	ctx context.Context,
	path string,
	previous calendar.Calendar,
	exchange domain.Exchange,
	date domain.CivilDate,
) (calendar.Calendar, error) {
	schedule, err := Load(path)
	if err == nil {
		return schedule, nil
	}
	if previous == nil {
		return nil, err
	}
	if _, fallbackErr := previous.Day(ctx, exchange, date); fallbackErr != nil {
		return nil, calendar.ErrCalendarOutOfRange
	}
	return previous, nil
}

func Decode(data []byte) (*calendar.Schedule, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var encoded fixture
	if err := decoder.Decode(&encoded); err != nil {
		return nil, fmt.Errorf("%w: decode fixture: %v", calendar.ErrInvalidCalendar, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, calendar.ErrInvalidCalendar
	}
	if (encoded.SchemaVersion != 1 && encoded.SchemaVersion != 2) || encoded.Timezone != "Asia/Kolkata" {
		return nil, calendar.ErrInvalidCalendar
	}
	location, err := time.LoadLocation(encoded.Timezone)
	if err != nil {
		return nil, err
	}
	effectiveFrom, err := parseDate(encoded.EffectiveFrom)
	if err != nil {
		return nil, err
	}
	effectiveTo, err := parseDate(encoded.EffectiveTo)
	if err != nil {
		return nil, err
	}
	days := make([]calendar.TradingDay, 0, len(encoded.Days))
	for _, item := range encoded.Days {
		date, err := parseDate(item.Date)
		if err != nil {
			return nil, err
		}
		sessions := make([]calendar.Session, 0, len(item.Sessions))
		for _, value := range item.Sessions {
			open, err := parseLocalTime(date, value.Open, location)
			if err != nil {
				return nil, err
			}
			closeTime, err := parseLocalTime(date, value.Close, location)
			if err != nil {
				return nil, err
			}
			sessions = append(sessions, calendar.Session{
				Open: open, Close: closeTime, Kind: value.Kind, Note: strings.TrimSpace(value.Note),
			})
		}
		regimes := make([]calendar.RegimeWindow, 0, len(item.Regimes))
		for _, value := range item.Regimes {
			if encoded.SchemaVersion < 2 {
				return nil, calendar.ErrInvalidCalendar
			}
			open, err := parseLocalTime(date, value.Open, location)
			if err != nil {
				return nil, err
			}
			closeTime, err := parseLocalTime(date, value.Close, location)
			if err != nil {
				return nil, err
			}
			regimes = append(regimes, calendar.RegimeWindow{Open: open, Close: closeTime, Regime: value.Regime})
		}
		days = append(days, calendar.TradingDay{
			Exchange: item.Exchange, Date: date, Status: item.Status, Sessions: sessions, Regimes: regimes,
		})
	}
	return calendar.New(calendar.Spec{
		Source:   calendar.Source{Name: encoded.Source, PublishedAt: encoded.PublishedAt},
		Timezone: encoded.Timezone, EffectiveFrom: effectiveFrom, EffectiveTo: effectiveTo, Days: days,
	})
}

func parseDate(value string) (domain.CivilDate, error) {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return domain.CivilDate{}, calendar.ErrInvalidCalendar
	}
	return domain.NewCivilDate(parsed.Year(), parsed.Month(), parsed.Day())
}

func parseLocalTime(date domain.CivilDate, value string, location *time.Location) (time.Time, error) {
	parsed, err := time.Parse("15:04:05", value)
	if err != nil {
		return time.Time{}, calendar.ErrInvalidCalendar
	}
	return time.Date(date.Year(), date.Month(), date.Day(),
		parsed.Hour(), parsed.Minute(), parsed.Second(), 0, location), nil
}
