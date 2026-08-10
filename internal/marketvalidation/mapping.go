package marketvalidation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	brokerzerodha "github.com/bibhuyash/tradeedge/internal/adapters/broker/zerodha"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	marketreadiness "github.com/bibhuyash/tradeedge/internal/marketdata/readiness"
)

const MappingSelectionSchemaVersion = "market-validation-mapping-selection/v1"

var ErrInvalidMappingSelection = errors.New("invalid market-validation mapping selection")

type MappingSelection struct {
	SchemaVersion string                 `json:"schema_version"`
	WatchlistID   string                 `json:"watchlist_id"`
	Instruments   []MappingSelectionItem `json:"instruments"`
}

type MappingSelectionItem struct {
	Key                    string                `json:"key"`
	ProviderExchange       string                `json:"provider_exchange"`
	ProviderSegment        string                `json:"provider_segment"`
	ProviderInstrumentType string                `json:"provider_instrument_type"`
	TradingSymbol          string                `json:"trading_symbol"`
	CanonicalSegment       domain.Segment        `json:"canonical_segment"`
	Underlying             string                `json:"underlying"`
	Type                   domain.InstrumentType `json:"type"`
}

type MappingGeneration struct {
	InstrumentMaster []byte
	Watchlist        []byte
	MasterVersion    string
	WatchlistVersion string
	SourceSHA256     string
}

type generatedMaster struct {
	SchemaVersion int                   `json:"schema_version"`
	AsOf          time.Time             `json:"as_of"`
	SourceSHA256  string                `json:"source_sha256"`
	Instruments   []generatedInstrument `json:"instruments"`
	Mappings      []generatedMapping    `json:"mappings"`
}
type generatedInstrument struct {
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
type generatedMapping struct {
	Provider      domain.Provider `json:"provider"`
	Token         string          `json:"token"`
	TradingSymbol string          `json:"trading_symbol"`
	InstrumentKey string          `json:"instrument_key"`
	ValidFrom     time.Time       `json:"valid_from"`
	ValidUntil    time.Time       `json:"valid_until"`
}
type generatedWatchlist struct {
	SchemaVersion int                    `json:"schema_version"`
	ID            string                 `json:"id"`
	Requirements  []generatedRequirement `json:"requirements"`
}
type generatedRequirement struct {
	Provider      domain.Provider `json:"provider"`
	InstrumentKey string          `json:"instrument_key"`
	Exchange      domain.Exchange `json:"exchange"`
	Segment       domain.Segment  `json:"segment"`
	EventKind     string          `json:"event_kind"`
	Required      bool            `json:"required"`
}

func DecodeMappingSelection(raw []byte) (MappingSelection, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value MappingSelection
	if decoder.Decode(&value) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		value.SchemaVersion != MappingSelectionSchemaVersion || strings.TrimSpace(value.WatchlistID) == "" ||
		len(value.Instruments) < 1 || len(value.Instruments) > 4 {
		return MappingSelection{}, ErrInvalidMappingSelection
	}
	seenKeys, seenSymbols := map[string]bool{}, map[string]bool{}
	for index := range value.Instruments {
		item := &value.Instruments[index]
		item.Key, item.ProviderExchange = strings.TrimSpace(item.Key), strings.ToUpper(strings.TrimSpace(item.ProviderExchange))
		item.ProviderSegment, item.ProviderInstrumentType = strings.ToUpper(strings.TrimSpace(item.ProviderSegment)), strings.ToUpper(strings.TrimSpace(item.ProviderInstrumentType))
		item.TradingSymbol, item.Underlying = strings.ToUpper(strings.TrimSpace(item.TradingSymbol)), strings.ToUpper(strings.TrimSpace(item.Underlying))
		if item.Key == "" || item.TradingSymbol == "" || item.Underlying == "" || item.ProviderExchange == "" || item.ProviderSegment == "" ||
			seenKeys[item.Key] || seenSymbols[item.ProviderExchange+"|"+item.TradingSymbol] || !validCanonicalKind(*item) {
			return MappingSelection{}, ErrInvalidMappingSelection
		}
		seenKeys[item.Key], seenSymbols[item.ProviderExchange+"|"+item.TradingSymbol] = true, true
	}
	return value, nil
}

func validCanonicalKind(item MappingSelectionItem) bool {
	switch item.Type {
	case domain.InstrumentIndex:
		return item.CanonicalSegment == domain.SegmentIndex && item.ProviderExchange == "NSE" && item.ProviderSegment == "INDICES" && item.ProviderInstrumentType == "EQ"
	case domain.InstrumentEquity:
		return item.CanonicalSegment == domain.SegmentCash && item.ProviderExchange == "NSE" && item.ProviderSegment == "NSE" && item.ProviderInstrumentType == "EQ"
	case domain.InstrumentFuture:
		return item.CanonicalSegment == domain.SegmentFutures && item.ProviderExchange == "NFO" && item.ProviderSegment == "NFO-FUT" && item.ProviderInstrumentType == "FUT"
	case domain.InstrumentOption:
		return item.CanonicalSegment == domain.SegmentOptions && item.ProviderExchange == "NFO" && item.ProviderSegment == "NFO-OPT" && (item.ProviderInstrumentType == "CE" || item.ProviderInstrumentType == "PE")
	default:
		return false
	}
}

func GenerateMappings(dumpRaw []byte, selection MappingSelection, asOf, validFrom, validUntil time.Time) (MappingGeneration, error) {
	selectionRaw, _ := json.Marshal(selection)
	validatedSelection, selectionErr := DecodeMappingSelection(selectionRaw)
	if selectionErr != nil || asOf.IsZero() || validFrom.IsZero() ||
		!validUntil.After(validFrom) || validUntil.Sub(validFrom) > 24*time.Hour || asOf.After(validUntil) {
		return MappingGeneration{}, ErrInvalidMappingSelection
	}
	selection = validatedSelection
	records, err := brokerzerodha.ParseInstrumentDump(dumpRaw)
	if err != nil {
		return MappingGeneration{}, err
	}
	source := sha256.Sum256(dumpRaw)
	masterDoc := generatedMaster{SchemaVersion: 1, AsOf: asOf.UTC(), SourceSHA256: hex.EncodeToString(source[:])}
	watchlistDoc := generatedWatchlist{SchemaVersion: 1, ID: selection.WatchlistID}
	domainInstruments := make([]domain.Instrument, 0, len(selection.Instruments))
	domainMappings := make([]domain.ProviderInstrumentRef, 0, len(selection.Instruments))
	requirements := make([]marketreadiness.Requirement, 0, len(selection.Instruments))
	for _, selected := range selection.Instruments {
		matches := make([]brokerzerodha.InstrumentRecord, 0, 1)
		for _, record := range records {
			if strings.EqualFold(record.Exchange, selected.ProviderExchange) && strings.EqualFold(record.Segment, selected.ProviderSegment) &&
				strings.EqualFold(record.InstrumentType, selected.ProviderInstrumentType) && strings.EqualFold(record.TradingSymbol, selected.TradingSymbol) {
				matches = append(matches, record)
			}
		}
		if len(matches) != 1 {
			return MappingGeneration{}, fmt.Errorf("%w: %s matched %d records", ErrInvalidMappingSelection, selected.Key, len(matches))
		}
		record := matches[0]
		instrumentDoc, instrument, buildErr := buildGeneratedInstrument(selected, record, validFrom)
		if buildErr != nil {
			return MappingGeneration{}, buildErr
		}
		mapping := generatedMapping{Provider: brokerzerodha.Provider, Token: record.Token, TradingSymbol: record.TradingSymbol, InstrumentKey: selected.Key, ValidFrom: validFrom.UTC(), ValidUntil: validUntil.UTC()}
		masterDoc.Instruments, masterDoc.Mappings = append(masterDoc.Instruments, instrumentDoc), append(masterDoc.Mappings, mapping)
		watchlistDoc.Requirements = append(watchlistDoc.Requirements, generatedRequirement{Provider: brokerzerodha.Provider, InstrumentKey: selected.Key, Exchange: domain.ExchangeNSE, Segment: selected.CanonicalSegment, EventKind: "QUOTE", Required: true})
		domainInstruments = append(domainInstruments, instrument)
		domainMappings = append(domainMappings, domain.ProviderInstrumentRef{Provider: brokerzerodha.Provider, Token: record.Token, TradingSymbol: record.TradingSymbol, InstrumentID: instrument.ID(), ValidFrom: validFrom.UTC(), ValidUntil: validUntil.UTC()})
		requirements = append(requirements, marketreadiness.Requirement{Provider: brokerzerodha.Provider, InstrumentID: instrument.ID(), Exchange: domain.ExchangeNSE, Segment: selected.CanonicalSegment, EventKind: "QUOTE", Required: true})
	}
	master, err := instrumentmaster.New(asOf.UTC(), domainInstruments, domainMappings)
	if err != nil {
		return MappingGeneration{}, err
	}
	watchlist, err := marketreadiness.NewWatchlist(selection.WatchlistID, requirements)
	if err != nil {
		return MappingGeneration{}, err
	}
	masterRaw, _ := json.MarshalIndent(masterDoc, "", "  ")
	watchlistRaw, _ := json.MarshalIndent(watchlistDoc, "", "  ")
	return MappingGeneration{InstrumentMaster: append(masterRaw, '\n'), Watchlist: append(watchlistRaw, '\n'), MasterVersion: string(master.Version()), WatchlistVersion: watchlist.Version, SourceSHA256: masterDoc.SourceSHA256}, nil
}

func buildGeneratedInstrument(selected MappingSelectionItem, record brokerzerodha.InstrumentRecord, at time.Time) (generatedInstrument, domain.Instrument, error) {
	tick, err := decimalMinor(record.TickSize)
	observationOnlyIndex := selected.Type == domain.InstrumentIndex && tick == 0 && record.LotSize == 0
	if err != nil || (!observationOnlyIndex && (tick <= 0 || record.LotSize <= 0)) {
		return generatedInstrument{}, domain.Instrument{}, ErrInvalidMappingSelection
	}
	doc := generatedInstrument{Key: selected.Key, Exchange: domain.ExchangeNSE, Segment: selected.CanonicalSegment, Underlying: selected.Underlying, Type: selected.Type, Symbol: selected.TradingSymbol, LotSize: record.LotSize, TickSizeMinor: tick, Currency: "INR"}
	underlying, _ := domain.NewUnderlyingID(selected.Underlying)
	quantity, _ := domain.NewQuantity(record.LotSize)
	if observationOnlyIndex {
		quantity = 0
	}
	tickPrice, _ := domain.NewPrice(tick, "INR")
	spec := domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: selected.CanonicalSegment, UnderlyingID: underlying, Type: selected.Type, ExchangeSymbol: selected.TradingSymbol, LotSize: quantity, TickSize: tickPrice, Currency: "INR"}
	if selected.Type == domain.InstrumentFuture || selected.Type == domain.InstrumentOption {
		expiryTime, parseErr := time.Parse("2006-01-02", record.Expiry)
		if parseErr != nil {
			return generatedInstrument{}, domain.Instrument{}, ErrInvalidMappingSelection
		}
		expiry, _ := domain.NewCivilDate(expiryTime.Year(), expiryTime.Month(), expiryTime.Day())
		local := at.In(time.FixedZone("IST", 5*60*60+30*60))
		if expiry.String() < fmt.Sprintf("%04d-%02d-%02d", local.Year(), local.Month(), local.Day()) {
			return generatedInstrument{}, domain.Instrument{}, brokerzerodha.ErrDerivativeExpired
		}
		doc.Expiry = expiry.String()
		derivative := &domain.DerivativeSpec{Expiry: expiry, OptionType: domain.OptionNone}
		if selected.Type == domain.InstrumentOption {
			strike, strikeErr := decimalMinor(record.Strike)
			if strikeErr != nil || strike <= 0 {
				return generatedInstrument{}, domain.Instrument{}, ErrInvalidMappingSelection
			}
			option := domain.OptionType(strings.ToUpper(record.InstrumentType))
			if option == "CE" {
				option = domain.OptionCall
			} else if option == "PE" {
				option = domain.OptionPut
			}
			strikePrice, _ := domain.NewPrice(strike, "INR")
			derivative.Strike, derivative.OptionType = strikePrice, option
			doc.StrikeMinor, doc.OptionType = strike, option
		}
		spec.Derivative = derivative
	}
	instrument, err := domain.NewInstrument(spec)
	if err != nil {
		return generatedInstrument{}, domain.Instrument{}, ErrInvalidMappingSelection
	}
	return doc, instrument, nil
}

func decimalMinor(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "eE+") {
		return 0, ErrInvalidMappingSelection
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, ErrInvalidMappingSelection
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > 2 {
		return 0, ErrInvalidMappingSelection
	}
	for len(fraction) < 2 {
		fraction += "0"
	}
	major, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || major > (1<<63-1-99)/100 {
		return 0, ErrInvalidMappingSelection
	}
	minor := int64(0)
	if fraction != "" {
		minor, err = strconv.ParseInt(fraction, 10, 64)
	}
	if err != nil {
		return 0, ErrInvalidMappingSelection
	}
	return major*100 + minor, nil
}
