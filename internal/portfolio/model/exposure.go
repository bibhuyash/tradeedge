package model

import (
	"errors"
	"sort"
	"strings"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

const (
	MaximumExposureRecords = 100
	MaximumSubjectBytes    = 128
)

var (
	ErrInvalidExposure         = errors.New("invalid portfolio exposure")
	ErrUnavailableExposureData = errors.New("portfolio exposure data unavailable")
)

type Availability string

const (
	AvailabilityKnown         Availability = "KNOWN"
	AvailabilityUnknown       Availability = "UNKNOWN"
	AvailabilityNotApplicable Availability = "NOT_APPLICABLE"
	AvailabilityUnavailable   Availability = "UNAVAILABLE"
)

func (value Availability) Validate() error {
	switch value {
	case AvailabilityKnown, AvailabilityUnknown, AvailabilityNotApplicable, AvailabilityUnavailable:
		return nil
	default:
		return ErrInvalidExposure
	}
}

type ExposureDimension string

const (
	ExposureInstrument    ExposureDimension = "INSTRUMENT"
	ExposureUnderlying    ExposureDimension = "UNDERLYING"
	ExposureStrategy      ExposureDimension = "STRATEGY"
	ExposureGroup         ExposureDimension = "EXPOSURE_GROUP"
	ExposureExpiry        ExposureDimension = "EXPIRY"
	ExposurePortfolioWide ExposureDimension = "PORTFOLIO"
)

func (value ExposureDimension) Validate() error {
	switch value {
	case ExposureInstrument, ExposureUnderlying, ExposureStrategy,
		ExposureGroup, ExposureExpiry, ExposurePortfolioWide:
		return nil
	default:
		return ErrInvalidExposure
	}
}

type MeasuredMoney struct {
	availability Availability
	value        domain.Money
}

func NewKnownMoney(value domain.Money) (MeasuredMoney, error) {
	if value.IsZeroValue() {
		return MeasuredMoney{}, ErrInvalidExposure
	}
	return MeasuredMoney{availability: AvailabilityKnown, value: value}, nil
}

func NewUnavailableMoney(state Availability) (MeasuredMoney, error) {
	if state != AvailabilityUnknown && state != AvailabilityNotApplicable &&
		state != AvailabilityUnavailable {
		return MeasuredMoney{}, ErrInvalidExposure
	}
	return MeasuredMoney{availability: state}, nil
}

func (value MeasuredMoney) Availability() Availability { return value.availability }
func (value MeasuredMoney) Value() (domain.Money, bool) {
	return value.value, value.availability == AvailabilityKnown
}
func (value MeasuredMoney) Validate() error {
	if value.availability.Validate() != nil {
		return ErrInvalidExposure
	}
	if value.availability == AvailabilityKnown && value.value.IsZeroValue() {
		return ErrInvalidExposure
	}
	if value.availability != AvailabilityKnown && !value.value.IsZeroValue() {
		return ErrInvalidExposure
	}
	return nil
}

type LossBoundState string

const (
	LossBoundKnown     LossBoundState = "KNOWN"
	LossBoundUnknown   LossBoundState = "UNKNOWN"
	LossBoundUnbounded LossBoundState = "UNBOUNDED"
)

type ExposureRecordSpec struct {
	Dimension       ExposureDimension
	Subject         string
	Gross           MeasuredMoney
	NetDirectional  MeasuredMoney
	PremiumAtRisk   MeasuredMoney
	Long            MeasuredMoney
	Short           MeasuredMoney
	PremiumPaid     MeasuredMoney
	PremiumReceived MeasuredMoney
	MaximumLoss     MeasuredMoney
	LossBound       LossBoundState
	Overnight       bool
}

type ExposureRecord struct {
	spec ExposureRecordSpec
}

func NewExposureRecord(spec ExposureRecordSpec) (ExposureRecord, error) {
	spec.Subject = strings.TrimSpace(spec.Subject)
	if spec.Dimension.Validate() != nil || spec.Subject == "" ||
		len(spec.Subject) > MaximumSubjectBytes {
		return ExposureRecord{}, ErrInvalidExposure
	}
	values := []MeasuredMoney{
		spec.Gross, spec.NetDirectional, spec.PremiumAtRisk, spec.Long, spec.Short,
		spec.PremiumPaid, spec.PremiumReceived, spec.MaximumLoss,
	}
	for _, value := range values {
		if value.Validate() != nil {
			return ExposureRecord{}, ErrInvalidExposure
		}
	}
	switch spec.LossBound {
	case LossBoundKnown:
		if spec.MaximumLoss.availability != AvailabilityKnown ||
			spec.MaximumLoss.value.MinorUnits() < 0 {
			return ExposureRecord{}, ErrInvalidExposure
		}
	case LossBoundUnknown, LossBoundUnbounded:
		if spec.MaximumLoss.availability == AvailabilityKnown {
			return ExposureRecord{}, ErrInvalidExposure
		}
	default:
		return ExposureRecord{}, ErrInvalidExposure
	}
	if err := validateExposureCurrencies(values); err != nil {
		return ExposureRecord{}, err
	}
	for _, nonnegative := range []MeasuredMoney{
		spec.Gross, spec.PremiumAtRisk, spec.Long, spec.Short,
		spec.PremiumPaid, spec.PremiumReceived,
	} {
		if money, known := nonnegative.Value(); known && money.MinorUnits() < 0 {
			return ExposureRecord{}, ErrInvalidExposure
		}
	}
	return ExposureRecord{spec: spec}, nil
}

func validateExposureCurrencies(values []MeasuredMoney) error {
	var currency domain.Currency
	for _, value := range values {
		money, known := value.Value()
		if !known {
			continue
		}
		if currency == "" {
			currency = money.Currency()
		} else if currency != money.Currency() {
			return domain.ErrCurrencyMismatch
		}
	}
	return nil
}

func (value ExposureRecord) Dimension() ExposureDimension  { return value.spec.Dimension }
func (value ExposureRecord) Subject() string               { return value.spec.Subject }
func (value ExposureRecord) Gross() MeasuredMoney          { return value.spec.Gross }
func (value ExposureRecord) NetDirectional() MeasuredMoney { return value.spec.NetDirectional }
func (value ExposureRecord) PremiumAtRisk() MeasuredMoney  { return value.spec.PremiumAtRisk }
func (value ExposureRecord) MaximumLoss() MeasuredMoney    { return value.spec.MaximumLoss }
func (value ExposureRecord) LossBound() LossBoundState     { return value.spec.LossBound }
func (value ExposureRecord) Overnight() bool               { return value.spec.Overnight }
func (value ExposureRecord) Spec() ExposureRecordSpec      { return value.spec }

func ProjectExposure(current, incremental ExposureRecord) (ExposureRecord, error) {
	if current.Dimension() != incremental.Dimension() || current.Subject() != incremental.Subject() {
		return ExposureRecord{}, ErrInvalidExposure
	}
	project := current.spec
	var err error
	project.Gross, err = addMeasured(current.spec.Gross, incremental.spec.Gross)
	if err != nil {
		return ExposureRecord{}, err
	}
	project.NetDirectional, err = addMeasured(current.spec.NetDirectional, incremental.spec.NetDirectional)
	if err != nil {
		return ExposureRecord{}, err
	}
	project.PremiumAtRisk, err = addMeasured(current.spec.PremiumAtRisk, incremental.spec.PremiumAtRisk)
	if err != nil {
		return ExposureRecord{}, err
	}
	project.Long, err = addMeasured(current.spec.Long, incremental.spec.Long)
	if err != nil {
		return ExposureRecord{}, err
	}
	project.Short, err = addMeasured(current.spec.Short, incremental.spec.Short)
	if err != nil {
		return ExposureRecord{}, err
	}
	project.PremiumPaid, err = addMeasured(current.spec.PremiumPaid, incremental.spec.PremiumPaid)
	if err != nil {
		return ExposureRecord{}, err
	}
	project.PremiumReceived, err = addMeasured(current.spec.PremiumReceived, incremental.spec.PremiumReceived)
	if err != nil {
		return ExposureRecord{}, err
	}
	if current.spec.LossBound == LossBoundKnown && incremental.spec.LossBound == LossBoundKnown {
		project.MaximumLoss, err = addMeasured(current.spec.MaximumLoss, incremental.spec.MaximumLoss)
		if err != nil {
			return ExposureRecord{}, err
		}
		project.LossBound = LossBoundKnown
	} else {
		project.MaximumLoss, _ = NewUnavailableMoney(AvailabilityUnknown)
		if current.spec.LossBound == LossBoundUnbounded || incremental.spec.LossBound == LossBoundUnbounded {
			project.LossBound = LossBoundUnbounded
		} else {
			project.LossBound = LossBoundUnknown
		}
	}
	project.Overnight = current.spec.Overnight || incremental.spec.Overnight
	return NewExposureRecord(project)
}

func addMeasured(left, right MeasuredMoney) (MeasuredMoney, error) {
	if left.availability != AvailabilityKnown || right.availability != AvailabilityKnown {
		state := AvailabilityUnknown
		if left.availability == AvailabilityUnavailable || right.availability == AvailabilityUnavailable {
			state = AvailabilityUnavailable
		}
		return NewUnavailableMoney(state)
	}
	value, err := left.value.Add(right.value)
	if err != nil {
		if errors.Is(err, domain.ErrMoneyOverflow) {
			return MeasuredMoney{}, ErrArithmeticOverflow
		}
		return MeasuredMoney{}, err
	}
	return NewKnownMoney(value)
}

func normalizeExposures(values []ExposureRecord) ([]ExposureRecord, error) {
	if len(values) > MaximumExposureRecords {
		return nil, ErrInvalidExposure
	}
	result := append([]ExposureRecord(nil), values...)
	seen := make(map[string]struct{}, len(result))
	for _, value := range result {
		if _, err := NewExposureRecord(value.spec); err != nil {
			return nil, err
		}
		key := string(value.Dimension()) + "|" + value.Subject()
		if _, exists := seen[key]; exists {
			return nil, ErrInvalidExposure
		}
		seen[key] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool {
		left := string(result[i].Dimension()) + "|" + result[i].Subject()
		right := string(result[j].Dimension()) + "|" + result[j].Subject()
		return left < right
	})
	return result, nil
}
