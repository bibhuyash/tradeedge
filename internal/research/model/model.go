// Package model contains provider-neutral, immutable research data contracts.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

var (
	ErrInvalidDataset     = errors.New("invalid research dataset")
	ErrInvalidInstrument  = errors.New("invalid research instrument")
	ErrInvalidObservation = errors.New("invalid research observation")
	ErrDuplicateEvent     = errors.New("duplicate research observation")
	ErrArithmeticOverflow = errors.New("research arithmetic overflow")
)

const DatasetSchemaV1 = "tradeedge.research.dataset/v1"

type OptionType string

const (
	OptionNone OptionType = "NONE"
	OptionCE   OptionType = "CE"
	OptionPE   OptionType = "PE"
)

type ObservationID [sha256.Size]byte

func (id ObservationID) String() string { return hex.EncodeToString(id[:]) }
func (id ObservationID) IsZero() bool   { return id == ObservationID{} }

type InstrumentSpec struct {
	Instrument    domain.Instrument
	AvailableFrom time.Time
}

// ResearchInstrument binds normalized metadata to the first time at which it
// was knowable. Its fields are intentionally private.
type ResearchInstrument struct {
	instrument    domain.Instrument
	availableFrom time.Time
}

func NewResearchInstrument(spec InstrumentSpec) (ResearchInstrument, error) {
	if spec.Instrument.IsZero() || spec.AvailableFrom.IsZero() {
		return ResearchInstrument{}, ErrInvalidInstrument
	}
	return ResearchInstrument{instrument: spec.Instrument, availableFrom: spec.AvailableFrom.UTC()}, nil
}

func (i ResearchInstrument) Instrument() domain.Instrument { return i.instrument }
func (i ResearchInstrument) ID() domain.InstrumentID       { return i.instrument.ID() }
func (i ResearchInstrument) AvailableFrom() time.Time      { return i.availableFrom }
func (i ResearchInstrument) AvailableAt(at time.Time) bool { return !at.UTC().Before(i.availableFrom) }
func (i ResearchInstrument) OptionType() OptionType {
	switch i.instrument.OptionType() {
	case domain.OptionCall:
		return OptionCE
	case domain.OptionPut:
		return OptionPE
	default:
		return OptionNone
	}
}

type ObservationSpec struct {
	InstrumentID domain.InstrumentID
	ExchangeTime time.Time
	Price        domain.Price
	Bid          *domain.Price
	Ask          *domain.Price
	Volume       int64
	OpenInterest *int64
}

type Observation struct {
	id           ObservationID
	instrumentID domain.InstrumentID
	exchangeTime time.Time
	price        domain.Price
	bid          *domain.Price
	ask          *domain.Price
	volume       int64
	openInterest *int64
}

func NewObservation(spec ObservationSpec) (Observation, error) {
	if spec.InstrumentID.IsZero() || spec.ExchangeTime.IsZero() || spec.Price.IsZeroValue() ||
		spec.Price.MinorUnits() < 0 || spec.Volume < 0 || (spec.OpenInterest != nil && *spec.OpenInterest < 0) {
		return Observation{}, ErrInvalidObservation
	}
	for _, value := range []*domain.Price{spec.Bid, spec.Ask} {
		if value != nil && (value.IsZeroValue() || value.MinorUnits() < 0 || value.Currency() != spec.Price.Currency()) {
			return Observation{}, ErrInvalidObservation
		}
	}
	if spec.Bid != nil && spec.Ask != nil && spec.Bid.MinorUnits() > spec.Ask.MinorUnits() {
		return Observation{}, ErrInvalidObservation
	}
	at := spec.ExchangeTime.UTC()
	key := fmt.Sprintf("research-observation/v1|%s|%s|%d|%s|%s|%d|%s",
		spec.InstrumentID, at.Format(time.RFC3339Nano), spec.Price.MinorUnits(), priceKey(spec.Bid),
		priceKey(spec.Ask), spec.Volume, optionalIntKey(spec.OpenInterest))
	id := sha256.Sum256([]byte(key))
	return Observation{id: id, instrumentID: spec.InstrumentID, exchangeTime: at, price: spec.Price,
		bid: clonePrice(spec.Bid), ask: clonePrice(spec.Ask), volume: spec.Volume,
		openInterest: cloneInt64(spec.OpenInterest)}, nil
}

func (o Observation) ID() ObservationID                 { return o.id }
func (o Observation) InstrumentID() domain.InstrumentID { return o.instrumentID }
func (o Observation) ExchangeTime() time.Time           { return o.exchangeTime }
func (o Observation) Price() domain.Price               { return o.price }
func (o Observation) Bid() *domain.Price                { return clonePrice(o.bid) }
func (o Observation) Ask() *domain.Price                { return clonePrice(o.ask) }
func (o Observation) Volume() int64                     { return o.volume }
func (o Observation) OpenInterest() *int64              { return cloneInt64(o.openInterest) }

func ObservationLess(a, b Observation) bool {
	if !a.exchangeTime.Equal(b.exchangeTime) {
		return a.exchangeTime.Before(b.exchangeTime)
	}
	return a.id.String() < b.id.String()
}

type DatasetSpec struct {
	SchemaVersion string
	Version       string
	Instruments   []ResearchInstrument
	Observations  []Observation
}

type Dataset struct {
	schemaVersion string
	version       string
	instruments   []ResearchInstrument
	observations  []Observation
}

func NewDataset(spec DatasetSpec) (Dataset, error) {
	if strings.TrimSpace(spec.SchemaVersion) != DatasetSchemaV1 || strings.TrimSpace(spec.Version) == "" ||
		len(spec.Instruments) == 0 || len(spec.Observations) == 0 {
		return Dataset{}, ErrInvalidDataset
	}
	instruments := append([]ResearchInstrument(nil), spec.Instruments...)
	sort.Slice(instruments, func(a, b int) bool { return instruments[a].ID().String() < instruments[b].ID().String() })
	byID := make(map[domain.InstrumentID]ResearchInstrument, len(instruments))
	for _, instrument := range instruments {
		if instrument.ID().IsZero() {
			return Dataset{}, ErrInvalidDataset
		}
		if _, exists := byID[instrument.ID()]; exists {
			return Dataset{}, ErrInvalidDataset
		}
		byID[instrument.ID()] = instrument
	}
	observations := append([]Observation(nil), spec.Observations...)
	sort.Slice(observations, func(a, b int) bool { return ObservationLess(observations[a], observations[b]) })
	seen := make(map[ObservationID]struct{}, len(observations))
	for _, observation := range observations {
		instrument, exists := byID[observation.InstrumentID()]
		if !exists || !instrument.AvailableAt(observation.ExchangeTime()) {
			return Dataset{}, ErrInvalidDataset
		}
		if _, exists := seen[observation.ID()]; exists {
			return Dataset{}, ErrDuplicateEvent
		}
		seen[observation.ID()] = struct{}{}
	}
	return Dataset{schemaVersion: DatasetSchemaV1, version: strings.TrimSpace(spec.Version), instruments: instruments, observations: observations}, nil
}

func (d Dataset) SchemaVersion() string { return d.schemaVersion }
func (d Dataset) Version() string       { return d.version }
func (d Dataset) Instruments() []ResearchInstrument {
	return append([]ResearchInstrument(nil), d.instruments...)
}
func (d Dataset) Observations() []Observation { return append([]Observation(nil), d.observations...) }

func (d Dataset) InstrumentAt(id domain.InstrumentID, at time.Time) (ResearchInstrument, bool) {
	for _, instrument := range d.instruments {
		if instrument.ID() == id && instrument.AvailableAt(at) {
			return instrument, true
		}
	}
	return ResearchInstrument{}, false
}

func priceKey(value *domain.Price) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%d", value.MinorUnits())
}
func optionalIntKey(value *int64) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *value)
}
func clonePrice(value *domain.Price) *domain.Price {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
