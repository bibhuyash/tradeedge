package ingest

import (
	"sync"

	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

// Deduplicator is useful for sources that need an exact bounded recent-event
// window before ordering. The Orderer also suppresses duplicates in its buffer.
type Deduplicator struct {
	mu       sync.Mutex
	capacity int
	order    []model.EventID
	seen     map[model.EventID]struct{}
}

func NewDeduplicator(capacity int) *Deduplicator {
	return &Deduplicator{capacity: capacity, seen: make(map[model.EventID]struct{})}
}

func (d *Deduplicator) First(id model.EventID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, found := d.seen[id]; found {
		return false
	}
	d.seen[id] = struct{}{}
	d.order = append(d.order, id)
	if len(d.order) > d.capacity {
		delete(d.seen, d.order[0])
		d.order = d.order[1:]
	}
	return true
}
