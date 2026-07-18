package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
)

var (
	ErrInvalidCurrency  = errors.New("invalid currency")
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrMoneyOverflow    = errors.New("money overflow")
	ErrInvalidQuantity  = errors.New("quantity must be positive")
	ErrInvalidPrice     = errors.New("price cannot be negative")
	ErrInvalidID        = errors.New("identifier cannot be empty")
)

type Currency string

func NewCurrency(value string) (Currency, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) != 3 {
		return "", ErrInvalidCurrency
	}
	for _, r := range value {
		if r > unicode.MaxASCII || !unicode.IsLetter(r) {
			return "", ErrInvalidCurrency
		}
	}
	return Currency(value), nil
}

func (c Currency) String() string { return string(c) }

type Money struct {
	minor    int64
	currency Currency
}

func NewMoney(minor int64, currency string) (Money, error) {
	parsed, err := NewCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	return Money{minor: minor, currency: parsed}, nil
}

func (m Money) MinorUnits() int64  { return m.minor }
func (m Money) Currency() Currency { return m.currency }
func (m Money) IsZeroValue() bool  { return m.currency == "" }
func (m Money) String() string     { return fmt.Sprintf("%s %d minor-units", m.currency, m.minor) }
func (m Money) Add(other Money) (Money, error) {
	if m.currency == "" || other.currency == "" || m.currency != other.currency {
		return Money{}, ErrCurrencyMismatch
	}
	if (other.minor > 0 && m.minor > math.MaxInt64-other.minor) ||
		(other.minor < 0 && m.minor < math.MinInt64-other.minor) {
		return Money{}, ErrMoneyOverflow
	}
	return Money{minor: m.minor + other.minor, currency: m.currency}, nil
}

type Quantity int64

func NewQuantity(value int64) (Quantity, error) {
	if value <= 0 {
		return 0, ErrInvalidQuantity
	}
	return Quantity(value), nil
}

func (q Quantity) Int64() int64  { return int64(q) }
func (q Quantity) IsValid() bool { return q > 0 }

type Price struct {
	minor    int64
	currency Currency
}

func NewPrice(minor int64, currency string) (Price, error) {
	if minor < 0 {
		return Price{}, ErrInvalidPrice
	}
	parsed, err := NewCurrency(currency)
	if err != nil {
		return Price{}, err
	}
	return Price{minor: minor, currency: parsed}, nil
}

func (p Price) MinorUnits() int64  { return p.minor }
func (p Price) Currency() Currency { return p.currency }
func (p Price) IsZeroValue() bool  { return p.currency == "" }

type OrderID string
type StrategyID string
type AccountID string
type ClientRequestID string

func NewOrderID(value string) (OrderID, error) {
	value, err := validateID(value)
	return OrderID(value), err
}

func NewStrategyID(value string) (StrategyID, error) {
	value, err := validateID(value)
	return StrategyID(value), err
}

func NewAccountID(value string) (AccountID, error) {
	value, err := validateID(value)
	return AccountID(value), err
}

func NewClientRequestID(value string) (ClientRequestID, error) {
	value, err := validateID(value)
	return ClientRequestID(value), err
}

func validateID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrInvalidID
	}
	return value, nil
}
