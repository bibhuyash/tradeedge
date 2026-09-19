package features

import (
	"errors"
	"sort"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/research/model"
)

var ErrInvalidFrame = errors.New("invalid research feature frame")

type Unit string

const (
	UnitMinorUnits Unit = "MINOR_UNITS"
	UnitPPM        Unit = "PPM"
)
const (
	FeatureLastPrice = "LAST_PRICE"
	FeatureReturn    = "RETURN"
)

type Value struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
	Scale int64  `json:"scale"`
	Unit  Unit   `json:"unit"`
}

// PointInTimeView is constructed by the engine and contains no observation or
// metadata newer than AsOf.
type PointInTimeView struct {
	asOf         time.Time
	instrument   model.ResearchInstrument
	observations []model.Observation
}

func NewPointInTimeView(asOf time.Time, instrument model.ResearchInstrument, observations []model.Observation) (PointInTimeView, error) {
	if asOf.IsZero() || !instrument.AvailableAt(asOf) {
		return PointInTimeView{}, ErrInvalidFrame
	}
	copied := append([]model.Observation(nil), observations...)
	for index, observation := range copied {
		if observation.InstrumentID() != instrument.ID() || observation.ExchangeTime().After(asOf) ||
			(index > 0 && !model.ObservationLess(copied[index-1], observation)) {
			return PointInTimeView{}, ErrInvalidFrame
		}
	}
	return PointInTimeView{asOf: asOf.UTC(), instrument: instrument, observations: copied}, nil
}
func (v PointInTimeView) AsOf() time.Time                      { return v.asOf }
func (v PointInTimeView) Instrument() model.ResearchInstrument { return v.instrument }
func (v PointInTimeView) Observations() []model.Observation {
	return append([]model.Observation(nil), v.observations...)
}

type FeatureFrame struct {
	asOf       time.Time
	instrument model.ResearchInstrument
	sources    []model.ObservationID
	values     []Value
}

func NewFeatureFrame(view PointInTimeView, values []Value) (FeatureFrame, error) {
	if view.asOf.IsZero() {
		return FeatureFrame{}, ErrInvalidFrame
	}
	values = append([]Value(nil), values...)
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	for i, value := range values {
		if value.Name == "" || value.Scale <= 0 || (i > 0 && values[i-1].Name == value.Name) {
			return FeatureFrame{}, ErrInvalidFrame
		}
	}
	sources := make([]model.ObservationID, len(view.observations))
	for i, o := range view.observations {
		sources[i] = o.ID()
	}
	return FeatureFrame{asOf: view.asOf, instrument: view.instrument, sources: sources, values: values}, nil
}
func (f FeatureFrame) AsOf() time.Time                      { return f.asOf }
func (f FeatureFrame) Instrument() model.ResearchInstrument { return f.instrument }
func (f FeatureFrame) SourceObservationIDs() []model.ObservationID {
	return append([]model.ObservationID(nil), f.sources...)
}
func (f FeatureFrame) Values() []Value { return append([]Value(nil), f.values...) }
func (f FeatureFrame) Value(name string) (Value, bool) {
	i := sort.Search(len(f.values), func(i int) bool { return f.values[i].Name >= name })
	if i < len(f.values) && f.values[i].Name == name {
		return f.values[i], true
	}
	return Value{}, false
}

type Engine struct{}

func (Engine) Build(view PointInTimeView) (FeatureFrame, error) {
	observations := view.Observations()
	if len(observations) == 0 {
		return NewFeatureFrame(view, nil)
	}
	last := observations[len(observations)-1]
	values := []Value{{Name: FeatureLastPrice, Value: last.Price().MinorUnits(), Scale: 1, Unit: UnitMinorUnits}}
	if len(observations) > 1 {
		previous := observations[len(observations)-2].Price().MinorUnits()
		if previous > 0 {
			delta, err := model.CheckedSub(last.Price().MinorUnits(), previous)
			if err != nil {
				return FeatureFrame{}, err
			}
			ppm, err := model.CheckedMul(delta, 1_000_000)
			if err != nil {
				return FeatureFrame{}, err
			}
			values = append(values, Value{Name: FeatureReturn, Value: ppm / previous, Scale: 1_000_000, Unit: UnitPPM})
		}
	}
	return NewFeatureFrame(view, values)
}

// Ensure this package remains tied to canonical instrument identity only.
var _ domain.InstrumentID
