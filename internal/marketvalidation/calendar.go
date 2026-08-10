package marketvalidation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/adapters/marketdata/calendarfile"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
)

const CalendarSourcesSchemaVersion = "market-validation-calendar-sources/v1"

type CalendarSourceSet struct {
	SchemaVersion string                    `json:"schema_version"`
	Sources       []CalendarSourceReference `json:"sources"`
}

type CalendarSourceReference struct {
	CircularID string `json:"circular_id"`
	Segment    string `json:"segment"`
	URL        string `json:"url"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
}

type CalendarApproval struct {
	SchemaVersion         string                    `json:"schema_version"`
	CalendarVersion       string                    `json:"calendar_version"`
	CalendarSHA256        string                    `json:"calendar_sha256"`
	EffectiveFrom         string                    `json:"effective_from"`
	EffectiveTo           string                    `json:"effective_to"`
	TradingDays           int                       `json:"trading_days"`
	Holidays              int                       `json:"holidays"`
	CASRequired           bool                      `json:"cas_required"`
	Sources               []CalendarSourceReference `json:"sources"`
	Approved              bool                      `json:"approved"`
	LiveTradingAuthorized bool                      `json:"live_trading_authorized"`
}

func DecodeCalendarSources(raw []byte) (CalendarSourceSet, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value CalendarSourceSet
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		value.SchemaVersion != CalendarSourcesSchemaVersion || len(value.Sources) == 0 || len(value.Sources) > 32 {
		return CalendarSourceSet{}, ErrInvalidRecord
	}
	seen := map[string]bool{}
	for index := range value.Sources {
		item := &value.Sources[index]
		item.CircularID, item.Segment, item.URL, item.Path, item.SHA256 = strings.TrimSpace(item.CircularID), strings.TrimSpace(item.Segment), strings.TrimSpace(item.URL), strings.TrimSpace(item.Path), strings.ToLower(strings.TrimSpace(item.SHA256))
		if item.CircularID == "" || item.Segment == "" || !strings.HasPrefix(item.URL, "https://") || item.Path == "" || unsafeIdentity(item.Path) || !validDigest(item.SHA256) || seen[item.CircularID] {
			return CalendarSourceSet{}, ErrInvalidRecord
		}
		seen[item.CircularID] = true
	}
	sort.Slice(value.Sources, func(i, j int) bool { return value.Sources[i].CircularID < value.Sources[j].CircularID })
	return value, nil
}

func ApproveCalendar(calendarPath string, sources CalendarSourceSet, from, to string, casRequired bool) (CalendarApproval, error) {
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return CalendarApproval{}, ErrInvalidRecord
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil || end.Before(start) || end.Sub(start) > 366*24*time.Hour {
		return CalendarApproval{}, ErrInvalidRecord
	}
	raw, err := os.ReadFile(calendarPath)
	if err != nil {
		return CalendarApproval{}, err
	}
	for _, source := range sources.Sources {
		path := source.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(calendarPath), path)
		}
		sourceRaw, sourceErr := os.ReadFile(path)
		if sourceErr != nil {
			return CalendarApproval{}, sourceErr
		}
		sum := sha256.Sum256(sourceRaw)
		if hex.EncodeToString(sum[:]) != source.SHA256 {
			return CalendarApproval{}, ErrInvalidRecord
		}
	}
	schedule, err := calendarfile.Decode(raw)
	if err != nil {
		return CalendarApproval{}, err
	}
	coverageFrom, coverageTo := schedule.Coverage()
	if coverageFrom.String() != from || coverageTo.String() != to {
		return CalendarApproval{}, calendar.ErrCalendarOutOfRange
	}
	result := CalendarApproval{SchemaVersion: "market-validation-calendar-approval/v1", CalendarVersion: string(schedule.Version()), EffectiveFrom: from, EffectiveTo: to, CASRequired: casRequired, Sources: append([]CalendarSourceReference(nil), sources.Sources...), Approved: true, LiveTradingAuthorized: false}
	sum := sha256.Sum256(raw)
	result.CalendarSHA256 = hex.EncodeToString(sum[:])
	for at := start; !at.After(end); at = at.AddDate(0, 0, 1) {
		date, _ := domain.NewCivilDate(at.Year(), at.Month(), at.Day())
		day, dayErr := schedule.Day(context.Background(), domain.ExchangeNSE, date)
		if dayErr != nil {
			return CalendarApproval{}, dayErr
		}
		if day.Status == calendar.DayHoliday {
			result.Holidays++
			continue
		}
		result.TradingDays++
		if casRequired {
			if len(day.Regimes) != 3 || day.Regimes[0].Regime != calendar.RegimePreCAS || day.Regimes[1].Regime != calendar.RegimeCAS || day.Regimes[2].Regime != calendar.RegimePostCAS {
				return CalendarApproval{}, ErrInvalidRecord
			}
		}
	}
	if result.TradingDays == 0 {
		return CalendarApproval{}, ErrInvalidRecord
	}
	return result, nil
}
