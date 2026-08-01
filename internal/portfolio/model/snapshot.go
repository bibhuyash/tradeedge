package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	strategymodel "github.com/bibhuyash/tradeedge/internal/strategy/model"
)

const (
	MaximumStrategiesPerPortfolio = 20
	MaximumSnapshotBytes          = 1 << 20
)

var ErrInvalidPortfolioSnapshot = errors.New("invalid portfolio snapshot")

type PortfolioState string

const (
	PortfolioEnabled      PortfolioState = "ENABLED"
	PortfolioDisabled     PortfolioState = "DISABLED"
	PortfolioRecovering   PortfolioState = "RECOVERING"
	PortfolioInconsistent PortfolioState = "INCONSISTENT"
)

func (value PortfolioState) Validate() error {
	switch value {
	case PortfolioEnabled, PortfolioDisabled, PortfolioRecovering, PortfolioInconsistent:
		return nil
	default:
		return ErrInvalidPortfolioSnapshot
	}
}

type CapitalState struct {
	Total     domain.Money
	Available domain.Money
	Reserved  domain.Money
	Deployed  domain.Money
}

func NewCapitalState(total, available, reserved, deployed domain.Money) (CapitalState, error) {
	values := []domain.Money{total, available, reserved, deployed}
	for _, value := range values {
		if value.IsZeroValue() || value.MinorUnits() < 0 || value.Currency() != total.Currency() {
			return CapitalState{}, ErrInvalidPortfolioSnapshot
		}
	}
	sum, err := available.Add(reserved)
	if err != nil {
		return CapitalState{}, err
	}
	sum, err = sum.Add(deployed)
	if err != nil || sum.MinorUnits() != total.MinorUnits() {
		return CapitalState{}, ErrInvalidPortfolioSnapshot
	}
	return CapitalState{Total: total, Available: available, Reserved: reserved, Deployed: deployed}, nil
}

type Drawdown struct {
	Amount domain.Money
	BPS    BasisPoints
}

type StrategyAllocationState string

const (
	StrategyAllocationEnabled   StrategyAllocationState = "ENABLED"
	StrategyAllocationDisabled  StrategyAllocationState = "DISABLED"
	StrategyAllocationExhausted StrategyAllocationState = "EXHAUSTED"
	StrategyAllocationExpired   StrategyAllocationState = "EXPIRED"
)

type StrategyAllocationSpec struct {
	ID                 StrategyAllocationID
	DefinitionID       strategymodel.DefinitionID
	VersionID          strategymodel.VersionID
	InstanceID         domain.StrategyID
	InstanceRevisionID strategymodel.InstanceRevisionID
	PolicyID           AllocationPolicyID
	PolicyVersion      AllocationPolicyVersion
	Limit              domain.Money
	Deployed           domain.Money
	Reserved           domain.Money
	Remaining          domain.Money
	DailyLoss          domain.Money
	State              StrategyAllocationState
	EffectiveFrom      time.Time
	EffectiveUntil     time.Time
	ConfigurationHash  ConfigurationHash
	SchemaVersion      string
}

type StrategyAllocation struct{ spec StrategyAllocationSpec }

func NewStrategyAllocation(spec StrategyAllocationSpec) (StrategyAllocation, error) {
	if spec.ID.IsZero() || spec.DefinitionID == "" || spec.VersionID.IsZero() ||
		strings.TrimSpace(string(spec.InstanceID)) == "" || spec.InstanceRevisionID.IsZero() ||
		spec.PolicyID.IsZero() || spec.PolicyVersion.Validate() != nil ||
		spec.ConfigurationHash.IsZero() || strings.TrimSpace(spec.SchemaVersion) == "" ||
		spec.EffectiveFrom.IsZero() || !spec.EffectiveUntil.After(spec.EffectiveFrom) {
		return StrategyAllocation{}, ErrInvalidPortfolioSnapshot
	}
	values := []domain.Money{spec.Limit, spec.Deployed, spec.Reserved, spec.Remaining, spec.DailyLoss}
	for index, value := range values {
		if value.IsZeroValue() || value.Currency() != spec.Limit.Currency() ||
			(index < 4 && value.MinorUnits() < 0) {
			return StrategyAllocation{}, ErrInvalidPortfolioSnapshot
		}
	}
	used, err := spec.Deployed.Add(spec.Reserved)
	if err != nil {
		return StrategyAllocation{}, err
	}
	used, err = used.Add(spec.Remaining)
	if err != nil || used.MinorUnits() != spec.Limit.MinorUnits() {
		return StrategyAllocation{}, ErrInvalidPortfolioSnapshot
	}
	switch spec.State {
	case StrategyAllocationEnabled, StrategyAllocationDisabled,
		StrategyAllocationExhausted, StrategyAllocationExpired:
	default:
		return StrategyAllocation{}, ErrInvalidPortfolioSnapshot
	}
	if spec.State == StrategyAllocationExhausted && spec.Remaining.MinorUnits() != 0 {
		return StrategyAllocation{}, ErrInvalidPortfolioSnapshot
	}
	spec.EffectiveFrom = spec.EffectiveFrom.UTC()
	spec.EffectiveUntil = spec.EffectiveUntil.UTC()
	return StrategyAllocation{spec: spec}, nil
}

func (value StrategyAllocation) Spec() StrategyAllocationSpec { return value.spec }
func (value StrategyAllocation) ID() StrategyAllocationID     { return value.spec.ID }

type PortfolioSnapshotSpec struct {
	SchemaVersion        string
	PortfolioID          PortfolioID
	Revision             PortfolioRevision
	AsOfExchangeTime     time.Time
	GeneratedAt          time.Time
	TradingDate          domain.CivilDate
	BaseCurrency         domain.Currency
	State                PortfolioState
	ConfigurationID      PortfolioConfigurationID
	ConfigurationVersion PortfolioConfigurationVersion
	ConfigurationHash    ConfigurationHash
	Capital              CapitalState
	RealizedPnL          domain.Money
	UnrealizedPnL        domain.Money
	DailyRealizedPnL     domain.Money
	DailyUnrealizedPnL   domain.Money
	WeeklyRealizedPnL    domain.Money
	HighWaterMark        domain.Money
	CurrentEquity        domain.Money
	StrategyAllocations  []StrategyAllocation
	Exposures            []ExposureRecord
	KillSwitches         []KillSwitch
	CircuitBreakers      []CircuitBreaker
	SourceStateChecksum  StateChecksum
}

type PortfolioSnapshot struct {
	id   PortfolioSnapshotID
	spec PortfolioSnapshotSpec
	raw  []byte
}

func NewPortfolioSnapshot(spec PortfolioSnapshotSpec) (PortfolioSnapshot, error) {
	spec.SchemaVersion = strings.TrimSpace(spec.SchemaVersion)
	if spec.SchemaVersion == "" || spec.PortfolioID.IsZero() ||
		spec.Revision.Validate() != nil || spec.AsOfExchangeTime.IsZero() ||
		spec.GeneratedAt.IsZero() || spec.TradingDate.IsZero() ||
		spec.BaseCurrency == "" || spec.State.Validate() != nil ||
		spec.ConfigurationID.IsZero() || spec.ConfigurationVersion.Validate() != nil ||
		spec.ConfigurationHash.IsZero() || spec.SourceStateChecksum.IsZero() {
		return PortfolioSnapshot{}, ErrInvalidPortfolioSnapshot
	}
	spec.AsOfExchangeTime = spec.AsOfExchangeTime.UTC()
	spec.GeneratedAt = spec.GeneratedAt.UTC()
	money := []domain.Money{
		spec.Capital.Total, spec.Capital.Available, spec.Capital.Reserved, spec.Capital.Deployed,
		spec.RealizedPnL, spec.UnrealizedPnL, spec.DailyRealizedPnL,
		spec.DailyUnrealizedPnL, spec.WeeklyRealizedPnL, spec.HighWaterMark, spec.CurrentEquity,
	}
	for _, value := range money {
		if value.IsZeroValue() || value.Currency() != spec.BaseCurrency {
			return PortfolioSnapshot{}, ErrInvalidPortfolioSnapshot
		}
	}
	if _, err := NewCapitalState(spec.Capital.Total, spec.Capital.Available,
		spec.Capital.Reserved, spec.Capital.Deployed); err != nil {
		return PortfolioSnapshot{}, err
	}
	expectedEquity, err := spec.Capital.Total.Add(spec.RealizedPnL)
	if err != nil {
		return PortfolioSnapshot{}, err
	}
	expectedEquity, err = expectedEquity.Add(spec.UnrealizedPnL)
	if err != nil || expectedEquity.MinorUnits() != spec.CurrentEquity.MinorUnits() ||
		spec.CurrentEquity.MinorUnits() < 0 ||
		spec.HighWaterMark.MinorUnits() < spec.CurrentEquity.MinorUnits() {
		return PortfolioSnapshot{}, ErrInvalidPortfolioSnapshot
	}
	allocations, err := normalizeStrategyAllocations(spec.StrategyAllocations, spec.BaseCurrency)
	if err != nil {
		return PortfolioSnapshot{}, err
	}
	exposures, err := normalizeExposures(spec.Exposures)
	if err != nil {
		return PortfolioSnapshot{}, err
	}
	spec.StrategyAllocations = allocations
	spec.Exposures = exposures
	spec.KillSwitches = append([]KillSwitch(nil), spec.KillSwitches...)
	spec.CircuitBreakers = append([]CircuitBreaker(nil), spec.CircuitBreakers...)
	if len(spec.KillSwitches) > 64 || len(spec.CircuitBreakers) > 64 {
		return PortfolioSnapshot{}, ErrInvalidPortfolioSnapshot
	}
	seenKillSwitches := make(map[KillSwitchID]struct{}, len(spec.KillSwitches))
	for _, value := range spec.KillSwitches {
		if value.spec.ID.IsZero() {
			return PortfolioSnapshot{}, ErrInvalidPortfolioSnapshot
		}
		if _, exists := seenKillSwitches[value.spec.ID]; exists {
			return PortfolioSnapshot{}, ErrInvalidPortfolioSnapshot
		}
		seenKillSwitches[value.spec.ID] = struct{}{}
	}
	seenCircuitBreakers := make(map[CircuitBreakerID]struct{}, len(spec.CircuitBreakers))
	for _, value := range spec.CircuitBreakers {
		if value.spec.ID.IsZero() {
			return PortfolioSnapshot{}, ErrInvalidPortfolioSnapshot
		}
		if _, exists := seenCircuitBreakers[value.spec.ID]; exists {
			return PortfolioSnapshot{}, ErrInvalidPortfolioSnapshot
		}
		seenCircuitBreakers[value.spec.ID] = struct{}{}
	}
	sort.Slice(spec.KillSwitches, func(i, j int) bool {
		return spec.KillSwitches[i].spec.ID.String() < spec.KillSwitches[j].spec.ID.String()
	})
	sort.Slice(spec.CircuitBreakers, func(i, j int) bool {
		return spec.CircuitBreakers[i].spec.ID.String() < spec.CircuitBreakers[j].spec.ID.String()
	})
	raw, err := canonicalSnapshot(spec)
	if err != nil || len(raw) > MaximumSnapshotBytes {
		return PortfolioSnapshot{}, ErrInvalidPortfolioSnapshot
	}
	id := deriveSnapshotID(spec.PortfolioID.String(), fmt.Sprint(spec.Revision),
		string(raw), spec.SourceStateChecksum.String())
	return PortfolioSnapshot{id: id, spec: spec, raw: raw}, nil
}

func normalizeStrategyAllocations(values []StrategyAllocation, currency domain.Currency) ([]StrategyAllocation, error) {
	if len(values) > MaximumStrategiesPerPortfolio {
		return nil, ErrInvalidPortfolioSnapshot
	}
	result := append([]StrategyAllocation(nil), values...)
	seen := make(map[StrategyAllocationID]struct{}, len(result))
	for _, value := range result {
		validated, err := NewStrategyAllocation(value.spec)
		if err != nil || validated.spec.Limit.Currency() != currency {
			return nil, ErrInvalidPortfolioSnapshot
		}
		if _, exists := seen[value.ID()]; exists {
			return nil, ErrInvalidPortfolioSnapshot
		}
		seen[value.ID()] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID().String() < result[j].ID().String() })
	return result, nil
}

func (value PortfolioSnapshot) ID() PortfolioSnapshotID     { return value.id }
func (value PortfolioSnapshot) PortfolioID() PortfolioID    { return value.spec.PortfolioID }
func (value PortfolioSnapshot) Revision() PortfolioRevision { return value.spec.Revision }
func (value PortfolioSnapshot) Capital() CapitalState       { return value.spec.Capital }
func (value PortfolioSnapshot) State() PortfolioState       { return value.spec.State }
func (value PortfolioSnapshot) ConfigurationHash() ConfigurationHash {
	return value.spec.ConfigurationHash
}
func (value PortfolioSnapshot) StrategyAllocations() []StrategyAllocation {
	return append([]StrategyAllocation(nil), value.spec.StrategyAllocations...)
}
func (value PortfolioSnapshot) Exposures() []ExposureRecord {
	return append([]ExposureRecord(nil), value.spec.Exposures...)
}
func (value PortfolioSnapshot) CanonicalJSON() []byte { return append([]byte(nil), value.raw...) }
func (value PortfolioSnapshot) Spec() PortfolioSnapshotSpec {
	result := value.spec
	result.StrategyAllocations = append([]StrategyAllocation(nil), result.StrategyAllocations...)
	result.Exposures = append([]ExposureRecord(nil), result.Exposures...)
	result.KillSwitches = append([]KillSwitch(nil), result.KillSwitches...)
	result.CircuitBreakers = append([]CircuitBreaker(nil), result.CircuitBreakers...)
	return result
}
func (value PortfolioSnapshot) Drawdown() Drawdown {
	amount, _ := CheckedMoneySubtract(value.spec.HighWaterMark, value.spec.CurrentEquity)
	var bps BasisPoints
	if value.spec.HighWaterMark.MinorUnits() > 0 {
		numerator := new(big.Int).Mul(big.NewInt(amount.MinorUnits()), big.NewInt(10000))
		numerator.Quo(numerator, big.NewInt(value.spec.HighWaterMark.MinorUnits()))
		bps, _ = NewBasisPoints(int32(numerator.Int64()))
	}
	return Drawdown{Amount: amount, BPS: bps}
}

type moneyWire struct {
	Minor    int64  `json:"minor"`
	Currency string `json:"currency"`
}

func moneyValue(value domain.Money) moneyWire {
	return moneyWire{Minor: value.MinorUnits(), Currency: value.Currency().String()}
}

func canonicalSnapshot(spec PortfolioSnapshotSpec) ([]byte, error) {
	type allocationWire struct {
		ID, DefinitionID, VersionID, InstanceID, InstanceRevisionID       string
		PolicyID, ConfigurationHash, State, EffectiveFrom, EffectiveUntil string
		PolicyVersion                                                     uint64
		Limit, Deployed, Reserved, Remaining, DailyLoss                   moneyWire
	}
	allocations := make([]allocationWire, len(spec.StrategyAllocations))
	for index, value := range spec.StrategyAllocations {
		item := value.spec
		allocations[index] = allocationWire{
			ID: item.ID.String(), DefinitionID: item.DefinitionID.String(),
			VersionID: item.VersionID.String(), InstanceID: string(item.InstanceID),
			InstanceRevisionID: item.InstanceRevisionID.String(), PolicyID: item.PolicyID.String(),
			PolicyVersion: uint64(item.PolicyVersion), Limit: moneyValue(item.Limit),
			Deployed: moneyValue(item.Deployed), Reserved: moneyValue(item.Reserved),
			Remaining: moneyValue(item.Remaining), DailyLoss: moneyValue(item.DailyLoss),
			State: string(item.State), EffectiveFrom: item.EffectiveFrom.Format(time.RFC3339Nano),
			EffectiveUntil:    item.EffectiveUntil.Format(time.RFC3339Nano),
			ConfigurationHash: item.ConfigurationHash.String(),
		}
	}
	type exposureWire struct {
		Dimension, Subject, LossBound                             string
		Gross, Net, Premium, Long, Short, Paid, Received, MaxLoss string
		Overnight                                                 bool
	}
	exposures := make([]exposureWire, len(spec.Exposures))
	for index, value := range spec.Exposures {
		exposures[index] = exposureWire{
			Dimension: string(value.Dimension()), Subject: value.Subject(),
			LossBound: string(value.LossBound()), Gross: measuredKey(value.Gross()),
			Net: measuredKey(value.NetDirectional()), Premium: measuredKey(value.PremiumAtRisk()),
			Long: measuredKey(value.spec.Long), Short: measuredKey(value.spec.Short),
			Paid: measuredKey(value.spec.PremiumPaid), Received: measuredKey(value.spec.PremiumReceived),
			MaxLoss: measuredKey(value.spec.MaximumLoss), Overnight: value.Overnight(),
		}
	}
	type controlWire struct {
		ID, Scope, Subject, State, Reason, Evidence, ChangedAt, ExpiresAt string
		ConfigurationID, ConfigurationHash, SchemaVersion                 string
		StateRevision                                                     uint64
	}
	killSwitches := make([]controlWire, len(spec.KillSwitches))
	for index, value := range spec.KillSwitches {
		item := value.spec
		killSwitches[index] = controlWire{
			ID: item.ID.String(), Scope: string(item.Scope), Subject: item.ScopeSubject,
			State: string(item.State), Reason: string(item.ReasonCode),
			Evidence:          item.ActivationEvidence.String(),
			ChangedAt:         item.ActivatedAt.Format(time.RFC3339Nano),
			ExpiresAt:         item.ExpiresAt.Format(time.RFC3339Nano),
			ConfigurationID:   item.ConfigurationID.String(),
			ConfigurationHash: item.ConfigurationHash.String(),
			StateRevision:     item.StateRevision, SchemaVersion: item.SchemaVersion,
		}
	}
	circuitBreakers := make([]controlWire, len(spec.CircuitBreakers))
	for index, value := range spec.CircuitBreakers {
		item := value.spec
		circuitBreakers[index] = controlWire{
			ID: item.ID.String(), Scope: string(item.Scope), Subject: item.ScopeSubject,
			State: string(item.State), Reason: string(item.ReasonCode), Evidence: item.Evidence.String(),
			ChangedAt:         item.ChangedAt.Format(time.RFC3339Nano),
			ConfigurationID:   item.ConfigurationID.String(),
			ConfigurationHash: item.ConfigurationHash.String(),
			StateRevision:     item.StateRevision, SchemaVersion: item.SchemaVersion,
		}
	}
	return json.Marshal(struct {
		SchemaVersion, PortfolioID, AsOfExchangeTime, GeneratedAt, TradingDate  string
		BaseCurrency, State, ConfigurationID, ConfigurationHash, SourceChecksum string
		Revision, ConfigurationVersion                                          uint64
		Total, Available, Reserved, Deployed                                    moneyWire
		RealizedPnL, UnrealizedPnL, DailyRealizedPnL, DailyUnrealizedPnL        moneyWire
		WeeklyRealizedPnL, HighWaterMark, CurrentEquity                         moneyWire
		Allocations                                                             []allocationWire
		Exposures                                                               []exposureWire
		KillSwitches                                                            []controlWire
		CircuitBreakers                                                         []controlWire
	}{
		SchemaVersion: spec.SchemaVersion, PortfolioID: spec.PortfolioID.String(),
		Revision: uint64(spec.Revision), AsOfExchangeTime: spec.AsOfExchangeTime.Format(time.RFC3339Nano),
		GeneratedAt: spec.GeneratedAt.Format(time.RFC3339Nano), TradingDate: spec.TradingDate.String(),
		BaseCurrency: spec.BaseCurrency.String(), State: string(spec.State),
		ConfigurationID:      spec.ConfigurationID.String(),
		ConfigurationVersion: uint64(spec.ConfigurationVersion),
		ConfigurationHash:    spec.ConfigurationHash.String(),
		Total:                moneyValue(spec.Capital.Total), Available: moneyValue(spec.Capital.Available),
		Reserved: moneyValue(spec.Capital.Reserved), Deployed: moneyValue(spec.Capital.Deployed),
		RealizedPnL: moneyValue(spec.RealizedPnL), UnrealizedPnL: moneyValue(spec.UnrealizedPnL),
		DailyRealizedPnL:   moneyValue(spec.DailyRealizedPnL),
		DailyUnrealizedPnL: moneyValue(spec.DailyUnrealizedPnL),
		WeeklyRealizedPnL:  moneyValue(spec.WeeklyRealizedPnL),
		HighWaterMark:      moneyValue(spec.HighWaterMark), CurrentEquity: moneyValue(spec.CurrentEquity),
		Allocations: allocations, Exposures: exposures, KillSwitches: killSwitches,
		CircuitBreakers: circuitBreakers, SourceChecksum: spec.SourceStateChecksum.String(),
	})
}

func measuredKey(value MeasuredMoney) string {
	money, known := value.Value()
	if !known {
		return string(value.Availability())
	}
	return fmt.Sprintf("%s:%d", money.Currency(), money.MinorUnits())
}
