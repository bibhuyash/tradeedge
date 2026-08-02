package model

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

var (
	ErrInvalidOrderPlan  = errors.New("invalid order plan")
	ErrDependencyCycle   = errors.New("order leg dependency cycle")
	ErrUnsafeLegSequence = errors.New("sell leg lacks required protective buy dependency")
)

type OrderLegDraft struct {
	InstrumentID domain.InstrumentID
	Side         domain.Side
	Quantity     domain.Quantity
	LimitPrice   domain.Price
	Protective   bool
	DependsOn    []domain.InstrumentID
}

type OrderLeg struct {
	ID           OrderLegID
	InstrumentID domain.InstrumentID
	Side         domain.Side
	Quantity     domain.Quantity
	LimitPrice   domain.Price
	Protective   bool
	DependsOn    []OrderLegID
}

type OrderPlanSpec struct {
	SchemaVersion string
	Intent        ExecutionIntent
	Legs          []OrderLegDraft
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

type OrderPlan struct {
	id   OrderPlanID
	spec OrderPlanSpec
	legs []OrderLeg
	raw  []byte
}

func NewOrderPlan(spec OrderPlanSpec) (OrderPlan, error) {
	spec.SchemaVersion = strings.TrimSpace(spec.SchemaVersion)
	if spec.SchemaVersion == "" || spec.Intent.IsZero() || len(spec.Legs) == 0 ||
		len(spec.Legs) > len(spec.Intent.Spec().Legs) || spec.CreatedAt.IsZero() ||
		!spec.ExpiresAt.After(spec.CreatedAt) || spec.CreatedAt.Before(spec.Intent.Spec().CreatedAt) ||
		spec.ExpiresAt.After(spec.Intent.Spec().ExpiresAt) {
		return OrderPlan{}, ErrInvalidOrderPlan
	}
	authority := make(map[domain.InstrumentID]IntentLeg)
	for _, leg := range spec.Intent.Spec().Legs {
		authority[leg.InstrumentID] = leg
	}
	drafts := append([]OrderLegDraft(nil), spec.Legs...)
	sort.Slice(drafts, func(i, j int) bool { return drafts[i].InstrumentID.String() < drafts[j].InstrumentID.String() })
	legIDs := make(map[domain.InstrumentID]OrderLegID, len(drafts))
	protective := make([]OrderLegID, 0)
	for index := range drafts {
		draft := &drafts[index]
		approved, found := authority[draft.InstrumentID]
		if !found || draft.Side != approved.Side || draft.Quantity != approved.Quantity ||
			draft.LimitPrice.IsZeroValue() || draft.LimitPrice.Currency() != spec.Intent.Spec().MaximumCapital.Currency() ||
			(index > 0 && drafts[index-1].InstrumentID == draft.InstrumentID) ||
			(draft.Protective && draft.Side != domain.SideBuy) {
			return OrderPlan{}, ErrInvalidOrderPlan
		}
		legIDs[draft.InstrumentID] = OrderLegID(derive("order-leg-id/v1", spec.Intent.ID().String(), draft.InstrumentID.String(), string(draft.Side)))
		if draft.Protective {
			protective = append(protective, legIDs[draft.InstrumentID])
		}
	}
	legs := make([]OrderLeg, len(drafts))
	for index, draft := range drafts {
		dependencies := make([]OrderLegID, len(draft.DependsOn))
		seen := make(map[OrderLegID]struct{}, len(dependencies))
		for depIndex, instrument := range draft.DependsOn {
			id, found := legIDs[instrument]
			if !found || id == legIDs[draft.InstrumentID] {
				return OrderPlan{}, ErrInvalidOrderPlan
			}
			if _, duplicate := seen[id]; duplicate {
				return OrderPlan{}, ErrInvalidOrderPlan
			}
			seen[id] = struct{}{}
			dependencies[depIndex] = id
		}
		sort.Slice(dependencies, func(i, j int) bool { return dependencies[i].String() < dependencies[j].String() })
		if draft.Side == domain.SideSell {
			if len(protective) == 0 {
				return OrderPlan{}, ErrUnsafeLegSequence
			}
			for _, required := range protective {
				if _, found := seen[required]; !found {
					return OrderPlan{}, ErrUnsafeLegSequence
				}
			}
		}
		legs[index] = OrderLeg{legIDs[draft.InstrumentID], draft.InstrumentID, draft.Side,
			draft.Quantity, draft.LimitPrice, draft.Protective, dependencies}
		draft.DependsOn = append([]domain.InstrumentID(nil), draft.DependsOn...)
	}
	if hasCycle(legs) {
		return OrderPlan{}, ErrDependencyCycle
	}
	spec.Legs = drafts
	spec.CreatedAt, spec.ExpiresAt = spec.CreatedAt.UTC(), spec.ExpiresAt.UTC()
	raw, err := canonicalPlan(spec, legs)
	if err != nil {
		return OrderPlan{}, ErrInvalidOrderPlan
	}
	id := OrderPlanID(derive("order-plan-id/v1", spec.Intent.ID().String(), string(raw)))
	return OrderPlan{id: id, spec: spec, legs: legs, raw: raw}, nil
}

func hasCycle(legs []OrderLeg) bool {
	edges := make(map[OrderLegID][]OrderLegID, len(legs))
	for _, leg := range legs {
		edges[leg.ID] = leg.DependsOn
	}
	visiting, visited := map[OrderLegID]bool{}, map[OrderLegID]bool{}
	var visit func(OrderLegID) bool
	visit = func(id OrderLegID) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for _, dependency := range edges[id] {
			if visit(dependency) {
				return true
			}
		}
		visiting[id], visited[id] = false, true
		return false
	}
	for id := range edges {
		if visit(id) {
			return true
		}
	}
	return false
}

func canonicalPlan(spec OrderPlanSpec, legs []OrderLeg) ([]byte, error) {
	type legWire struct {
		ID, InstrumentID, Side string
		Quantity, PriceMinor   int64
		Currency               string
		Protective             bool
		DependsOn              []string
	}
	wire := make([]legWire, len(legs))
	for index, leg := range legs {
		dependencies := make([]string, len(leg.DependsOn))
		for depIndex, id := range leg.DependsOn {
			dependencies[depIndex] = id.String()
		}
		wire[index] = legWire{leg.ID.String(), leg.InstrumentID.String(), string(leg.Side), leg.Quantity.Int64(),
			leg.LimitPrice.MinorUnits(), leg.LimitPrice.Currency().String(), leg.Protective, dependencies}
	}
	return json.Marshal(struct {
		SchemaVersion, IntentID, CreatedAt, ExpiresAt string
		Legs                                          []legWire
	}{
		spec.SchemaVersion, spec.Intent.ID().String(), spec.CreatedAt.Format(time.RFC3339Nano),
		spec.ExpiresAt.Format(time.RFC3339Nano), wire})
}

func (value OrderPlan) ID() OrderPlanID             { return value.id }
func (value OrderPlan) IntentID() ExecutionIntentID { return value.spec.Intent.ID() }
func (value OrderPlan) Legs() []OrderLeg {
	result := append([]OrderLeg(nil), value.legs...)
	for index := range result {
		result[index].DependsOn = append([]OrderLegID(nil), result[index].DependsOn...)
	}
	return result
}
func (value OrderPlan) CanonicalJSON() []byte { return append([]byte(nil), value.raw...) }
func (value OrderPlan) IsZero() bool          { return value.id.IsZero() }

func (value OrderPlan) Spec() OrderPlanSpec {
	result := value.spec
	result.Legs = append([]OrderLegDraft(nil), result.Legs...)
	for index := range result.Legs {
		result.Legs[index].DependsOn = append([]domain.InstrumentID(nil), result.Legs[index].DependsOn...)
	}
	return result
}
