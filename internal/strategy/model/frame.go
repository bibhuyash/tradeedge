package model

import (
	"errors"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
)

const MaximumFrameBytes = 4 << 20

var ErrInvalidCandleFrame = errors.New("invalid strategy candle frame")

type ReadinessEvidence struct {
	State           readiness.State
	Reasons         []readiness.ReasonCode
	PolicyVersion   string
	CalendarVersion string
	EvaluatedAt     time.Time
}

func (evidence ReadinessEvidence) Ready() bool {
	if evidence.State != readiness.StateReady {
		return false
	}
	if strings.TrimSpace(evidence.PolicyVersion) == "" ||
		strings.TrimSpace(evidence.CalendarVersion) == "" ||
		evidence.EvaluatedAt.IsZero() {
		return false
	}
	for _, reason := range evidence.Reasons {
		if reason != readiness.ReasonNone {
			return false
		}
	}
	return true
}

type CandleSeries struct {
	Role         string
	InstrumentID domain.InstrumentID
	Interval     marketmodel.CandleInterval
	Candles      []marketmodel.CompletedCandleEvent
}

func NewCandleSeries(
	subscription InputSubscription,
	candles []marketmodel.CompletedCandleEvent,
) (CandleSeries, error) {
	if err := subscription.Validate(); err != nil || len(candles) > subscription.Lookback {
		return CandleSeries{}, ErrInvalidCandleFrame
	}
	if subscription.Required && len(candles) == 0 {
		return CandleSeries{}, ErrInvalidCandleFrame
	}
	copied := append([]marketmodel.CompletedCandleEvent(nil), candles...)
	for index, candle := range copied {
		if candle.InstrumentID() != subscription.InstrumentID ||
			candle.Interval() != subscription.Interval {
			return CandleSeries{}, ErrInvalidCandleFrame
		}
		if index > 0 && !copied[index-1].OpenTime().Before(candle.OpenTime()) {
			return CandleSeries{}, ErrInvalidCandleFrame
		}
	}
	return CandleSeries{
		Role: subscription.Role, InstrumentID: subscription.InstrumentID,
		Interval: subscription.Interval, Candles: copied,
	}, nil
}

type CandleFrame struct {
	id                FrameID
	triggerID         TriggerID
	logicalTime       time.Time
	subscription      SubscriptionSpec
	series            []CandleSeries
	masterVersion     string
	calendarVersion   string
	datasetRevision   string
	sourceEventIDs    []marketmodel.EventID
	estimatedByteSize int
}

type CandleFrameSpec struct {
	TriggerID       TriggerID
	LogicalTime     time.Time
	Subscription    SubscriptionSpec
	Series          []CandleSeries
	MasterVersion   string
	CalendarVersion string
	DatasetRevision string
}

func NewCandleFrame(spec CandleFrameSpec) (CandleFrame, error) {
	if spec.TriggerID.IsZero() || spec.LogicalTime.IsZero() || spec.Subscription.IsZero() ||
		len(spec.Series) != len(spec.Subscription.subscriptions) ||
		strings.TrimSpace(spec.MasterVersion) == "" ||
		strings.TrimSpace(spec.CalendarVersion) == "" {
		return CandleFrame{}, ErrInvalidCandleFrame
	}
	byRole := make(map[string]CandleSeries, len(spec.Series))
	for _, series := range spec.Series {
		if _, exists := byRole[series.Role]; exists {
			return CandleFrame{}, ErrInvalidCandleFrame
		}
		byRole[series.Role] = series
	}
	ordered := make([]CandleSeries, 0, len(spec.Series))
	var sourceIDs []marketmodel.EventID
	latestCloses := make([]time.Time, 0, len(spec.Series))
	estimated := 0
	for _, subscription := range spec.Subscription.subscriptions {
		series, found := byRole[subscription.Role]
		if !found || series.InstrumentID != subscription.InstrumentID ||
			series.Interval != subscription.Interval ||
			(subscription.Required && len(series.Candles) == 0) {
			return CandleFrame{}, ErrInvalidCandleFrame
		}
		validatedSeries, err := NewCandleSeries(subscription, series.Candles)
		if err != nil {
			return CandleFrame{}, ErrInvalidCandleFrame
		}
		series = validatedSeries
		if len(series.Candles) > 0 {
			latest := series.Candles[len(series.Candles)-1]
			if latest.CloseTime().After(spec.LogicalTime) {
				return CandleFrame{}, ErrInvalidCandleFrame
			}
			if subscription.MaximumAge > 0 &&
				spec.LogicalTime.Sub(latest.CloseTime()) > subscription.MaximumAge {
				return CandleFrame{}, ErrInvalidCandleFrame
			}
			latestCloses = append(latestCloses, latest.CloseTime())
		}
		for _, candle := range series.Candles {
			sourceIDs = append(sourceIDs, candle.ID())
			estimated += 256
		}
		series.Candles = append([]marketmodel.CompletedCandleEvent(nil), series.Candles...)
		ordered = append(ordered, series)
	}
	if spec.Subscription.mode == SubscriptionExactCloseFrame {
		for _, closeTime := range latestCloses {
			if !closeTime.Equal(spec.LogicalTime) {
				return CandleFrame{}, ErrInvalidCandleFrame
			}
		}
	}
	if estimated > MaximumFrameBytes {
		return CandleFrame{}, ErrInvalidCandleFrame
	}
	parts := []string{
		spec.TriggerID.String(), spec.LogicalTime.UTC().Format(time.RFC3339Nano),
		spec.Subscription.version, spec.MasterVersion, spec.CalendarVersion,
		spec.DatasetRevision,
	}
	for _, eventID := range sourceIDs {
		parts = append(parts, eventID.String())
	}
	key := stableKey("strategy-candle-frame/v1", parts...)
	id, _ := NewFrameID(key)
	return CandleFrame{
		id: id, triggerID: spec.TriggerID, logicalTime: spec.LogicalTime.UTC(),
		subscription: spec.Subscription, series: ordered,
		masterVersion: spec.MasterVersion, calendarVersion: spec.CalendarVersion,
		datasetRevision: spec.DatasetRevision, sourceEventIDs: sourceIDs,
		estimatedByteSize: estimated,
	}, nil
}

func (frame CandleFrame) ID() FrameID            { return frame.id }
func (frame CandleFrame) TriggerID() TriggerID   { return frame.triggerID }
func (frame CandleFrame) LogicalTime() time.Time { return frame.logicalTime }
func (frame CandleFrame) Subscription() SubscriptionSpec {
	result := frame.subscription
	result.subscriptions = append([]InputSubscription(nil), result.subscriptions...)
	return result
}
func (frame CandleFrame) MasterVersion() string   { return frame.masterVersion }
func (frame CandleFrame) CalendarVersion() string { return frame.calendarVersion }
func (frame CandleFrame) DatasetRevision() string { return frame.datasetRevision }
func (frame CandleFrame) EstimatedByteSize() int  { return frame.estimatedByteSize }
func (frame CandleFrame) SourceEventIDs() []marketmodel.EventID {
	return append([]marketmodel.EventID(nil), frame.sourceEventIDs...)
}
func (frame CandleFrame) Series() []CandleSeries {
	result := make([]CandleSeries, len(frame.series))
	for index, series := range frame.series {
		result[index] = series
		result[index].Candles = append([]marketmodel.CompletedCandleEvent(nil), series.Candles...)
	}
	return result
}
func (frame CandleFrame) IsZero() bool { return frame.id.IsZero() }
