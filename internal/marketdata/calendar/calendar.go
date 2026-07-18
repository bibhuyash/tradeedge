package calendar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

var (
	ErrInvalidCalendar    = errors.New("invalid market calendar")
	ErrCalendarOutOfRange = errors.New("market calendar date is outside verified coverage")
	ErrTradingDayNotFound = errors.New("market calendar has no explicit trading-day entry")
)

type Version string

type DayStatus string

const (
	DayTrading DayStatus = "TRADING"
	DayHoliday DayStatus = "HOLIDAY"
)

type SessionKind string

const (
	SessionRegular  SessionKind = "REGULAR"
	SessionSpecial  SessionKind = "SPECIAL"
	SessionModified SessionKind = "MODIFIED"
)

type Source struct {
	Name        string
	PublishedAt time.Time
}

type Session struct {
	Open  time.Time
	Close time.Time
	Kind  SessionKind
	Note  string
}

type TradingDay struct {
	Exchange domain.Exchange
	Date     domain.CivilDate
	Status   DayStatus
	Sessions []Session
}

type Window struct {
	Open  time.Time
	Close time.Time
}

type Spec struct {
	Source        Source
	Timezone      string
	EffectiveFrom domain.CivilDate
	EffectiveTo   domain.CivilDate
	Days          []TradingDay
}

type Calendar interface {
	Version() Version
	Source() Source
	Timezone() string
	Coverage() (domain.CivilDate, domain.CivilDate)
	Day(ctx context.Context, exchange domain.Exchange, date domain.CivilDate) (TradingDay, error)
	ExpectedWindows(
		ctx context.Context,
		exchange domain.Exchange,
		date domain.CivilDate,
		interval model.CandleInterval,
	) ([]Window, error)
	SessionAt(ctx context.Context, exchange domain.Exchange, at time.Time) (model.MarketSession, bool, error)
}

type Schedule struct {
	version       Version
	source        Source
	timezone      string
	location      *time.Location
	effectiveFrom domain.CivilDate
	effectiveTo   domain.CivilDate
	days          map[string]TradingDay
}

func New(spec Spec) (*Schedule, error) {
	if strings.TrimSpace(spec.Source.Name) == "" || spec.Source.PublishedAt.IsZero() ||
		spec.Timezone != "Asia/Kolkata" || spec.EffectiveFrom.IsZero() ||
		spec.EffectiveTo.IsZero() || compareDate(spec.EffectiveFrom, spec.EffectiveTo) > 0 ||
		len(spec.Days) == 0 {
		return nil, ErrInvalidCalendar
	}
	location, err := time.LoadLocation(spec.Timezone)
	if err != nil {
		return nil, fmt.Errorf("%w: load timezone: %v", ErrInvalidCalendar, err)
	}
	days := make(map[string]TradingDay, len(spec.Days))
	canonical := make([]string, 0, len(spec.Days))
	exchanges := make(map[domain.Exchange]struct{})
	for _, day := range spec.Days {
		if day.Exchange != domain.ExchangeNSE || day.Date.IsZero() ||
			compareDate(day.Date, spec.EffectiveFrom) < 0 ||
			compareDate(day.Date, spec.EffectiveTo) > 0 ||
			(day.Status != DayTrading && day.Status != DayHoliday) ||
			(day.Status == DayHoliday && len(day.Sessions) != 0) ||
			(day.Status == DayTrading && len(day.Sessions) == 0) {
			return nil, ErrInvalidCalendar
		}
		key := dayKey(day.Exchange, day.Date)
		if _, exists := days[key]; exists {
			return nil, ErrInvalidCalendar
		}
		sessions := append([]Session(nil), day.Sessions...)
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].Open.Before(sessions[j].Open) })
		for index, session := range sessions {
			if !session.Open.Before(session.Close) ||
				session.Open.Location().String() != location.String() ||
				session.Close.Location().String() != location.String() ||
				session.Open.Second() != 0 || session.Open.Nanosecond() != 0 ||
				session.Close.Second() != 0 || session.Close.Nanosecond() != 0 ||
				(session.Kind != SessionRegular && session.Kind != SessionSpecial &&
					session.Kind != SessionModified) ||
				session.Open.Year() != day.Date.Year() ||
				session.Open.Month() != day.Date.Month() ||
				session.Open.Day() != day.Date.Day() ||
				(index > 0 && session.Open.Before(sessions[index-1].Close)) {
				return nil, ErrInvalidCalendar
			}
		}
		day.Sessions = sessions
		days[key] = cloneDay(day)
		exchanges[day.Exchange] = struct{}{}
		parts := []string{string(day.Exchange), day.Date.String(), string(day.Status)}
		for _, session := range sessions {
			parts = append(parts,
				session.Open.Format(time.RFC3339Nano),
				session.Close.Format(time.RFC3339Nano),
				string(session.Kind),
				session.Note,
			)
		}
		canonical = append(canonical, strings.Join(parts, "|"))
	}
	for exchange := range exchanges {
		for date := spec.EffectiveFrom; compareDate(date, spec.EffectiveTo) <= 0; date = nextDate(date) {
			if _, found := days[dayKey(exchange, date)]; !found {
				return nil, fmt.Errorf("%w: %s %s", ErrTradingDayNotFound, exchange, date)
			}
		}
	}
	sort.Strings(canonical)
	payload := fmt.Sprintf("v1|%s|%s|%s|%s|%s|%s",
		spec.Source.Name,
		spec.Source.PublishedAt.UTC().Format(time.RFC3339Nano),
		spec.Timezone,
		spec.EffectiveFrom,
		spec.EffectiveTo,
		strings.Join(canonical, "\n"),
	)
	digest := sha256.Sum256([]byte(payload))
	return &Schedule{
		version:       Version(hex.EncodeToString(digest[:])),
		source:        Source{Name: spec.Source.Name, PublishedAt: spec.Source.PublishedAt.UTC()},
		timezone:      spec.Timezone,
		location:      location,
		effectiveFrom: spec.EffectiveFrom,
		effectiveTo:   spec.EffectiveTo,
		days:          days,
	}, nil
}

func (s *Schedule) Version() Version         { return s.version }
func (s *Schedule) Source() Source           { return s.source }
func (s *Schedule) Timezone() string         { return s.timezone }
func (s *Schedule) Location() *time.Location { return s.location }

func (s *Schedule) Coverage() (domain.CivilDate, domain.CivilDate) {
	return s.effectiveFrom, s.effectiveTo
}

func (s *Schedule) Day(
	ctx context.Context,
	exchange domain.Exchange,
	date domain.CivilDate,
) (TradingDay, error) {
	if err := ctx.Err(); err != nil {
		return TradingDay{}, err
	}
	if compareDate(date, s.effectiveFrom) < 0 || compareDate(date, s.effectiveTo) > 0 {
		return TradingDay{}, ErrCalendarOutOfRange
	}
	day, found := s.days[dayKey(exchange, date)]
	if !found {
		return TradingDay{}, ErrTradingDayNotFound
	}
	return cloneDay(day), nil
}

func (s *Schedule) SessionAt(
	ctx context.Context,
	exchange domain.Exchange,
	at time.Time,
) (model.MarketSession, bool, error) {
	local := at.In(s.location)
	date, err := domain.NewCivilDate(local.Year(), local.Month(), local.Day())
	if err != nil {
		return model.MarketSession{}, false, err
	}
	day, err := s.Day(ctx, exchange, date)
	if err != nil {
		return model.MarketSession{}, false, err
	}
	if day.Status == DayHoliday {
		return model.MarketSession{}, false, nil
	}
	for _, session := range day.Sessions {
		if !at.Before(session.Open) && at.Before(session.Close) {
			return model.MarketSession{
				Exchange: exchange, TradingDate: date,
				Open: session.Open, Close: session.Close, Version: string(s.version),
			}, true, nil
		}
	}
	return model.MarketSession{}, false, nil
}

func (s *Schedule) ExpectedWindows(
	ctx context.Context,
	exchange domain.Exchange,
	date domain.CivilDate,
	interval model.CandleInterval,
) ([]Window, error) {
	day, err := s.Day(ctx, exchange, date)
	if err != nil {
		return nil, err
	}
	if day.Status == DayHoliday {
		return nil, nil
	}
	duration, valid := interval.Duration()
	if !valid {
		return nil, model.ErrInvalidEvent
	}
	if interval == model.Interval1Day {
		return []Window{{Open: day.Sessions[0].Open, Close: day.Sessions[len(day.Sessions)-1].Close}}, nil
	}
	var windows []Window
	for _, session := range day.Sessions {
		for open := session.Open; !open.Add(duration).After(session.Close); open = open.Add(duration) {
			windows = append(windows, Window{Open: open, Close: open.Add(duration)})
		}
	}
	return windows, nil
}

func dayKey(exchange domain.Exchange, date domain.CivilDate) string {
	return string(exchange) + "|" + date.String()
}

func cloneDay(day TradingDay) TradingDay {
	day.Sessions = append([]Session(nil), day.Sessions...)
	return day
}

func compareDate(left, right domain.CivilDate) int {
	return strings.Compare(left.String(), right.String())
}

func nextDate(date domain.CivilDate) domain.CivilDate {
	next := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	result, _ := domain.NewCivilDate(next.Year(), next.Month(), next.Day())
	return result
}
