package model

import (
	"errors"
	"math"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

var (
	ErrArithmeticOverflow = errors.New("portfolio arithmetic overflow")
	ErrInvalidRatio       = errors.New("invalid portfolio ratio")
)

type BasisPoints int32

func NewBasisPoints(value int32) (BasisPoints, error) {
	if value < 0 || value > 10000 {
		return 0, ErrInvalidRatio
	}
	return BasisPoints(value), nil
}

func (value BasisPoints) Int32() int32 { return int32(value) }

type ContractRatio uint32

func NewContractRatio(value uint32) (ContractRatio, error) {
	if value == 0 {
		return 0, ErrInvalidRatio
	}
	return ContractRatio(value), nil
}

func CheckedMoneyAdd(left, right domain.Money) (domain.Money, error) {
	return left.Add(right)
}

func CheckedMoneySubtract(left, right domain.Money) (domain.Money, error) {
	if right.MinorUnits() == math.MinInt64 {
		return domain.Money{}, ErrArithmeticOverflow
	}
	negated, err := domain.NewMoney(-right.MinorUnits(), right.Currency().String())
	if err != nil {
		return domain.Money{}, err
	}
	value, err := left.Add(negated)
	if errors.Is(err, domain.ErrMoneyOverflow) {
		return domain.Money{}, ErrArithmeticOverflow
	}
	return value, err
}

func CheckedMoneyMultiply(value domain.Money, factor int64) (domain.Money, error) {
	if value.IsZeroValue() {
		return domain.Money{}, domain.ErrInvalidCurrency
	}
	product, ok := checkedMultiply(value.MinorUnits(), factor)
	if !ok {
		return domain.Money{}, ErrArithmeticOverflow
	}
	return domain.NewMoney(product, value.Currency().String())
}

func checkedMultiply(left, right int64) (int64, bool) {
	if left == 0 || right == 0 {
		return 0, true
	}
	if left == -1 && right == math.MinInt64 || right == -1 && left == math.MinInt64 {
		return 0, false
	}
	value := left * right
	return value, value/right == left
}

type Rational struct {
	numerator   uint64
	denominator uint64
}

func NewRational(numerator, denominator uint64) (Rational, error) {
	if denominator == 0 {
		return Rational{}, ErrInvalidRatio
	}
	divisor := gcd(numerator, denominator)
	return Rational{numerator: numerator / divisor, denominator: denominator / divisor}, nil
}

func (value Rational) Numerator() uint64   { return value.numerator }
func (value Rational) Denominator() uint64 { return value.denominator }
func (value Rational) IsZero() bool        { return value.denominator == 0 }

func gcd(left, right uint64) uint64 {
	for right != 0 {
		left, right = right, left%right
	}
	if left == 0 {
		return 1
	}
	return left
}
