package memory

import (
	"bytes"
	"context"
	"sort"
	"sync"

	portfolioconfig "github.com/bibhuyash/tradeedge/internal/portfolio/config"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	portfoliostorage "github.com/bibhuyash/tradeedge/internal/portfolio/storage"
)

type Limits struct {
	Configurations int
	Snapshots      int
}

func DefaultLimits() Limits { return Limits{Configurations: 64, Snapshots: 10000} }

type storedConfiguration struct {
	value     portfolioconfig.PortfolioConfiguration
	canonical []byte
}
type storedSnapshot struct {
	value     portfoliomodel.PortfolioSnapshot
	canonical []byte
}

type Store struct {
	mu             sync.RWMutex
	limits         Limits
	configurations map[portfoliomodel.PortfolioConfigurationID]storedConfiguration
	policies       map[portfoliomodel.AllocationPolicyID]portfolioconfig.AllocationPolicy
	snapshots      map[portfoliomodel.PortfolioSnapshotID]storedSnapshot
}

func NewStore() *Store { return NewStoreWithLimits(DefaultLimits()) }

func NewStoreWithLimits(limits Limits) *Store {
	if limits.Configurations <= 0 || limits.Snapshots <= 0 {
		limits = DefaultLimits()
	}
	return &Store{
		limits:         limits,
		configurations: make(map[portfoliomodel.PortfolioConfigurationID]storedConfiguration),
		policies:       make(map[portfoliomodel.AllocationPolicyID]portfolioconfig.AllocationPolicy),
		snapshots:      make(map[portfoliomodel.PortfolioSnapshotID]storedSnapshot),
	}
}

func (store *Store) RegisterConfiguration(ctx context.Context,
	value portfolioconfig.PortfolioConfiguration) (portfoliostorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return portfoliostorage.RegistrationOutcome{}, err
	}
	canonical := value.CanonicalJSON()
	if value.ID().IsZero() || len(canonical) == 0 {
		return portfoliostorage.RegistrationOutcome{}, portfolioconfig.ErrInvalidConfiguration
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return portfoliostorage.RegistrationOutcome{}, err
	}
	if existing, found := store.configurations[value.ID()]; found {
		if bytes.Equal(existing.canonical, canonical) {
			return portfoliostorage.RegistrationOutcome{Status: portfoliostorage.RegistrationIdempotent}, nil
		}
		return portfoliostorage.RegistrationOutcome{}, &portfoliostorage.IdentityCollisionError{
			Kind: "configuration", Identity: value.ID().String(),
		}
	}
	if len(store.configurations) >= store.limits.Configurations {
		return portfoliostorage.RegistrationOutcome{}, portfoliostorage.ErrCapacityExhausted
	}
	store.configurations[value.ID()] = storedConfiguration{
		value: value, canonical: append([]byte(nil), canonical...),
	}
	policy := value.AllocationPolicy()
	store.policies[policy.ID] = policy
	return portfoliostorage.RegistrationOutcome{Status: portfoliostorage.RegistrationCommitted}, nil
}

func (store *Store) Configuration(ctx context.Context,
	id portfoliomodel.PortfolioConfigurationID) (portfolioconfig.PortfolioConfiguration, error) {
	if err := ctx.Err(); err != nil {
		return portfolioconfig.PortfolioConfiguration{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.configurations[id]
	if !found {
		return portfolioconfig.PortfolioConfiguration{}, portfoliostorage.ErrNotFound
	}
	return value.value, nil
}

func (store *Store) Configurations(ctx context.Context) ([]portfolioconfig.PortfolioConfiguration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]portfolioconfig.PortfolioConfiguration, 0, len(store.configurations))
	for _, value := range store.configurations {
		result = append(result, value.value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID().String() < result[j].ID().String() })
	return result, nil
}

func (store *Store) AllocationPolicy(ctx context.Context,
	id portfoliomodel.AllocationPolicyID) (portfolioconfig.AllocationPolicy, error) {
	if err := ctx.Err(); err != nil {
		return portfolioconfig.AllocationPolicy{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.policies[id]
	if !found {
		return portfolioconfig.AllocationPolicy{}, portfoliostorage.ErrNotFound
	}
	value.Limits.ExposureGroups = append([]string(nil), value.Limits.ExposureGroups...)
	return value, nil
}

func (store *Store) RegisterSnapshot(ctx context.Context,
	value portfoliomodel.PortfolioSnapshot) (portfoliostorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return portfoliostorage.RegistrationOutcome{}, err
	}
	canonical := value.CanonicalJSON()
	if value.ID().IsZero() || len(canonical) == 0 {
		return portfoliostorage.RegistrationOutcome{}, portfoliomodel.ErrInvalidPortfolioSnapshot
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return portfoliostorage.RegistrationOutcome{}, err
	}
	if existing, found := store.snapshots[value.ID()]; found {
		if bytes.Equal(existing.canonical, canonical) {
			return portfoliostorage.RegistrationOutcome{Status: portfoliostorage.RegistrationIdempotent}, nil
		}
		return portfoliostorage.RegistrationOutcome{}, &portfoliostorage.IdentityCollisionError{
			Kind: "snapshot", Identity: value.ID().String(),
		}
	}
	if len(store.snapshots) >= store.limits.Snapshots {
		return portfoliostorage.RegistrationOutcome{}, portfoliostorage.ErrCapacityExhausted
	}
	store.snapshots[value.ID()] = storedSnapshot{value: value, canonical: append([]byte(nil), canonical...)}
	return portfoliostorage.RegistrationOutcome{Status: portfoliostorage.RegistrationCommitted}, nil
}

func (store *Store) Snapshot(ctx context.Context,
	id portfoliomodel.PortfolioSnapshotID) (portfoliomodel.PortfolioSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return portfoliomodel.PortfolioSnapshot{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.snapshots[id]
	if !found {
		return portfoliomodel.PortfolioSnapshot{}, portfoliostorage.ErrNotFound
	}
	return value.value, nil
}

func (store *Store) Snapshots(ctx context.Context,
	id portfoliomodel.PortfolioID) ([]portfoliomodel.PortfolioSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	var result []portfoliomodel.PortfolioSnapshot
	for _, value := range store.snapshots {
		if value.value.PortfolioID() == id {
			result = append(result, value.value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Revision() == result[j].Revision() {
			return result[i].ID().String() < result[j].ID().String()
		}
		return result[i].Revision() < result[j].Revision()
	})
	return result, nil
}
