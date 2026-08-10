package instrumentmaster

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

type fileInstrument struct {
	Key           string                `json:"key"`
	Exchange      domain.Exchange       `json:"exchange"`
	Segment       domain.Segment        `json:"segment"`
	Underlying    string                `json:"underlying"`
	Type          domain.InstrumentType `json:"type"`
	Symbol        string                `json:"symbol"`
	Expiry        string                `json:"expiry,omitempty"`
	StrikeMinor   int64                 `json:"strike_minor,omitempty"`
	OptionType    domain.OptionType     `json:"option_type,omitempty"`
	LotSize       int64                 `json:"lot_size"`
	TickSizeMinor int64                 `json:"tick_size_minor"`
	Currency      string                `json:"currency"`
}
type fileMapping struct {
	Provider      domain.Provider `json:"provider"`
	Token         string          `json:"token"`
	TradingSymbol string          `json:"trading_symbol"`
	InstrumentKey string          `json:"instrument_key"`
	ValidFrom     time.Time       `json:"valid_from"`
	ValidUntil    time.Time       `json:"valid_until"`
}
type fileMaster struct {
	SchemaVersion int              `json:"schema_version"`
	AsOf          time.Time        `json:"as_of"`
	SourceSHA256  string           `json:"source_sha256"`
	Instruments   []fileInstrument `json:"instruments"`
	Mappings      []fileMapping    `json:"mappings"`
}

func LoadFile(path string) (Master, map[string]domain.InstrumentID, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Master{}, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var encoded fileMaster
	if decoder.Decode(&encoded) != nil || decoder.Decode(&struct{}{}) != io.EOF || encoded.SchemaVersion != 1 || encoded.AsOf.IsZero() || encoded.SourceSHA256 == "" || len(encoded.Instruments) == 0 {
		return Master{}, nil, ErrInvalidMaster
	}
	instruments := make([]domain.Instrument, 0, len(encoded.Instruments))
	keys := make(map[string]domain.InstrumentID, len(encoded.Instruments))
	for _, item := range encoded.Instruments {
		key := strings.TrimSpace(item.Key)
		underlying, underlyingErr := domain.NewUnderlyingID(item.Underlying)
		quantity, quantityErr := domain.NewQuantity(item.LotSize)
		tick, tickErr := domain.NewPrice(item.TickSizeMinor, item.Currency)
		currency, currencyErr := domain.NewCurrency(item.Currency)
		if key == "" || underlyingErr != nil || quantityErr != nil || tickErr != nil || currencyErr != nil || keys[key] != (domain.InstrumentID{}) {
			return Master{}, nil, ErrInvalidMaster
		}
		spec := domain.InstrumentSpec{Exchange: item.Exchange, Segment: item.Segment, UnderlyingID: underlying, Type: item.Type, ExchangeSymbol: item.Symbol, LotSize: quantity, TickSize: tick, Currency: currency}
		if item.Type == domain.InstrumentFuture || item.Type == domain.InstrumentOption {
			parsed, dateErr := time.Parse("2006-01-02", item.Expiry)
			expiry, expiryErr := domain.NewCivilDate(parsed.Year(), parsed.Month(), parsed.Day())
			if dateErr != nil || expiryErr != nil {
				return Master{}, nil, ErrInvalidMaster
			}
			strike := domain.Price{}
			if item.Type == domain.InstrumentOption {
				strike, err = domain.NewPrice(item.StrikeMinor, item.Currency)
				if err != nil {
					return Master{}, nil, ErrInvalidMaster
				}
			}
			optionType := item.OptionType
			if item.Type == domain.InstrumentFuture {
				optionType = domain.OptionNone
			}
			spec.Derivative = &domain.DerivativeSpec{Expiry: expiry, Strike: strike, OptionType: optionType}
		}
		instrument, instrumentErr := domain.NewInstrument(spec)
		if instrumentErr != nil {
			return Master{}, nil, instrumentErr
		}
		instruments = append(instruments, instrument)
		keys[key] = instrument.ID()
	}
	mappings := make([]domain.ProviderInstrumentRef, 0, len(encoded.Mappings))
	for _, item := range encoded.Mappings {
		id := keys[item.InstrumentKey]
		if id.IsZero() {
			return Master{}, nil, ErrInvalidMaster
		}
		mappings = append(mappings, domain.ProviderInstrumentRef{Provider: item.Provider, Token: item.Token, TradingSymbol: item.TradingSymbol, InstrumentID: id, ValidFrom: item.ValidFrom, ValidUntil: item.ValidUntil})
	}
	master, err := New(encoded.AsOf, instruments, mappings)
	return master, keys, err
}
