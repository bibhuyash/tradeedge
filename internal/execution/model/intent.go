package model

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
)

var (
	ErrInvalidExecutionIntent = errors.New("invalid execution intent")
	ErrDecisionNotExecutable  = errors.New("portfolio risk decision is not executable")
	ErrDecisionExpired        = errors.New("portfolio risk decision is expired or stale")
	ErrDecisionStale          = errors.New("portfolio risk decision does not match the authoritative portfolio revision")
	ErrAuthorityEscalation    = errors.New("requested execution authority exceeds approval")
)

type IntentLeg struct {
	InstrumentID     domain.InstrumentID
	Side             domain.Side
	Ratio            portfoliomodel.ContractRatio
	Quantity         domain.Quantity
	LotSize          domain.Quantity
	ReducingExposure bool
}

type ExecutionIntentSpec struct {
	SchemaVersion     string
	Decision          riskmodel.PortfolioRiskDecision
	MaximumCapital    domain.Money
	Legs              []IntentLeg
	PortfolioRevision portfoliomodel.PortfolioRevision
	CreatedAt         time.Time
	ExpiresAt         time.Time
}

type ExecutionIntent struct {
	id   ExecutionIntentID
	spec ExecutionIntentSpec
	raw  []byte
}

func NewExecutionIntent(spec ExecutionIntentSpec) (ExecutionIntent, error) {
	spec.SchemaVersion = strings.TrimSpace(spec.SchemaVersion)
	decisionSpec := spec.Decision.Spec()
	if spec.SchemaVersion == "" || spec.Decision.ID().IsZero() || spec.PortfolioRevision.Validate() != nil || spec.CreatedAt.IsZero() ||
		spec.ExpiresAt.IsZero() || !spec.ExpiresAt.After(spec.CreatedAt) {
		return ExecutionIntent{}, ErrInvalidExecutionIntent
	}
	if decisionSpec.Outcome != riskmodel.DecisionApproved && decisionSpec.Outcome != riskmodel.DecisionModified {
		return ExecutionIntent{}, ErrDecisionNotExecutable
	}
	if uint64(decisionSpec.ExpectedPortfolioRevision) == ^uint64(0) || spec.PortfolioRevision != decisionSpec.ExpectedPortfolioRevision+1 {
		return ExecutionIntent{}, ErrDecisionStale
	}
	approved, ok := spec.Decision.ApprovedAllocation()
	if !ok {
		return ExecutionIntent{}, ErrDecisionNotExecutable
	}
	created := spec.CreatedAt.UTC()
	expires := spec.ExpiresAt.UTC()
	if created.Before(decisionSpec.GeneratedAt) || !created.Before(decisionSpec.ExpiresAt) ||
		expires.After(decisionSpec.ExpiresAt) || expires.After(approved.ValidUntil) {
		return ExecutionIntent{}, ErrDecisionExpired
	}
	if spec.MaximumCapital.IsZeroValue() || spec.MaximumCapital.MinorUnits() < 0 ||
		spec.MaximumCapital.Currency() != approved.MaximumCapital.Currency() ||
		spec.MaximumCapital.MinorUnits() > approved.MaximumCapital.MinorUnits() ||
		len(spec.Legs) == 0 || len(spec.Legs) > len(approved.LegBounds) {
		return ExecutionIntent{}, ErrAuthorityEscalation
	}
	approvedByInstrument := make(map[domain.InstrumentID]portfoliomodel.AllocationLegBound, len(approved.LegBounds))
	for _, leg := range approved.LegBounds {
		approvedByInstrument[leg.InstrumentID] = leg
	}
	legs := append([]IntentLeg(nil), spec.Legs...)
	seen := make(map[domain.InstrumentID]struct{}, len(legs))
	for _, leg := range legs {
		bound, found := approvedByInstrument[leg.InstrumentID]
		if !found || leg.Side != bound.Side || leg.Ratio != bound.Ratio ||
			!leg.Quantity.IsValid() || leg.Quantity.Int64() > bound.MaximumUnits.Int64() ||
			leg.LotSize != bound.LotSize || leg.Quantity.Int64()%leg.LotSize.Int64() != 0 ||
			(leg.ReducingExposure && leg.Side != domain.SideSell) {
			return ExecutionIntent{}, ErrAuthorityEscalation
		}
		if _, duplicate := seen[leg.InstrumentID]; duplicate {
			return ExecutionIntent{}, ErrInvalidExecutionIntent
		}
		seen[leg.InstrumentID] = struct{}{}
	}
	sort.Slice(legs, func(i, j int) bool { return legs[i].InstrumentID.String() < legs[j].InstrumentID.String() })
	spec.Legs, spec.CreatedAt, spec.ExpiresAt = legs, created, expires
	raw, err := canonicalIntent(spec)
	if err != nil {
		return ExecutionIntent{}, ErrInvalidExecutionIntent
	}
	id := ExecutionIntentID(derive("execution-intent-id/v1", spec.Decision.ID().String(), string(raw)))
	return ExecutionIntent{id: id, spec: spec, raw: raw}, nil
}

func canonicalIntent(spec ExecutionIntentSpec) ([]byte, error) {
	type legWire struct {
		InstrumentID     string `json:"instrument_id"`
		Side             string `json:"side"`
		Ratio            uint32 `json:"ratio"`
		Quantity         int64  `json:"quantity"`
		LotSize          int64  `json:"lot_size"`
		ReducingExposure bool   `json:"reducing_exposure"`
	}
	legs := make([]legWire, len(spec.Legs))
	for index, leg := range spec.Legs {
		legs[index] = legWire{leg.InstrumentID.String(), string(leg.Side), uint32(leg.Ratio), leg.Quantity.Int64(), leg.LotSize.Int64(), leg.ReducingExposure}
	}
	return json.Marshal(struct {
		SchemaVersion, DecisionID, DecisionChecksum string
		CapitalMinor                                int64
		PortfolioRevision                           uint64
		Currency                                    string
		Legs                                        []legWire
		CreatedAt, ExpiresAt                        string
	}{spec.SchemaVersion, spec.Decision.ID().String(), spec.Decision.Checksum().String(),
		spec.MaximumCapital.MinorUnits(), uint64(spec.PortfolioRevision), spec.MaximumCapital.Currency().String(), legs,
		spec.CreatedAt.Format(time.RFC3339Nano), spec.ExpiresAt.Format(time.RFC3339Nano)})
}

func (value ExecutionIntent) ID() ExecutionIntentID { return value.id }
func (value ExecutionIntent) Spec() ExecutionIntentSpec {
	result := value.spec
	result.Legs = append([]IntentLeg(nil), result.Legs...)
	return result
}
func (value ExecutionIntent) CanonicalJSON() []byte { return append([]byte(nil), value.raw...) }
func (value ExecutionIntent) IsZero() bool          { return value.id.IsZero() }
