package rules

import (
	"errors"
	"reflect"
	"sync"

	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
)

var ErrRuleNotRegistered = errors.New("risk rule implementation not registered")

type Registry struct {
	mu      sync.RWMutex
	entries map[riskmodel.RiskRuleID]Rule
}

func NewRegistry() *Registry { return &Registry{entries: make(map[riskmodel.RiskRuleID]Rule)} }

func (value *Registry) Register(rule Rule) error {
	if rule == nil || rule.Descriptor().Validate() != nil {
		return ErrRuleNotRegistered
	}
	descriptor := rule.Descriptor()
	value.mu.Lock()
	defer value.mu.Unlock()
	if existing, found := value.entries[descriptor.ID]; found {
		left, right := reflect.ValueOf(existing), reflect.ValueOf(rule)
		if left.IsValid() && right.IsValid() && left.Type() == right.Type() &&
			left.Comparable() && right.Comparable() && left.Interface() == right.Interface() {
			return nil
		}
		return ErrRuleNotRegistered
	}
	value.entries[descriptor.ID] = rule
	return nil
}

func (value *Registry) Resolve(configuration riskmodel.RiskRuleConfiguration) (Rule, error) {
	value.mu.RLock()
	defer value.mu.RUnlock()
	rule, found := value.entries[configuration.Descriptor.ID]
	if !found || rule.Descriptor() != configuration.Descriptor {
		return nil, ErrRuleNotRegistered
	}
	return rule, nil
}
