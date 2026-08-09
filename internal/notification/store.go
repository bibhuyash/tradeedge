package notification

import (
	"sync"
	"time"
)

type Store struct {
	mu                              sync.RWMutex
	eventCapacity, deliveryCapacity int
	events                          []Event
	deliveries                      []Delivery
}

func NewStore(eventCapacity, deliveryCapacity int) (*Store, error) {
	if eventCapacity <= 0 || deliveryCapacity <= 0 || eventCapacity > 10000 || deliveryCapacity > 10000 {
		return nil, ErrInvalid
	}
	return &Store{eventCapacity: eventCapacity, deliveryCapacity: deliveryCapacity}, nil
}

func (s *Store) RecordEvent(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = appendBounded(s.events, event, s.eventCapacity)
}
func (s *Store) RecordDelivery(value Delivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value.UpdatedAt = value.UpdatedAt.UTC()
	s.deliveries = appendBounded(s.deliveries, value, s.deliveryCapacity)
}
func (s *Store) RecentEvents(limit int) []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return newest(s.events, boundedLimit(limit))
}
func (s *Store) RecentDeliveries(limit int, failedOnly bool) []Delivery {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]Delivery, 0, boundedLimit(limit))
	for i := len(s.deliveries) - 1; i >= 0 && len(values) < boundedLimit(limit); i-- {
		if !failedOnly || s.deliveries[i].State == DeliveryFailed || s.deliveries[i].State == DeliveryDropped {
			values = append(values, s.deliveries[i])
		}
	}
	return values
}
func appendBounded[T any](values []T, value T, capacity int) []T {
	if len(values) == capacity {
		copy(values, values[1:])
		values = values[:capacity-1]
	}
	return append(values, value)
}
func newest[T any](values []T, limit int) []T {
	if limit > len(values) {
		limit = len(values)
	}
	out := make([]T, limit)
	for i := 0; i < limit; i++ {
		out[i] = values[len(values)-1-i]
	}
	return out
}
func boundedLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

type Health struct {
	State         string    `json:"state"`
	Accepting     bool      `json:"accepting"`
	QueueDepth    int       `json:"queue_depth"`
	QueueCapacity int       `json:"queue_capacity"`
	InFlight      int       `json:"in_flight"`
	FailureCount  uint64    `json:"failure_count"`
	DroppedCount  uint64    `json:"dropped_count"`
	UpdatedAt     time.Time `json:"updated_at"`
}
