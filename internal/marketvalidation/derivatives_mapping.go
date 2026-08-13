package marketvalidation

import (
	"fmt"
	"sort"
	"strings"
	"time"

	brokerzerodha "github.com/bibhuyash/tradeedge/internal/adapters/broker/zerodha"
	"github.com/bibhuyash/tradeedge/internal/domain"
)

const DerivativeMappingPolicyVersion = "phase8-m4-bounded-derivative-mapping/v2"

func GenerateNIFTYDerivativeMappings(dumpRaw []byte, forwardReferenceMinor int64, asOf, validFrom, validUntil time.Time) (MappingGeneration, MappingSelection, error) {
	selection, err := selectDerivativeMappings(dumpRaw, "NIFTY", "NIFTY 50", forwardReferenceMinor, asOf)
	if err != nil {
		return MappingGeneration{}, MappingSelection{}, err
	}
	selection.WatchlistID = "phase8-m2-nifty-derivatives/v1"
	generated, err := GenerateMappings(dumpRaw, selection, asOf, validFrom, validUntil)
	return generated, selection, err
}

// GenerateShadowDerivativeMappings derives exactly two spot indices, two
// eligible futures, and two five-contract CE universes from provider metadata.
func GenerateShadowDerivativeMappings(dumpRaw []byte, niftyForwardMinor, bankNIFTYForwardMinor int64, asOf, validFrom, validUntil time.Time) (MappingGeneration, MappingSelection, error) {
	nifty, err := selectDerivativeMappings(dumpRaw, "NIFTY", "NIFTY 50", niftyForwardMinor, asOf)
	if err != nil {
		return MappingGeneration{}, MappingSelection{}, err
	}
	bank, err := selectDerivativeMappings(dumpRaw, "BANKNIFTY", "NIFTY BANK", bankNIFTYForwardMinor, asOf)
	if err != nil {
		return MappingGeneration{}, MappingSelection{}, err
	}
	selection := MappingSelection{SchemaVersion: MappingSelectionSchemaVersion, WatchlistID: "phase8-m4-live-shadow/v1", Instruments: append(nifty.Instruments, bank.Instruments...)}
	generated, err := GenerateMappings(dumpRaw, selection, asOf, validFrom, validUntil)
	return generated, selection, err
}

func selectDerivativeMappings(dumpRaw []byte, underlying, spotSymbol string, forwardReferenceMinor int64, asOf time.Time) (MappingSelection, error) {
	if forwardReferenceMinor <= 0 || asOf.IsZero() || (underlying != "NIFTY" && underlying != "BANKNIFTY") {
		return MappingSelection{}, ErrInvalidMappingSelection
	}
	records, err := brokerzerodha.ParseInstrumentDump(dumpRaw)
	if err != nil {
		return MappingSelection{}, err
	}
	minDate, maxDate := date(asOf.AddDate(0, 0, 1)), date(asOf.AddDate(0, 0, 14))
	var futures []brokerzerodha.InstrumentRecord
	expiries := map[string]bool{}
	for _, record := range records {
		if !isDerivativeSymbol(record.TradingSymbol, underlying) {
			continue
		}
		if record.Exchange == "NFO" && record.Segment == "NFO-FUT" && record.InstrumentType == "FUT" && record.Expiry >= minDate {
			futures = append(futures, record)
		}
		if record.Exchange == "NFO" && record.Segment == "NFO-OPT" && record.InstrumentType == "CE" && record.Expiry >= minDate && record.Expiry <= maxDate {
			expiries[record.Expiry] = true
		}
	}
	if len(futures) == 0 || len(expiries) == 0 {
		return MappingSelection{}, ErrInvalidMappingSelection
	}
	sort.Slice(futures, func(i, j int) bool { return futures[i].Expiry < futures[j].Expiry })
	if len(futures) > 1 && futures[0].Expiry == futures[1].Expiry {
		return MappingSelection{}, ErrInvalidMappingSelection
	}
	expiryKeys := make([]string, 0, len(expiries))
	for expiry := range expiries {
		expiryKeys = append(expiryKeys, expiry)
	}
	sort.Strings(expiryKeys)
	optionExpiry := expiryKeys[0]
	byStrike := map[int64][]brokerzerodha.InstrumentRecord{}
	var strikes []int64
	for _, record := range records {
		if record.Exchange != "NFO" || record.Segment != "NFO-OPT" || record.InstrumentType != "CE" || record.Expiry != optionExpiry || !isDerivativeSymbol(record.TradingSymbol, underlying) {
			continue
		}
		strike, parseErr := decimalMinor(record.Strike)
		if parseErr != nil {
			continue
		}
		if _, ok := byStrike[strike]; !ok {
			strikes = append(strikes, strike)
		}
		byStrike[strike] = append(byStrike[strike], record)
	}
	sort.Slice(strikes, func(i, j int) bool { return strikes[i] < strikes[j] })
	if len(strikes) < 5 {
		return MappingSelection{}, ErrInvalidMappingSelection
	}
	center := sort.Search(len(strikes), func(i int) bool { return strikes[i] >= forwardReferenceMinor })
	if center == len(strikes) {
		center--
	} else if center > 0 {
		lowerDistance, upperDistance := forwardReferenceMinor-strikes[center-1], strikes[center]-forwardReferenceMinor
		if lowerDistance < upperDistance {
			center--
		}
	}
	if center < 2 || center+2 >= len(strikes) {
		return MappingSelection{}, ErrInvalidMappingSelection
	}
	interval := strikes[center] - strikes[center-1]
	for index := center - 1; index <= center+2; index++ {
		if strikes[index]-strikes[index-1] != interval {
			return MappingSelection{}, ErrInvalidMappingSelection
		}
	}
	selection := MappingSelection{SchemaVersion: MappingSelectionSchemaVersion, Instruments: []MappingSelectionItem{
		{Key: "NSE:INDEX:" + underlying, ProviderExchange: "NSE", ProviderSegment: "INDICES", ProviderInstrumentType: "EQ", TradingSymbol: spotSymbol, CanonicalSegment: domain.SegmentIndex, Underlying: underlying, Type: domain.InstrumentIndex},
		{Key: "NFO:FUTURE:" + underlying + ":" + futures[0].Expiry, ProviderExchange: "NFO", ProviderSegment: "NFO-FUT", ProviderInstrumentType: "FUT", TradingSymbol: futures[0].TradingSymbol, CanonicalSegment: domain.SegmentFutures, Underlying: underlying, Type: domain.InstrumentFuture},
	}}
	for _, strike := range strikes[center-2 : center+3] {
		matches := byStrike[strike]
		if len(matches) != 1 {
			return MappingSelection{}, ErrInvalidMappingSelection
		}
		record := matches[0]
		selection.Instruments = append(selection.Instruments, MappingSelectionItem{Key: fmt.Sprintf("NFO:OPTION:%s:%s:%d:CE", underlying, record.Expiry, strike), ProviderExchange: "NFO", ProviderSegment: "NFO-OPT", ProviderInstrumentType: "CE", TradingSymbol: record.TradingSymbol, CanonicalSegment: domain.SegmentOptions, Underlying: underlying, Type: domain.InstrumentOption})
	}
	return selection, nil
}

func date(at time.Time) string {
	year, month, day := at.Date()
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

func isDerivativeSymbol(value, underlying string) bool {
	return len(value) > len(underlying) && strings.HasPrefix(value, underlying) && value[len(underlying)] >= '0' && value[len(underlying)] <= '9'
}
