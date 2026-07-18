package quality

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/calendar"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

var ErrInvalidGapDetector = errors.New("invalid market-data gap detector")

type GapRequirement struct {
	Provider     domain.Provider      `json:"provider"`
	InstrumentID domain.InstrumentID  `json:"-"`
	Exchange     domain.Exchange      `json:"exchange"`
	Interval     model.CandleInterval `json:"interval"`
}

type MissingInterval struct {
	GapRequirement
	Open  time.Time
	Close time.Time
}

type MissingRange struct {
	GapRequirement
	Instrument string    `json:"instrument_id"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Count      int       `json:"count"`
}

type GapDetector struct {
	mu           sync.Mutex
	calendar     calendar.Calendar
	start        time.Time
	end          time.Time
	requirements []GapRequirement
	seen         map[string]struct{}
}

func NewGapDetector(
	schedule calendar.Calendar,
	start time.Time,
	end time.Time,
	requirements []GapRequirement,
) (*GapDetector, error) {
	if schedule == nil || start.IsZero() || !start.Before(end) || len(requirements) == 0 {
		return nil, ErrInvalidGapDetector
	}
	for _, requirement := range requirements {
		if requirement.Provider == "" || requirement.InstrumentID.IsZero() || requirement.Exchange == "" {
			return nil, ErrInvalidGapDetector
		}
		if _, valid := requirement.Interval.Duration(); !valid {
			return nil, ErrInvalidGapDetector
		}
	}
	return &GapDetector{
		calendar: schedule, start: start.UTC(), end: end.UTC(),
		requirements: append([]GapRequirement(nil), requirements...),
		seen:         make(map[string]struct{}),
	}, nil
}

func (d *GapDetector) Observe(event model.Event) {
	candle, ok := event.(model.CompletedCandleEvent)
	if !ok {
		return
	}
	key := gapKey(event.InstrumentID(), candle.Interval(), candle.OpenTime(), candle.CloseTime())
	d.mu.Lock()
	d.seen[key] = struct{}{}
	d.mu.Unlock()
}

func (d *GapDetector) Missing(ctx context.Context) ([]model.QualityRecord, error) {
	intervals, err := d.MissingIntervals(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]model.QualityRecord, 0, len(intervals))
	for _, interval := range intervals {
		records = append(records, model.QualityRecord{
			Code: model.QualityMissing, Disposition: model.DispositionQuarantined,
			Provider: interval.Provider, InstrumentID: interval.InstrumentID,
			ExchangeTime: interval.Close, ObservedAt: d.end,
			Reason: fmt.Sprintf("missing completed %s candle [%s,%s)",
				interval.Interval, interval.Open.UTC().Format(time.RFC3339Nano),
				interval.Close.UTC().Format(time.RFC3339Nano)),
		})
	}
	return records, nil
}

func (d *GapDetector) MissingIntervals(ctx context.Context) ([]MissingInterval, error) {
	location, err := time.LoadLocation(d.calendar.Timezone())
	if err != nil {
		return nil, err
	}
	startLocal, endLocal := d.start.In(location), d.end.In(location)
	date, _ := domain.NewCivilDate(startLocal.Year(), startLocal.Month(), startLocal.Day())
	lastDate, _ := domain.NewCivilDate(endLocal.Year(), endLocal.Month(), endLocal.Day())
	d.mu.Lock()
	seen := make(map[string]struct{}, len(d.seen))
	for key := range d.seen {
		seen[key] = struct{}{}
	}
	d.mu.Unlock()
	var missing []MissingInterval
	for ; compareCivilDate(date, lastDate) <= 0; date = nextCivilDate(date) {
		for _, requirement := range d.requirements {
			windows, err := d.calendar.ExpectedWindows(ctx, requirement.Exchange, date, requirement.Interval)
			if errors.Is(err, calendar.ErrCalendarOutOfRange) {
				return nil, err
			}
			if err != nil {
				return nil, fmt.Errorf("expected candle windows: %w", err)
			}
			for _, window := range windows {
				if window.Open.Before(d.start) || !window.Close.Before(d.end) {
					continue
				}
				if _, found := seen[gapKey(requirement.InstrumentID, requirement.Interval, window.Open, window.Close)]; !found {
					missing = append(missing, MissingInterval{
						GapRequirement: requirement, Open: window.Open.UTC(), Close: window.Close.UTC(),
					})
				}
			}
		}
	}
	sort.Slice(missing, func(i, j int) bool {
		left, right := missing[i], missing[j]
		if left.InstrumentID != right.InstrumentID {
			return left.InstrumentID.String() < right.InstrumentID.String()
		}
		if left.Interval != right.Interval {
			return left.Interval < right.Interval
		}
		return left.Open.Before(right.Open)
	})
	return missing, nil
}

func CoalesceMissing(intervals []MissingInterval) []MissingRange {
	if len(intervals) == 0 {
		return nil
	}
	result := make([]MissingRange, 0, len(intervals))
	for _, interval := range intervals {
		if len(result) > 0 {
			last := &result[len(result)-1]
			duration, _ := interval.Interval.Duration()
			if last.InstrumentID == interval.InstrumentID &&
				last.Provider == interval.Provider &&
				last.Interval == interval.Interval &&
				last.End.Equal(interval.Open) &&
				(duration == 0 || interval.Close.Sub(interval.Open) == duration) {
				last.End = interval.Close
				last.Count++
				continue
			}
		}
		result = append(result, MissingRange{
			GapRequirement: interval.GapRequirement,
			Instrument:     interval.InstrumentID.String(),
			Start:          interval.Open, End: interval.Close, Count: 1,
		})
	}
	return result
}

func gapKey(
	instrumentID domain.InstrumentID,
	interval model.CandleInterval,
	open time.Time,
	closeTime time.Time,
) string {
	return fmt.Sprintf("%s|%s|%d|%d", instrumentID, interval, open.UnixNano(), closeTime.UnixNano())
}

func compareCivilDate(left, right domain.CivilDate) int {
	switch {
	case left.String() < right.String():
		return -1
	case left.String() > right.String():
		return 1
	default:
		return 0
	}
}

func nextCivilDate(date domain.CivilDate) domain.CivilDate {
	next := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	result, _ := domain.NewCivilDate(next.Year(), next.Month(), next.Day())
	return result
}
