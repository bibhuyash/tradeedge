package allocation

import (
	"errors"
	"math"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	portfolioconfig "github.com/bibhuyash/tradeedge/internal/portfolio/config"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

var (
	ErrAllocationFailed = errors.New("portfolio allocation failed")
	ErrAllocationPanic  = errors.New("portfolio allocation panicked")
)

type Input struct {
	Proposal    strategymodel.TradeProposal
	Snapshot    portfoliomodel.PortfolioSnapshot
	Policy      portfolioconfig.AllocationPolicy
	Master      instrumentmaster.Master
	LogicalTime time.Time
}

type Engine struct{}

func (Engine) Evaluate(input Input) (portfoliomodel.AllocationCandidate, error) {
	if input.Proposal.IsZero() || input.Snapshot.ID().IsZero() || input.Master.Version() == "" ||
		input.LogicalTime.IsZero() || !input.Policy.Enabled ||
		input.LogicalTime.Before(input.Policy.EffectiveFrom) || !input.LogicalTime.Before(input.Policy.EffectiveUntil) ||
		input.Snapshot.State() != portfoliomodel.PortfolioEnabled {
		return portfoliomodel.AllocationCandidate{}, ErrAllocationFailed
	}
	var strategyAllocation portfoliomodel.StrategyAllocation
	metadata := input.Proposal.Metadata()
	for _, value := range input.Snapshot.StrategyAllocations() {
		spec := value.Spec()
		if spec.InstanceID == metadata.InstanceID && spec.InstanceRevisionID == metadata.InstanceRevisionID {
			strategyAllocation = value
			break
		}
	}
	if strategyAllocation.ID().IsZero() ||
		strategyAllocation.Spec().State != portfoliomodel.StrategyAllocationEnabled {
		return portfoliomodel.AllocationCandidate{}, ErrAllocationFailed
	}
	sizing := input.Proposal.Draft().Sizing
	allocationSpec := strategyAllocation.Spec()
	requestedMinor, ok := checkedMul(allocationSpec.Limit.MinorUnits(), int64(sizing.ValueBPS))
	if !ok {
		return portfoliomodel.AllocationCandidate{}, portfoliomodel.ErrArithmeticOverflow
	}
	requestedMinor /= 10000
	reserveBPS := int64(input.Policy.Limits.ReserveBPS) + int64(input.Policy.Limits.EmergencyReserveBPS)
	requiredReserve, ok := checkedMul(input.Policy.Limits.TotalCapital.MinorUnits(), reserveBPS)
	if !ok {
		return portfoliomodel.AllocationCandidate{}, portfoliomodel.ErrArithmeticOverflow
	}
	requiredReserve /= 10000
	availableAfterReserve := input.Snapshot.Capital().Available.MinorUnits() - requiredReserve
	if availableAfterReserve < 0 {
		availableAfterReserve = 0
	}
	bound := requestedMinor
	for _, value := range []int64{
		allocationSpec.Remaining.MinorUnits(), availableAfterReserve,
		input.Policy.Limits.MaximumStrategyCapital.MinorUnits(),
		input.Policy.Limits.MaximumInstrumentCapital.MinorUnits(),
		input.Policy.Limits.MaximumUnderlyingCapital.MinorUnits(),
		input.Policy.Limits.MaximumExposureGroupCapital.MinorUnits(),
	} {
		if value < bound {
			bound = value
		}
	}
	if bound <= 0 {
		return portfoliomodel.AllocationCandidate{}, ErrAllocationFailed
	}

	legs := input.Proposal.Draft().Legs
	legBounds := make([]portfoliomodel.AllocationLegBound, len(legs))
	packageCost := int64(0)
	for index, leg := range legs {
		instrument, found := input.Master.Instrument(leg.InstrumentID)
		if !found || instrument.Currency() != input.Snapshot.Spec().BaseCurrency {
			return portfoliomodel.AllocationCandidate{}, ErrAllocationFailed
		}
		units, ok := checkedMul(int64(leg.Ratio), instrument.LotSize().Int64())
		if !ok {
			return portfoliomodel.AllocationCandidate{}, portfoliomodel.ErrArithmeticOverflow
		}
		cost, ok := checkedMul(leg.ReferencePrice.MinorUnits(), units)
		if !ok || packageCost > math.MaxInt64-cost {
			return portfoliomodel.AllocationCandidate{}, portfoliomodel.ErrArithmeticOverflow
		}
		packageCost += cost
		legBounds[index] = portfoliomodel.AllocationLegBound{
			InstrumentID: leg.InstrumentID, Side: leg.Side,
			Ratio: portfoliomodel.ContractRatio(leg.Ratio), Resolution: portfoliomodel.QuantityResolved,
			LotSize: instrument.LotSize(),
		}
	}
	if packageCost <= 0 {
		return portfoliomodel.AllocationCandidate{}, ErrAllocationFailed
	}
	packages := bound / packageCost
	if packages <= 0 {
		return portfoliomodel.AllocationCandidate{}, ErrAllocationFailed
	}
	approvedMinor, ok := checkedMul(packageCost, packages)
	if !ok {
		return portfoliomodel.AllocationCandidate{}, portfoliomodel.ErrArithmeticOverflow
	}
	for index, leg := range legs {
		instrument, _ := input.Master.Instrument(leg.InstrumentID)
		units, ok := checkedMul(int64(leg.Ratio)*instrument.LotSize().Int64(), packages)
		if !ok {
			return portfoliomodel.AllocationCandidate{}, portfoliomodel.ErrArithmeticOverflow
		}
		quantity, err := domain.NewQuantity(units)
		if err != nil {
			return portfoliomodel.AllocationCandidate{}, ErrAllocationFailed
		}
		legBounds[index].MaximumUnits = quantity
	}
	capital, _ := domain.NewMoney(approvedMinor, input.Snapshot.Spec().BaseCurrency.String())
	knownCapital, _ := portfoliomodel.NewKnownMoney(capital)
	unknownRisk, _ := portfoliomodel.NewUnavailableMoney(portfoliomodel.AvailabilityUnknown)
	incremental, projected, err := exposure(input, legBounds)
	if err != nil {
		return portfoliomodel.AllocationCandidate{}, err
	}
	return portfoliomodel.NewAllocationCandidate(portfoliomodel.AllocationCandidateSpec{
		SchemaVersion: "allocation-candidate/v2", Proposal: input.Proposal,
		PortfolioID: input.Snapshot.PortfolioID(), PortfolioSnapshotID: input.Snapshot.ID(),
		PortfolioRevision: input.Snapshot.Revision(), StrategyAllocationID: strategyAllocation.ID(),
		PolicyID: input.Policy.ID, PolicyVersion: input.Policy.Version, RequestedSizing: sizing,
		CandidateCapital: capital, CandidatePremium: knownCapital, CandidateRiskBudget: unknownRisk,
		LegBounds: legBounds, IncrementalExposure: incremental, ProjectedExposure: projected,
		ReserveImpact: capital, Rounding: portfoliomodel.RoundingEvidence{
			RequestedMinor: requestedMinor, ApprovedMinor: approvedMinor,
			RemainderMinor: requestedMinor - approvedMinor, Method: "FLOOR_TO_BOUNDED_UNITS",
		}, ConfigurationHash: input.Policy.ConfigurationHash, GeneratedAt: input.LogicalTime,
		ValidFrom: input.Proposal.Draft().ValidFrom, ExpiresAt: input.Proposal.Draft().ExpiresAt,
	})
}

func exposure(input Input, bounds []portfoliomodel.AllocationLegBound) ([]portfoliomodel.ExposureRecord, []portfoliomodel.ExposureRecord, error) {
	currency := input.Snapshot.Spec().BaseCurrency.String()
	type totals struct {
		gross, net, paid, received int64
		hasShort                   bool
	}
	values := map[string]*totals{}
	add := func(dimension portfoliomodel.ExposureDimension, subject string, amount int64, side domain.Side) error {
		key := string(dimension) + "|" + subject
		value := values[key]
		if value == nil {
			value = &totals{}
			values[key] = value
		}
		if value.gross > math.MaxInt64-amount {
			return portfoliomodel.ErrArithmeticOverflow
		}
		value.gross += amount
		if side == domain.SideBuy {
			if value.net > math.MaxInt64-amount || value.paid > math.MaxInt64-amount {
				return portfoliomodel.ErrArithmeticOverflow
			}
			value.net += amount
			value.paid += amount
		} else {
			if value.net < math.MinInt64+amount || value.received > math.MaxInt64-amount {
				return portfoliomodel.ErrArithmeticOverflow
			}
			value.net -= amount
			value.received += amount
			value.hasShort = true
		}
		return nil
	}
	for index, leg := range input.Proposal.Draft().Legs {
		value, ok := checkedMul(leg.ReferencePrice.MinorUnits(), bounds[index].MaximumUnits.Int64())
		if !ok {
			return nil, nil, portfoliomodel.ErrArithmeticOverflow
		}
		instrument, found := input.Master.Instrument(leg.InstrumentID)
		if !found {
			return nil, nil, ErrAllocationFailed
		}
		for _, key := range []struct {
			dimension portfoliomodel.ExposureDimension
			subject   string
		}{
			{portfoliomodel.ExposurePortfolioWide, input.Snapshot.PortfolioID().String()},
			{portfoliomodel.ExposureInstrument, leg.InstrumentID.String()},
			{portfoliomodel.ExposureUnderlying, string(instrument.UnderlyingID())},
		} {
			if err := add(key.dimension, key.subject, value, leg.Side); err != nil {
				return nil, nil, err
			}
		}
	}
	money := func(value int64) domain.Money { result, _ := domain.NewMoney(value, currency); return result }
	known := func(value int64) portfoliomodel.MeasuredMoney {
		result, _ := portfoliomodel.NewKnownMoney(money(value))
		return result
	}
	zero := known(0)
	incrementalValues := make([]portfoliomodel.ExposureRecord, 0, len(values))
	projectedValues := make([]portfoliomodel.ExposureRecord, 0, len(values))
	for key, value := range values {
		parts := strings.SplitN(key, "|", 2)
		maximumLoss := known(value.gross)
		lossBound := portfoliomodel.LossBoundKnown
		if value.hasShort {
			maximumLoss, _ = portfoliomodel.NewUnavailableMoney(portfoliomodel.AvailabilityUnknown)
			lossBound = portfoliomodel.LossBoundUnbounded
		}
		incremental, err := portfoliomodel.NewExposureRecord(portfoliomodel.ExposureRecordSpec{
			Dimension: portfoliomodel.ExposureDimension(parts[0]), Subject: parts[1],
			Gross: known(value.gross), NetDirectional: known(value.net), PremiumAtRisk: known(value.paid),
			Long: known(value.paid), Short: known(value.received), PremiumPaid: known(value.paid),
			PremiumReceived: known(value.received), MaximumLoss: maximumLoss, LossBound: lossBound,
		})
		if err != nil {
			return nil, nil, err
		}
		current, err := portfoliomodel.NewExposureRecord(portfoliomodel.ExposureRecordSpec{
			Dimension: incremental.Dimension(), Subject: incremental.Subject(), Gross: zero,
			NetDirectional: zero, PremiumAtRisk: zero, Long: zero, Short: zero,
			PremiumPaid: zero, PremiumReceived: zero, MaximumLoss: zero, LossBound: portfoliomodel.LossBoundKnown,
		})
		if err != nil {
			return nil, nil, err
		}
		for _, existing := range input.Snapshot.Exposures() {
			if existing.Dimension() == incremental.Dimension() && existing.Subject() == incremental.Subject() {
				current = existing
				break
			}
		}
		projected, err := portfoliomodel.ProjectExposure(current, incremental)
		if err != nil {
			return nil, nil, err
		}
		incrementalValues = append(incrementalValues, incremental)
		projectedValues = append(projectedValues, projected)
	}
	return incrementalValues, projectedValues, nil
}

func checkedMul(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || (left != 0 && right > math.MaxInt64/left) {
		return 0, false
	}
	return left * right, true
}
