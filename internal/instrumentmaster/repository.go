package instrumentmaster

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

var ErrMasterNotFound = errors.New("instrument master not found")

type Repository interface {
	Put(ctx context.Context, master Master) error
	Current(ctx context.Context) (Master, error)
	Get(ctx context.Context, version Version) (Master, error)
}

type MemoryRepository struct {
	mu      sync.RWMutex
	current Version
	masters map[Version]Master
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{masters: make(map[Version]Master)}
}

func (r *MemoryRepository) Put(ctx context.Context, master Master) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if master.Version() == "" {
		return ErrInvalidMaster
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.masters[master.Version()] = master
	r.current = master.Version()
	return nil
}

func (r *MemoryRepository) Current(ctx context.Context) (Master, error) {
	if err := ctx.Err(); err != nil {
		return Master{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	master, found := r.masters[r.current]
	if !found {
		return Master{}, ErrMasterNotFound
	}
	return master, nil
}

func (r *MemoryRepository) Get(ctx context.Context, version Version) (Master, error) {
	if err := ctx.Err(); err != nil {
		return Master{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	master, found := r.masters[version]
	if !found {
		return Master{}, ErrMasterNotFound
	}
	return master, nil
}

type Resolver struct {
	Repository Repository
}

func (r Resolver) ResolveProviderRef(
	ctx context.Context,
	provider domain.Provider,
	token string,
	exchangeTime time.Time,
) (domain.Instrument, string, error) {
	master, err := r.Repository.Current(ctx)
	if err != nil {
		return domain.Instrument{}, "", err
	}
	id, err := master.Resolve(provider, token, exchangeTime)
	if err != nil {
		return domain.Instrument{}, string(master.Version()), err
	}
	instrument, found := master.Instrument(id)
	if !found {
		return domain.Instrument{}, string(master.Version()), ErrInstrumentNotFound
	}
	return instrument, string(master.Version()), nil
}
