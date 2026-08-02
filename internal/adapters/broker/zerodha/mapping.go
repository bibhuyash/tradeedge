package zerodha

import (
	"errors"
	"time"

	brokertelemetry "github.com/bibhuyash/tradeedge/internal/broker/telemetry"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
)

const Provider domain.Provider = ProviderName

type ResolvedInstrument struct {
	InstrumentID  domain.InstrumentID
	Token         string
	TradingSymbol string
	MasterVersion string
}

type Mapper struct {
	master instrumentmaster.Master
	maxAge time.Duration
	clock  Clock
	record brokertelemetry.Recorder
}

func NewMapper(master instrumentmaster.Master, maxAge time.Duration, clock Clock, recorder brokertelemetry.Recorder) (*Mapper, error) {
	if master.Version() == "" || maxAge <= 0 {
		return nil, ErrInvalidConfiguration
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &Mapper{master: master, maxAge: maxAge, clock: clock, record: brokertelemetry.Safe(recorder)}, nil
}

func (mapper *Mapper) ResolveCanonical(id domain.InstrumentID, at time.Time) (ResolvedInstrument, error) {
	if err := mapper.validateGeneration(at); err != nil {
		mapper.mappingEvent(err)
		return ResolvedInstrument{}, err
	}
	instrument, found := mapper.master.Instrument(id)
	if !found {
		mapper.mappingEvent(ErrMappingMissing)
		return ResolvedInstrument{}, ErrMappingMissing
	}
	if derivativeExpired(instrument, at) {
		mapper.mappingEvent(ErrDerivativeExpired)
		return ResolvedInstrument{}, ErrDerivativeExpired
	}
	ref, err := mapper.master.ResolveInstrument(Provider, id, at)
	if err != nil {
		err = mappingError(err)
		mapper.mappingEvent(err)
		return ResolvedInstrument{}, err
	}
	mapper.mappingEvent(nil)
	return ResolvedInstrument{InstrumentID: id, Token: ref.Token, TradingSymbol: ref.TradingSymbol, MasterVersion: string(mapper.master.Version())}, nil
}

func (mapper *Mapper) ResolveToken(token string, at time.Time) (ResolvedInstrument, error) {
	if err := mapper.validateGeneration(at); err != nil {
		mapper.mappingEvent(err)
		return ResolvedInstrument{}, err
	}
	id, err := mapper.master.Resolve(Provider, token, at)
	if err != nil {
		err = mappingError(err)
		mapper.mappingEvent(err)
		return ResolvedInstrument{}, err
	}
	return mapper.ResolveCanonical(id, at)
}

func (mapper *Mapper) validateGeneration(at time.Time) error {
	if at.IsZero() || mapper.master.AsOf().After(at) || at.Sub(mapper.master.AsOf()) > mapper.maxAge {
		return ErrMappingStale
	}
	return nil
}

func (mapper *Mapper) mappingEvent(err error) {
	outcome := brokertelemetry.OutcomeSuccess
	switch {
	case errors.Is(err, ErrMappingMissing):
		outcome = brokertelemetry.OutcomeMissing
	case errors.Is(err, ErrMappingStale), errors.Is(err, ErrDerivativeExpired):
		outcome = brokertelemetry.OutcomeStale
	case errors.Is(err, ErrMappingAmbiguous):
		outcome = brokertelemetry.OutcomeAmbiguous
	}
	mapper.record.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationMapping, Outcome: outcome})
}

func mappingError(err error) error {
	if errors.Is(err, instrumentmaster.ErrAmbiguousMapping) || errors.Is(err, instrumentmaster.ErrInvalidMaster) {
		return ErrMappingAmbiguous
	}
	return ErrMappingMissing
}

func derivativeExpired(instrument domain.Instrument, at time.Time) bool {
	if instrument.Type() != domain.InstrumentFuture && instrument.Type() != domain.InstrumentOption {
		return false
	}
	expiry := instrument.Expiry()
	location, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		location = time.FixedZone("IST", 5*60*60+30*60)
	}
	local := at.In(location)
	return local.Year() > expiry.Year() ||
		(local.Year() == expiry.Year() && local.Month() > expiry.Month()) ||
		(local.Year() == expiry.Year() && local.Month() == expiry.Month() && local.Day() > expiry.Day())
}
