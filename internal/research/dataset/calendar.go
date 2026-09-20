package dataset

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"time"
)

type Calendar struct {
	SchemaVersion string        `json:"schema_version"`
	Version       string        `json:"version"`
	Timezone      string        `json:"timezone"`
	Days          []CalendarDay `json:"days"`
}
type CalendarDay struct {
	Date     string            `json:"date"`
	Trading  bool              `json:"trading"`
	Sessions []CalendarSession `json:"sessions,omitempty"`
}
type CalendarSession struct {
	Open  string `json:"open"`
	Close string `json:"close"`
	Kind  string `json:"kind"`
}

func LoadCalendar(path string) (Calendar, error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return Calendar{}, e
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var c Calendar
	if d.Decode(&c) != nil {
		return Calendar{}, ErrInvalid
	}
	var x any
	if !errors.Is(d.Decode(&x), io.EOF) || c.SchemaVersion != "tradeedge.research.calendar/v1" || c.Version == "" || c.Timezone != "Asia/Kolkata" || len(c.Days) == 0 {
		return Calendar{}, ErrInvalid
	}
	sort.Slice(c.Days, func(i, j int) bool { return c.Days[i].Date < c.Days[j].Date })
	for _, day := range c.Days {
		if _, e = time.Parse("2006-01-02", day.Date); e != nil || day.Trading && len(day.Sessions) == 0 || !day.Trading && len(day.Sessions) > 0 {
			return Calendar{}, ErrInvalid
		}
		for _, s := range day.Sessions {
			if s.Kind != "REGULAR" && s.Kind != "SPECIAL" && s.Kind != "MODIFIED" {
				return Calendar{}, ErrInvalid
			}
			if _, _, e = sessionTimes(c, day, s); e != nil {
				return Calendar{}, e
			}
		}
	}
	return c, nil
}
func sessionTimes(c Calendar, d CalendarDay, s CalendarSession) (time.Time, time.Time, error) {
	loc, e := time.LoadLocation(c.Timezone)
	if e != nil {
		return time.Time{}, time.Time{}, e
	}
	a, e := time.ParseInLocation("2006-01-02 15:04", d.Date+" "+s.Open, loc)
	if e != nil {
		return time.Time{}, time.Time{}, ErrInvalid
	}
	b, e := time.ParseInLocation("2006-01-02 15:04", d.Date+" "+s.Close, loc)
	if e != nil || !b.After(a) {
		return time.Time{}, time.Time{}, ErrInvalid
	}
	return a, b, nil
}
func (c Calendar) sessionAt(at time.Time) (CalendarDay, bool) {
	loc, _ := time.LoadLocation(c.Timezone)
	date := at.In(loc).Format("2006-01-02")
	for _, d := range c.Days {
		if d.Date != date {
			continue
		}
		if !d.Trading {
			return d, false
		}
		for _, s := range d.Sessions {
			a, b, _ := sessionTimes(c, d, s)
			if !at.Before(a) && at.Before(b) {
				return d, true
			}
		}
		return d, false
	}
	return CalendarDay{}, false
}
