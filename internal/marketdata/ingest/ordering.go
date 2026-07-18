package ingest

import (
	"errors"
	"sort"
	"time"

	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

var ErrReorderCapacity = errors.New("market-data reorder capacity exceeded")

type PushDisposition string

const (
	PushAccepted  PushDisposition = "ACCEPTED"
	PushDuplicate PushDisposition = "DUPLICATE"
	PushLate      PushDisposition = "LATE"
)

type Orderer struct {
	allowedLateness time.Duration
	capacity        int
	maxTime         time.Time
	watermark       time.Time
	buffer          []model.Event
	seen            map[model.EventID]struct{}
}

func NewOrderer(allowedLateness time.Duration, capacity int) (*Orderer, error) {
	if allowedLateness < 0 || capacity <= 0 {
		return nil, ErrReorderCapacity
	}
	return &Orderer{
		allowedLateness: allowedLateness,
		capacity:        capacity,
		seen:            make(map[model.EventID]struct{}),
	}, nil
}

func (o *Orderer) Push(event model.Event) ([]model.Event, PushDisposition, error) {
	if _, duplicate := o.seen[event.ID()]; duplicate {
		return nil, PushDuplicate, nil
	}
	if !o.watermark.IsZero() && !event.ExchangeTime().After(o.watermark) {
		return nil, PushLate, nil
	}
	if len(o.buffer) >= o.capacity {
		return nil, "", ErrReorderCapacity
	}
	if o.maxTime.IsZero() || event.ExchangeTime().After(o.maxTime) {
		o.maxTime = event.ExchangeTime()
	}
	o.buffer = append(o.buffer, event)
	o.seen[event.ID()] = struct{}{}
	nextWatermark := o.maxTime.Add(-o.allowedLateness)
	ready := o.flushThrough(nextWatermark)
	if nextWatermark.After(o.watermark) {
		o.watermark = nextWatermark
	}
	return ready, PushAccepted, nil
}

func (o *Orderer) Flush() []model.Event {
	return o.flushThrough(time.Unix(1<<62, 0))
}

func (o *Orderer) flushThrough(watermark time.Time) []model.Event {
	sort.SliceStable(o.buffer, func(i, j int) bool {
		return model.EventLess(o.buffer[i], o.buffer[j])
	})
	index := 0
	for index < len(o.buffer) && !o.buffer[index].ExchangeTime().After(watermark) {
		index++
	}
	ready := append([]model.Event(nil), o.buffer[:index]...)
	for _, event := range ready {
		delete(o.seen, event.ID())
	}
	o.buffer = append([]model.Event(nil), o.buffer[index:]...)
	return ready
}
