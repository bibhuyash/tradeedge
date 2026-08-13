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

const (
	CalendarSourcesSchemaVersion       = "market-validation-calendar-sources/v1"
	TradingCalendarPolicySchemaVersion = "market-validation-calendar-policy/v1"
)

type TradingCalendarPolicy struct {
	SchemaVersion string                    `json:"schema_version"`
	PolicyID      string                    `json:"policy_id"`
	Exchange      string                    `json:"exchange"`
	Timezone      string                    `json:"timezone"`
	PublishedAt   time.Time                 `json:"published_at"`
	EffectiveFrom string                    `json:"effective_from"`
	EffectiveTo   string                    `json:"effective_to"`
	Holidays      []string                  `json:"holidays"`
	Sessions      []CalendarPolicySession   `json:"sessions"`
	Regimes       []CalendarPolicyRegime    `json:"regimes"`
	Sources       []CalendarSourceReference `json:"sources"`
}

type CalendarPolicySession struct {
	Open  string `json:"open"`
	Close string `json:"close"`
	Kind  string `json:"kind"`
	Note  string `json:"note,omitempty"`
}

type CalendarPolicyRegime struct {
	Open   string `json:"open"`
	Close  string `json:"close"`
	Regime string `json:"regime"`
}

type CalendarClassification string

const (
	CalendarTradingDay    CalendarClassification = "TRADING_DAY"
	CalendarNonTradingDay CalendarClassification = "NON_TRADING_DAY"
)

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

// GenerateTradingCalendar emits exact one-day calendar and source artifacts
// from a checksum-verified policy. It never infers absent policy coverage.
func GenerateTradingCalendar(policyPath, calendarOutputPath, targetDate string) ([]byte, CalendarSourceSet, CalendarClassification, error) {
	raw, err := os.ReadFile(policyPath)
	if err != nil {
		return nil, CalendarSourceSet{}, "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var policy TradingCalendarPolicy
	if decoder.Decode(&policy) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, CalendarSourceSet{}, "", ErrInvalidRecord
	}
	date, err := time.Parse("2006-01-02", targetDate)
	if err != nil || validateCalendarPolicy(policy, date) != nil {
		return nil, CalendarSourceSet{}, "", ErrInvalidRecord
	}
	policyBase := filepath.Dir(policyPath)
	calendarBase := filepath.Dir(calendarOutputPath)
	sources := CalendarSourceSet{SchemaVersion: CalendarSourcesSchemaVersion, Sources: append([]CalendarSourceReference(nil), policy.Sources...)}
	for index := range sources.Sources {
		source := &sources.Sources[index]
		resolved := source.Path
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(policyBase, filepath.Clean(resolved))
		}
		sourceRaw, readErr := os.ReadFile(resolved)
		if readErr != nil {
			return nil, CalendarSourceSet{}, "", readErr
		}
		sum := sha256.Sum256(sourceRaw)
		if hex.EncodeToString(sum[:]) != strings.ToLower(source.SHA256) {
			return nil, CalendarSourceSet{}, "", ErrInvalidRecord
		}
		relative, relErr := filepath.Rel(calendarBase, resolved)
		if relErr != nil {
			return nil, CalendarSourceSet{}, "", relErr
		}
		source.Path = filepath.ToSlash(relative)
	}
	sourcesRaw, err := json.Marshal(sources)
	if err != nil {
		return nil, CalendarSourceSet{}, "", err
	}
	sources, err = DecodeCalendarSources(sourcesRaw)
	if err != nil {
		return nil, CalendarSourceSet{}, "", err
	}
	holiday := date.Weekday() == time.Saturday || date.Weekday() == time.Sunday
	for _, item := range policy.Holidays {
		holiday = holiday || item == targetDate
	}
	classification := CalendarTradingDay
	day := map[string]any{"exchange": policy.Exchange, "date": targetDate, "status": "TRADING", "sessions": policy.Sessions, "regimes": policy.Regimes}
	if holiday {
		classification = CalendarNonTradingDay
		day = map[string]any{"exchange": policy.Exchange, "date": targetDate, "status": "HOLIDAY"}
	}
	encoded := map[string]any{
		"schema_version": 2, "source": policy.PolicyID, "published_at": policy.PublishedAt.UTC(), "timezone": policy.Timezone,
		"effective_from": targetDate, "effective_to": targetDate, "days": []any{day},
	}
	calendarRaw, err := json.MarshalIndent(encoded, "", "  ")
	if err != nil {
		return nil, CalendarSourceSet{}, "", err
	}
	calendarRaw = append(calendarRaw, '\n')
	if _, err = calendarfile.Decode(calendarRaw); err != nil {
		return nil, CalendarSourceSet{}, "", err
	}
	return calendarRaw, sources, classification, nil
}

func validateCalendarPolicy(policy TradingCalendarPolicy, target time.Time) error {
	if policy.SchemaVersion != TradingCalendarPolicySchemaVersion || strings.TrimSpace(policy.PolicyID) == "" || policy.Exchange != "NSE" || policy.Timezone != "Asia/Kolkata" || policy.PublishedAt.IsZero() || len(policy.Sources) == 0 || len(policy.Sessions) == 0 || len(policy.Regimes) != 3 {
		return ErrInvalidRecord
	}
	from, fromErr := time.Parse("2006-01-02", policy.EffectiveFrom)
	to, toErr := time.Parse("2006-01-02", policy.EffectiveTo)
	if fromErr != nil || toErr != nil || target.Before(from) || target.After(to) {
		return ErrInvalidRecord
	}
	seen := map[string]bool{}
	for _, holiday := range policy.Holidays {
		parsed, err := time.Parse("2006-01-02", holiday)
		if err != nil || parsed.Before(from) || parsed.After(to) || seen[holiday] {
			return ErrInvalidRecord
		}
		seen[holiday] = true
	}
	if policy.Regimes[0].Regime != "PRE_CAS" || policy.Regimes[1].Regime != "CAS_ACTIVE" || policy.Regimes[2].Regime != "POST_CAS" {
		return ErrInvalidRecord
	}
	return nil
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
