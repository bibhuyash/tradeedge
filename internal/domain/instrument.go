package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidInstrument   = errors.New("invalid instrument")
	ErrInvalidInstrumentID = errors.New("invalid instrument ID")
	ErrInvalidCivilDate    = errors.New("invalid civil date")
)

type InstrumentID [sha256.Size]byte

func InstrumentIDFromCanonicalKey(key string) (InstrumentID, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return InstrumentID{}, ErrInvalidInstrumentID
	}
	return sha256.Sum256([]byte(key)), nil
}

func ParseInstrumentID(value string) (InstrumentID, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != sha256.Size {
		return InstrumentID{}, ErrInvalidInstrumentID
	}
	var id InstrumentID
	copy(id[:], decoded)
	return id, nil
}

func (id InstrumentID) String() string { return hex.EncodeToString(id[:]) }
func (id InstrumentID) IsZero() bool   { return id == InstrumentID{} }

type UnderlyingID string

func NewUnderlyingID(value string) (UnderlyingID, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return "", ErrInvalidID
	}
	return UnderlyingID(value), nil
}

type Exchange string

const ExchangeNSE Exchange = "NSE"

type Segment string

const (
	SegmentCash    Segment = "CASH"
	SegmentIndex   Segment = "INDEX"
	SegmentFutures Segment = "FUTURES"
	SegmentOptions Segment = "OPTIONS"
)

type InstrumentType string

const (
	InstrumentEquity InstrumentType = "EQUITY"
	InstrumentIndex  InstrumentType = "INDEX"
	InstrumentFuture InstrumentType = "FUTURE"
	InstrumentOption InstrumentType = "OPTION"
)

type OptionType string

const (
	OptionNone OptionType = "NONE"
	OptionCall OptionType = "CALL"
	OptionPut  OptionType = "PUT"
)

type CivilDate struct {
	year  int
	month time.Month
	day   int
}

func NewCivilDate(year int, month time.Month, day int) (CivilDate, error) {
	candidate := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if candidate.Year() != year || candidate.Month() != month || candidate.Day() != day {
		return CivilDate{}, ErrInvalidCivilDate
	}
	return CivilDate{year: year, month: month, day: day}, nil
}

func (d CivilDate) Year() int         { return d.year }
func (d CivilDate) Month() time.Month { return d.month }
func (d CivilDate) Day() int          { return d.day }
func (d CivilDate) IsZero() bool      { return d.year == 0 }
func (d CivilDate) String() string {
	if d.IsZero() {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", d.year, d.month, d.day)
}

type DerivativeSpec struct {
	Expiry     CivilDate
	Strike     Price
	OptionType OptionType
}

type InstrumentSpec struct {
	Exchange       Exchange
	Segment        Segment
	UnderlyingID   UnderlyingID
	Type           InstrumentType
	ExchangeSymbol string
	Derivative     *DerivativeSpec
	LotSize        Quantity
	TickSize       Price
	Currency       Currency
}

type Instrument struct {
	id             InstrumentID
	exchange       Exchange
	segment        Segment
	underlyingID   UnderlyingID
	instrumentType InstrumentType
	exchangeSymbol string
	expiry         CivilDate
	strike         Price
	optionType     OptionType
	lotSize        Quantity
	tickSize       Price
	currency       Currency
}

func NewInstrument(spec InstrumentSpec) (Instrument, error) {
	spec.ExchangeSymbol = strings.ToUpper(strings.TrimSpace(spec.ExchangeSymbol))
	if spec.Exchange != ExchangeNSE || spec.UnderlyingID == "" || spec.ExchangeSymbol == "" ||
		!spec.LotSize.IsValid() || spec.TickSize.IsZeroValue() || spec.Currency == "" ||
		spec.TickSize.Currency() != spec.Currency || spec.TickSize.MinorUnits() <= 0 {
		return Instrument{}, ErrInvalidInstrument
	}

	var expiry CivilDate
	var strike Price
	optionType := OptionNone
	switch spec.Type {
	case InstrumentEquity:
		if spec.Segment != SegmentCash || spec.Derivative != nil {
			return Instrument{}, ErrInvalidInstrument
		}
	case InstrumentIndex:
		if spec.Segment != SegmentIndex || spec.Derivative != nil {
			return Instrument{}, ErrInvalidInstrument
		}
	case InstrumentFuture:
		if spec.Segment != SegmentFutures || spec.Derivative == nil ||
			spec.Derivative.Expiry.IsZero() || spec.Derivative.OptionType != OptionNone ||
			!spec.Derivative.Strike.IsZeroValue() {
			return Instrument{}, ErrInvalidInstrument
		}
		expiry = spec.Derivative.Expiry
	case InstrumentOption:
		if spec.Segment != SegmentOptions || spec.Derivative == nil ||
			spec.Derivative.Expiry.IsZero() || spec.Derivative.Strike.IsZeroValue() ||
			spec.Derivative.Strike.Currency() != spec.Currency ||
			spec.Derivative.Strike.MinorUnits() <= 0 ||
			(spec.Derivative.OptionType != OptionCall && spec.Derivative.OptionType != OptionPut) {
			return Instrument{}, ErrInvalidInstrument
		}
		expiry = spec.Derivative.Expiry
		strike = spec.Derivative.Strike
		optionType = spec.Derivative.OptionType
	default:
		return Instrument{}, ErrInvalidInstrument
	}

	key := fmt.Sprintf("v1|%s|%s|%s|%s|%s|%s|%d|%s|%d|%d|%s",
		spec.Exchange, spec.Segment, spec.UnderlyingID, spec.Type, spec.ExchangeSymbol,
		expiry.String(), strike.MinorUnits(), optionType, spec.LotSize.Int64(),
		spec.TickSize.MinorUnits(), spec.Currency)
	id, _ := InstrumentIDFromCanonicalKey(key)
	return Instrument{
		id:             id,
		exchange:       spec.Exchange,
		segment:        spec.Segment,
		underlyingID:   spec.UnderlyingID,
		instrumentType: spec.Type,
		exchangeSymbol: spec.ExchangeSymbol,
		expiry:         expiry,
		strike:         strike,
		optionType:     optionType,
		lotSize:        spec.LotSize,
		tickSize:       spec.TickSize,
		currency:       spec.Currency,
	}, nil
}

func (i Instrument) ID() InstrumentID           { return i.id }
func (i Instrument) Exchange() Exchange         { return i.exchange }
func (i Instrument) Segment() Segment           { return i.segment }
func (i Instrument) UnderlyingID() UnderlyingID { return i.underlyingID }
func (i Instrument) Type() InstrumentType       { return i.instrumentType }
func (i Instrument) Symbol() string             { return i.exchangeSymbol }
func (i Instrument) Expiry() CivilDate          { return i.expiry }
func (i Instrument) Strike() Price              { return i.strike }
func (i Instrument) OptionType() OptionType     { return i.optionType }
func (i Instrument) LotSize() Quantity          { return i.lotSize }
func (i Instrument) TickSize() Price            { return i.tickSize }
func (i Instrument) Currency() Currency         { return i.currency }
func (i Instrument) IsZero() bool               { return i.id.IsZero() }

type Provider string

type ProviderInstrumentRef struct {
	Provider      Provider
	Token         string
	TradingSymbol string
	InstrumentID  InstrumentID
	ValidFrom     time.Time
	ValidUntil    time.Time
	MasterVersion string
}

func (r ProviderInstrumentRef) Validate() error {
	if strings.TrimSpace(string(r.Provider)) == "" || strings.TrimSpace(r.Token) == "" ||
		r.InstrumentID.IsZero() || r.ValidFrom.IsZero() || !r.ValidUntil.After(r.ValidFrom) ||
		strings.TrimSpace(r.TradingSymbol) == "" {
		return ErrInvalidInstrument
	}
	return nil
}

func (r ProviderInstrumentRef) ValidAt(at time.Time) bool {
	return !at.Before(r.ValidFrom) && at.Before(r.ValidUntil)
}
