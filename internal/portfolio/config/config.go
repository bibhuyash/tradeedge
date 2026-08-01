package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/canonicaljson"
	"github.com/bibhuyash/tradeedge/internal/domain"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

const (
	MaximumConfigurationBytes = 256 << 10
	MaximumConfigurationDepth = 16
	MaximumCollectionEntries  = 100
)

var (
	ErrInvalidConfiguration   = errors.New("invalid portfolio configuration")
	ErrConfigurationCollision = errors.New("portfolio configuration identity collision")
)

type Limits struct {
	TotalCapital                domain.Money
	ReserveBPS                  portfoliomodel.BasisPoints
	EmergencyReserveBPS         portfoliomodel.BasisPoints
	MaximumStrategyCapital      domain.Money
	MaximumInstrumentCapital    domain.Money
	MaximumUnderlyingCapital    domain.Money
	MaximumExposureGroupCapital domain.Money
	MaximumStrategies           int
	ExposureGroups              []string
}

type AllocationPolicy struct {
	ID                portfoliomodel.AllocationPolicyID
	Version           portfoliomodel.AllocationPolicyVersion
	Enabled           bool
	EffectiveFrom     time.Time
	EffectiveUntil    time.Time
	Limits            Limits
	ConfigurationHash portfoliomodel.ConfigurationHash
}

type PortfolioConfiguration struct {
	id        portfoliomodel.PortfolioConfigurationID
	version   portfoliomodel.PortfolioConfigurationVersion
	hash      portfoliomodel.ConfigurationHash
	schema    string
	base      domain.Currency
	enabled   bool
	policy    AllocationPolicy
	canonical []byte
}

type document struct {
	SchemaVersion                    string   `json:"schema_version"`
	Version                          uint64   `json:"version"`
	Enabled                          bool     `json:"enabled"`
	BaseCurrency                     string   `json:"base_currency"`
	EffectiveFrom                    string   `json:"effective_from"`
	EffectiveUntil                   string   `json:"effective_until"`
	TotalCapitalMinor                int64    `json:"total_capital_minor"`
	ReserveBPS                       int32    `json:"reserve_bps"`
	EmergencyReserveBPS              int32    `json:"emergency_reserve_bps"`
	MaximumStrategyCapitalMinor      int64    `json:"maximum_strategy_capital_minor"`
	MaximumInstrumentCapitalMinor    int64    `json:"maximum_instrument_capital_minor"`
	MaximumUnderlyingCapitalMinor    int64    `json:"maximum_underlying_capital_minor"`
	MaximumExposureGroupCapitalMinor int64    `json:"maximum_exposure_group_capital_minor"`
	MaximumStrategies                int      `json:"maximum_strategies"`
	ExposureGroups                   []string `json:"exposure_groups"`
}

func Decode(raw []byte) (PortfolioConfiguration, error) {
	canonical, err := canonicaljson.ObjectBounded(raw, canonicaljson.Limits{
		MaximumBytes: MaximumConfigurationBytes, MaximumDepth: MaximumConfigurationDepth,
		MaximumCollection: MaximumCollectionEntries,
	})
	if err != nil {
		return PortfolioConfiguration{}, fmt.Errorf("%w: %v", ErrInvalidConfiguration, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var input document
	if err := decoder.Decode(&input); err != nil {
		return PortfolioConfiguration{}, fmt.Errorf("%w: %v", ErrInvalidConfiguration, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return PortfolioConfiguration{}, ErrInvalidConfiguration
	}
	return newConfiguration(input, canonical)
}

func newConfiguration(input document, canonical []byte) (PortfolioConfiguration, error) {
	if strings.TrimSpace(input.SchemaVersion) == "" || input.Version == 0 ||
		input.TotalCapitalMinor <= 0 || input.MaximumStrategyCapitalMinor <= 0 ||
		input.MaximumInstrumentCapitalMinor <= 0 || input.MaximumUnderlyingCapitalMinor <= 0 ||
		input.MaximumExposureGroupCapitalMinor <= 0 || input.MaximumStrategies <= 0 ||
		input.MaximumStrategies > portfoliomodel.MaximumStrategiesPerPortfolio ||
		len(input.ExposureGroups) == 0 || len(input.ExposureGroups) > MaximumCollectionEntries {
		return PortfolioConfiguration{}, ErrInvalidConfiguration
	}
	currency, err := domain.NewCurrency(input.BaseCurrency)
	if err != nil {
		return PortfolioConfiguration{}, ErrInvalidConfiguration
	}
	effectiveFrom, err := time.Parse(time.RFC3339Nano, input.EffectiveFrom)
	if err != nil {
		return PortfolioConfiguration{}, ErrInvalidConfiguration
	}
	effectiveUntil, err := time.Parse(time.RFC3339Nano, input.EffectiveUntil)
	if err != nil || !effectiveUntil.After(effectiveFrom) {
		return PortfolioConfiguration{}, ErrInvalidConfiguration
	}
	reserve, err := portfoliomodel.NewBasisPoints(input.ReserveBPS)
	if err != nil {
		return PortfolioConfiguration{}, ErrInvalidConfiguration
	}
	emergency, err := portfoliomodel.NewBasisPoints(input.EmergencyReserveBPS)
	if err != nil || int64(reserve)+int64(emergency) > 10000 {
		return PortfolioConfiguration{}, ErrInvalidConfiguration
	}
	total, _ := domain.NewMoney(input.TotalCapitalMinor, currency.String())
	strategy, _ := domain.NewMoney(input.MaximumStrategyCapitalMinor, currency.String())
	instrument, _ := domain.NewMoney(input.MaximumInstrumentCapitalMinor, currency.String())
	underlying, _ := domain.NewMoney(input.MaximumUnderlyingCapitalMinor, currency.String())
	group, _ := domain.NewMoney(input.MaximumExposureGroupCapitalMinor, currency.String())
	for _, limit := range []domain.Money{strategy, instrument, underlying, group} {
		if limit.MinorUnits() > total.MinorUnits() {
			return PortfolioConfiguration{}, ErrInvalidConfiguration
		}
	}
	groups := append([]string(nil), input.ExposureGroups...)
	seen := make(map[string]struct{}, len(groups))
	for index, value := range groups {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > portfoliomodel.MaximumSubjectBytes {
			return PortfolioConfiguration{}, ErrInvalidConfiguration
		}
		if _, exists := seen[value]; exists {
			return PortfolioConfiguration{}, ErrInvalidConfiguration
		}
		seen[value] = struct{}{}
		groups[index] = value
	}
	sort.Strings(groups)
	input.SchemaVersion = strings.TrimSpace(input.SchemaVersion)
	input.BaseCurrency = currency.String()
	input.EffectiveFrom = effectiveFrom.UTC().Format(time.RFC3339Nano)
	input.EffectiveUntil = effectiveUntil.UTC().Format(time.RFC3339Nano)
	input.ExposureGroups = groups
	normalizedRaw, marshalErr := json.Marshal(input)
	if marshalErr != nil {
		return PortfolioConfiguration{}, ErrInvalidConfiguration
	}
	canonical, err = canonicaljson.ObjectBounded(normalizedRaw, canonicaljson.Limits{
		MaximumBytes: MaximumConfigurationBytes, MaximumDepth: MaximumConfigurationDepth,
		MaximumCollection: MaximumCollectionEntries,
	})
	if err != nil {
		return PortfolioConfiguration{}, ErrInvalidConfiguration
	}
	hash, _ := portfoliomodel.NewConfigurationHash(canonical)
	id, _ := portfoliomodel.NewPortfolioConfigurationID(
		input.SchemaVersion, fmt.Sprint(input.Version), hash.String(),
	)
	policyID, _ := portfoliomodel.NewAllocationPolicyID(
		"portfolio-allocation-policy/v1", id.String(), fmt.Sprint(input.Version),
	)
	limits := Limits{
		TotalCapital: total, ReserveBPS: reserve, EmergencyReserveBPS: emergency,
		MaximumStrategyCapital: strategy, MaximumInstrumentCapital: instrument,
		MaximumUnderlyingCapital: underlying, MaximumExposureGroupCapital: group,
		MaximumStrategies: input.MaximumStrategies, ExposureGroups: groups,
	}
	policy := AllocationPolicy{
		ID: policyID, Version: portfoliomodel.AllocationPolicyVersion(input.Version),
		Enabled: input.Enabled, EffectiveFrom: effectiveFrom.UTC(),
		EffectiveUntil: effectiveUntil.UTC(), Limits: limits, ConfigurationHash: hash,
	}
	return PortfolioConfiguration{
		id: id, version: portfoliomodel.PortfolioConfigurationVersion(input.Version),
		hash: hash, schema: input.SchemaVersion, base: currency, enabled: input.Enabled,
		policy: policy, canonical: append([]byte(nil), canonical...),
	}, nil
}

func (value PortfolioConfiguration) ID() portfoliomodel.PortfolioConfigurationID { return value.id }
func (value PortfolioConfiguration) Version() portfoliomodel.PortfolioConfigurationVersion {
	return value.version
}
func (value PortfolioConfiguration) Hash() portfoliomodel.ConfigurationHash { return value.hash }
func (value PortfolioConfiguration) SchemaVersion() string                  { return value.schema }
func (value PortfolioConfiguration) BaseCurrency() domain.Currency          { return value.base }
func (value PortfolioConfiguration) Enabled() bool                          { return value.enabled }
func (value PortfolioConfiguration) AllocationPolicy() AllocationPolicy {
	result := value.policy
	result.Limits.ExposureGroups = append([]string(nil), result.Limits.ExposureGroups...)
	return result
}
func (value PortfolioConfiguration) CanonicalJSON() []byte {
	return append([]byte(nil), value.canonical...)
}
