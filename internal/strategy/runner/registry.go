package runner

import (
	"errors"
	"reflect"
	"sync"

	"github.com/bibhuyash/tradeedge/internal/strategy"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

var ErrDefinitionNotRegistered = errors.New("strategy definition implementation not registered")

type Registry struct {
	mu      sync.RWMutex
	entries map[strategymodel.VersionID]strategy.Definition
}

func NewRegistry() *Registry {
	return &Registry{entries: make(map[strategymodel.VersionID]strategy.Definition)}
}

func (registry *Registry) Register(definition strategy.Definition) error {
	if definition == nil {
		return ErrDefinitionNotRegistered
	}
	descriptor := definition.Descriptor()
	if descriptor.Validate() != nil {
		return ErrDefinitionNotRegistered
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if existing, found := registry.entries[descriptor.VersionID]; found {
		left, right := reflect.ValueOf(existing), reflect.ValueOf(definition)
		if left.IsValid() && right.IsValid() && left.Type() == right.Type() &&
			left.Comparable() && right.Comparable() && left.Interface() == right.Interface() {
			return nil
		}
		return ErrDefinitionNotRegistered
	}
	registry.entries[descriptor.VersionID] = definition
	return nil
}

func (registry *Registry) Resolve(id strategymodel.VersionID) (strategy.Definition, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	value, found := registry.entries[id]
	if !found {
		return nil, ErrDefinitionNotRegistered
	}
	return value, nil
}
