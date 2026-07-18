package ingest

import (
	"container/heap"
	"errors"
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
	buffer          eventHeap
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
	var ready []model.Event
	if o.maxTime.IsZero() || event.ExchangeTime().After(o.maxTime) {
		o.maxTime = event.ExchangeTime()
		nextWatermark := o.maxTime.Add(-o.allowedLateness)
		ready = o.flushThrough(nextWatermark)
		if nextWatermark.After(o.watermark) {
			o.watermark = nextWatermark
		}
	}
	if len(o.buffer) >= o.capacity {
		return nil, "", ErrReorderCapacity
	}
	heap.Push(&o.buffer, event)
	o.seen[event.ID()] = struct{}{}
	nextWatermark := o.maxTime.Add(-o.allowedLateness)
	ready = append(ready, o.flushThrough(nextWatermark)...)
	if nextWatermark.After(o.watermark) {
		o.watermark = nextWatermark
	}
	return ready, PushAccepted, nil
}

func (o *Orderer) Flush() []model.Event {
	return o.flushThrough(time.Unix(1<<62, 0))
}

func (o *Orderer) Depth() int { return len(o.buffer) }

func (o *Orderer) flushThrough(watermark time.Time) []model.Event {
	ready := make([]model.Event, 0)
	for len(o.buffer) > 0 && !o.buffer[0].ExchangeTime().After(watermark) {
		event := heap.Pop(&o.buffer).(model.Event)
		ready = append(ready, event)
		delete(o.seen, event.ID())
	}
	return ready
}

type eventHeap []model.Event

func (h eventHeap) Len() int           { return len(h) }
func (h eventHeap) Less(i, j int) bool { return model.EventLess(h[i], h[j]) }
func (h eventHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(value any)    { *h = append(*h, value.(model.Event)) }
func (h *eventHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	return last
}
