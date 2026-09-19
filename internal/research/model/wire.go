package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

type datasetWire struct {
	SchemaVersion string            `json:"schema_version"`
	Version       string            `json:"dataset_version"`
	Instruments   []instrumentWire  `json:"instruments"`
	Observations  []observationWire `json:"observations"`
}
type instrumentWire struct {
	InstrumentID   string                `json:"instrument_id"`
	Symbol         string                `json:"symbol"`
	Underlying     string                `json:"underlying"`
	Exchange       domain.Exchange       `json:"exchange"`
	Segment        domain.Segment        `json:"segment"`
	InstrumentType domain.InstrumentType `json:"instrument_type"`
	Expiry         string                `json:"expiry,omitempty"`
	StrikeMinor    *int64                `json:"strike_minor,omitempty"`
	OptionType     string                `json:"option_type,omitempty"`
	LotSize        int64                 `json:"lot_size"`
	TickSizeMinor  int64                 `json:"tick_size_minor"`
	Currency       string                `json:"currency"`
	AvailableFrom  time.Time             `json:"available_from"`
}
type observationWire struct {
	InstrumentID string    `json:"instrument_id"`
	ExchangeTime time.Time `json:"exchange_timestamp"`
	PriceMinor   int64     `json:"price_minor"`
	BidMinor     *int64    `json:"bid_minor,omitempty"`
	AskMinor     *int64    `json:"ask_minor,omitempty"`
	Volume       int64     `json:"volume"`
	OpenInterest *int64    `json:"open_interest,omitempty"`
}

func DecodeDataset(raw []byte) (Dataset, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire datasetWire
	if err := decoder.Decode(&wire); err != nil {
		return Dataset{}, ErrInvalidDataset
	}
	if err := ensureEOF(decoder); err != nil {
		return Dataset{}, ErrInvalidDataset
	}
	instruments := make([]ResearchInstrument, 0, len(wire.Instruments))
	ids := make(map[string]domain.InstrumentID, len(wire.Instruments))
	currencies := make(map[domain.InstrumentID]string, len(wire.Instruments))
	for _, item := range wire.Instruments {
		underlying, err := domain.NewUnderlyingID(item.Underlying)
		if err != nil {
			return Dataset{}, ErrInvalidInstrument
		}
		currency, err := domain.NewCurrency(item.Currency)
		if err != nil {
			return Dataset{}, ErrInvalidInstrument
		}
		lot, err := quantityAllowZero(item.LotSize, item.InstrumentType == domain.InstrumentIndex)
		if err != nil {
			return Dataset{}, ErrInvalidInstrument
		}
		tick, err := domain.NewPrice(item.TickSizeMinor, item.Currency)
		if err != nil {
			return Dataset{}, ErrInvalidInstrument
		}
		var derivative *domain.DerivativeSpec
		if item.InstrumentType == domain.InstrumentFuture || item.InstrumentType == domain.InstrumentOption {
			expiryTime, parseErr := time.Parse("2006-01-02", item.Expiry)
			if parseErr != nil {
				return Dataset{}, ErrInvalidInstrument
			}
			expiry, _ := domain.NewCivilDate(expiryTime.Year(), expiryTime.Month(), expiryTime.Day())
			strike := domain.Price{}
			if item.StrikeMinor != nil {
				strike, err = domain.NewPrice(*item.StrikeMinor, item.Currency)
				if err != nil {
					return Dataset{}, ErrInvalidInstrument
				}
			}
			optionType := domain.OptionNone
			if item.InstrumentType == domain.InstrumentOption {
				switch strings.ToUpper(strings.TrimSpace(item.OptionType)) {
				case string(OptionCE):
					optionType = domain.OptionCall
				case string(OptionPE):
					optionType = domain.OptionPut
				default:
					return Dataset{}, ErrInvalidInstrument
				}
			} else if strings.TrimSpace(item.OptionType) != "" && strings.ToUpper(strings.TrimSpace(item.OptionType)) != string(OptionNone) {
				return Dataset{}, ErrInvalidInstrument
			}
			derivative = &domain.DerivativeSpec{Expiry: expiry, Strike: strike, OptionType: optionType}
		}
		instrument, err := domain.NewInstrument(domain.InstrumentSpec{Exchange: item.Exchange, Segment: item.Segment,
			UnderlyingID: underlying, Type: item.InstrumentType, ExchangeSymbol: item.Symbol, Derivative: derivative,
			LotSize: lot, TickSize: tick, Currency: currency})
		if err != nil || (strings.TrimSpace(item.InstrumentID) != "" && item.InstrumentID != instrument.ID().String()) {
			return Dataset{}, ErrInvalidInstrument
		}
		researchInstrument, err := NewResearchInstrument(InstrumentSpec{Instrument: instrument, AvailableFrom: item.AvailableFrom})
		if err != nil {
			return Dataset{}, err
		}
		instruments = append(instruments, researchInstrument)
		ids[instrument.ID().String()] = instrument.ID()
		currencies[instrument.ID()] = item.Currency
	}
	observations := make([]Observation, 0, len(wire.Observations))
	for _, item := range wire.Observations {
		id, ok := ids[item.InstrumentID]
		if !ok {
			return Dataset{}, ErrInvalidObservation
		}
		price, err := domain.NewPrice(item.PriceMinor, currencies[id])
		if err != nil {
			return Dataset{}, ErrInvalidObservation
		}
		bid, err := optionalPrice(item.BidMinor, currencies[id])
		if err != nil {
			return Dataset{}, err
		}
		ask, err := optionalPrice(item.AskMinor, currencies[id])
		if err != nil {
			return Dataset{}, err
		}
		observation, err := NewObservation(ObservationSpec{InstrumentID: id, ExchangeTime: item.ExchangeTime, Price: price,
			Bid: bid, Ask: ask, Volume: item.Volume, OpenInterest: item.OpenInterest})
		if err != nil {
			return Dataset{}, err
		}
		observations = append(observations, observation)
	}
	return NewDataset(DatasetSpec{SchemaVersion: wire.SchemaVersion, Version: wire.Version, Instruments: instruments, Observations: observations})
}

func optionalPrice(value *int64, currency string) (*domain.Price, error) {
	if value == nil {
		return nil, nil
	}
	p, err := domain.NewPrice(*value, currency)
	if err != nil {
		return nil, ErrInvalidObservation
	}
	return &p, nil
}
func quantityAllowZero(value int64, index bool) (domain.Quantity, error) {
	if index && value == 0 {
		return 0, nil
	}
	return domain.NewQuantity(value)
}
func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalidDataset
	}
	return nil
}
