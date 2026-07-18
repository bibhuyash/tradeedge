package telemetry

import (
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

// Dimensions is deliberately bounded. IDs, tokens, paths, and free-text errors
// are not representable as metric labels.
type Dimensions struct {
	Provider domain.Provider
	Exchange domain.Exchange
	Segment  domain.Segment
	Kind     model.EventKind
	Interval model.CandleInterval
}

type Recorder interface {
	Observation(Dimensions, string)
	Quality(Dimensions, model.QualityCode, model.Disposition)
	Normalization(Dimensions, time.Duration)
	TransportLag(Dimensions, time.Duration)
	EventAge(Dimensions, time.Duration)
	ReorderDepth(Dimensions, int)
	Readiness(scopeType, provider, watchlist, state, reason string, ready bool, coverage float64)
	MissingIntervals(Dimensions, int)
	DatasetCommit(outcome string, duration time.Duration, bytes int64)
	ChecksumFailure()
	Replay(terminalState string, events uint64, duration, consumer, backpressure, pause time.Duration)
}

type NopRecorder struct{}

func (NopRecorder) Observation(Dimensions, string)                           {}
func (NopRecorder) Quality(Dimensions, model.QualityCode, model.Disposition) {}
func (NopRecorder) Normalization(Dimensions, time.Duration)                  {}
func (NopRecorder) TransportLag(Dimensions, time.Duration)                   {}
func (NopRecorder) EventAge(Dimensions, time.Duration)                       {}
func (NopRecorder) ReorderDepth(Dimensions, int)                             {}
func (NopRecorder) Readiness(string, string, string, string, string, bool, float64) {
}
func (NopRecorder) MissingIntervals(Dimensions, int)           {}
func (NopRecorder) DatasetCommit(string, time.Duration, int64) {}
func (NopRecorder) ChecksumFailure()                           {}
func (NopRecorder) Replay(string, uint64, time.Duration, time.Duration, time.Duration, time.Duration) {
}
