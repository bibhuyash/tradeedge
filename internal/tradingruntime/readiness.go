package tradingruntime

import (
	"sort"
	"strings"
	"time"
)

type HealthState string

const (
	HealthReady    HealthState = "READY"
	HealthDegraded HealthState = "DEGRADED"
	HealthBlocked  HealthState = "BLOCKED"
	HealthDisabled HealthState = "DISABLED"
	HealthUnknown  HealthState = "UNKNOWN"
)

type Requirement string

const (
	Required Requirement = "REQUIRED"
	Optional Requirement = "OPTIONAL"
)

type Dependency struct {
	Name        string      `json:"name"`
	Requirement Requirement `json:"requirement"`
	State       HealthState `json:"state"`
	Reasons     []string    `json:"reasons"`
	Version     string      `json:"version,omitempty"`
	ObservedAt  time.Time   `json:"observed_at"`
}

type ReadinessSnapshot struct {
	Ready        bool         `json:"ready"`
	State        HealthState  `json:"state"`
	Reasons      []string     `json:"reasons"`
	Dependencies []Dependency `json:"dependencies"`
	EvaluatedAt  time.Time    `json:"evaluated_at"`
}

func AggregateReadiness(mode Mode, dependencies []Dependency, at time.Time) ReadinessSnapshot {
	result := ReadinessSnapshot{Ready: mode.permitsPipeline(), State: HealthReady, Dependencies: append([]Dependency(nil), dependencies...), EvaluatedAt: at.UTC()}
	if mode.Validate() != nil {
		result.Ready, result.State, result.Reasons = false, HealthBlocked, []string{"INVALID_MODE"}
	} else if !mode.permitsPipeline() {
		result.Ready, result.State, result.Reasons = false, HealthBlocked, []string{string(mode) + "_NOT_TRADING"}
	}
	seen := map[string]struct{}{}
	for index := range result.Dependencies {
		dep := &result.Dependencies[index]
		dep.Name = strings.TrimSpace(dep.Name)
		dep.Reasons = append([]string(nil), dep.Reasons...)
		if dep.Name == "" {
			result.Ready, result.State = false, HealthBlocked
			result.Reasons = append(result.Reasons, "INVALID_DEPENDENCY")
			continue
		}
		if _, duplicate := seen[dep.Name]; duplicate {
			result.Ready, result.State = false, HealthBlocked
			result.Reasons = append(result.Reasons, "DUPLICATE_DEPENDENCY")
		}
		seen[dep.Name] = struct{}{}
		if dep.Requirement == Required && dep.State != HealthReady {
			result.Ready, result.State = false, HealthBlocked
			result.Reasons = append(result.Reasons, dep.Name+"_"+string(dep.State))
		} else if dep.Requirement == Optional && dep.State != HealthReady && result.State == HealthReady {
			result.State = HealthDegraded
		}
	}
	for _, required := range requiredDependencies(mode) {
		if _, found := seen[required]; !found {
			result.Ready, result.State = false, HealthBlocked
			result.Reasons = append(result.Reasons, required+"_MISSING")
		}
	}
	sort.Slice(result.Dependencies, func(i, j int) bool { return result.Dependencies[i].Name < result.Dependencies[j].Name })
	sort.Strings(result.Reasons)
	return result
}

func requiredDependencies(mode Mode) []string {
	common := []string{"accounting", "calendar", "configuration", "instrument_mappings", "market_data", "oms", "paper_broker", "reconciliation", "risk", "strategy", "valuation"}
	if mode == ModeShadow {
		return append(common, "broker_session", "broker_stream", "shadow_translation")
	}
	if mode == ModePaper {
		return common
	}
	return nil
}
