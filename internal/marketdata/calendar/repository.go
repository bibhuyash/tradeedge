package calendar

import (
	"context"
	"errors"
	"sync"
)

var ErrCalendarNotFound = errors.New("market calendar not found")

type Repository interface {
	Put(ctx context.Context, schedule *Schedule) error
	Current(ctx context.Context) (Calendar, error)
	Get(ctx context.Context, version Version) (Calendar, error)
}

type MemoryRepository struct {
	mu       sync.RWMutex
	current  Version
	versions map[Version]*Schedule
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{versions: make(map[Version]*Schedule)}
}

func (r *MemoryRepository) Put(ctx context.Context, schedule *Schedule) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if schedule == nil || schedule.Version() == "" {
		return ErrInvalidCalendar
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.versions[schedule.Version()] = schedule
	r.current = schedule.Version()
	return nil
}

func (r *MemoryRepository) Current(ctx context.Context) (Calendar, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	schedule, found := r.versions[r.current]
	if !found {
		return nil, ErrCalendarNotFound
	}
	return schedule, nil
}

func (r *MemoryRepository) Get(ctx context.Context, version Version) (Calendar, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	schedule, found := r.versions[version]
	if !found {
		return nil, ErrCalendarNotFound
	}
	return schedule, nil
}
