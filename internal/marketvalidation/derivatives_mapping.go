package marketvalidation

import (
	"fmt"
	"sort"
	"strings"
	"time"

	brokerzerodha "github.com/bibhuyash/tradeedge/internal/adapters/broker/zerodha"
	"github.com/bibhuyash/tradeedge/internal/domain"
)

const DerivativeMappingPolicyVersion = "nifty-bounded-derivative-mapping/v1"

// GenerateNIFTYDerivativeMappings derives symbols from checksum-pinned provider
// metadata and an accepted forward reference. No provider token or ATM strike
// is hard-coded in strategy configuration.
func GenerateNIFTYDerivativeMappings(dumpRaw []byte, forwardReferenceMinor int64, asOf, validFrom, validUntil time.Time) (MappingGeneration, MappingSelection, error) {
	if forwardReferenceMinor <= 0 || asOf.IsZero() {
		return MappingGeneration{}, MappingSelection{}, ErrInvalidMappingSelection
	}
	records, err := brokerzerodha.ParseInstrumentDump(dumpRaw)
	if err != nil {
		return MappingGeneration{}, MappingSelection{}, err
	}
	minDate, maxDate := date(asOf.AddDate(0, 0, 1)), date(asOf.AddDate(0, 0, 14))
	var futures []brokerzerodha.InstrumentRecord
	expiries := map[string]bool{}
	for _, r := range records {
		if !isNIFTYDerivativeSymbol(r.TradingSymbol) {
			continue
		}
		if r.Exchange == "NFO" && r.Segment == "NFO-FUT" && r.InstrumentType == "FUT" && r.Expiry >= minDate {
			futures = append(futures, r)
		}
		if r.Exchange == "NFO" && r.Segment == "NFO-OPT" && r.InstrumentType == "CE" && r.Expiry >= minDate && r.Expiry <= maxDate {
			expiries[r.Expiry] = true
		}
	}
	if len(futures) == 0 || len(expiries) == 0 {
		return MappingGeneration{}, MappingSelection{}, ErrInvalidMappingSelection
	}
	sort.Slice(futures, func(i, j int) bool { return futures[i].Expiry < futures[j].Expiry })
	if len(futures) > 1 && futures[0].Expiry == futures[1].Expiry {
		return MappingGeneration{}, MappingSelection{}, ErrInvalidMappingSelection
	}
	expiryKeys := make([]string, 0, len(expiries))
	for k := range expiries {
		expiryKeys = append(expiryKeys, k)
	}
	sort.Strings(expiryKeys)
	optionExpiry := expiryKeys[0]
	byStrike := map[int64][]brokerzerodha.InstrumentRecord{}
	var strikes []int64
	for _, r := range records {
		if r.Exchange != "NFO" || r.Segment != "NFO-OPT" || r.InstrumentType != "CE" || r.Expiry != optionExpiry || !isNIFTYDerivativeSymbol(r.TradingSymbol) {
			continue
		}
		strike, parseErr := decimalMinor(r.Strike)
		if parseErr != nil {
			continue
		}
		if _, ok := byStrike[strike]; !ok {
			strikes = append(strikes, strike)
		}
		byStrike[strike] = append(byStrike[strike], r)
	}
	sort.Slice(strikes, func(i, j int) bool { return strikes[i] < strikes[j] })
	if len(strikes) < 5 {
		return MappingGeneration{}, MappingSelection{}, ErrInvalidMappingSelection
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
		return MappingGeneration{}, MappingSelection{}, ErrInvalidMappingSelection
	}
	interval := strikes[center] - strikes[center-1]
	for i := center - 1; i <= center+2; i++ {
		if strikes[i]-strikes[i-1] != interval {
			return MappingGeneration{}, MappingSelection{}, ErrInvalidMappingSelection
		}
	}
	selection := MappingSelection{SchemaVersion: MappingSelectionSchemaVersion, WatchlistID: "phase8-m2-nifty-derivatives/v1", Instruments: []MappingSelectionItem{{Key: "NSE:INDEX:NIFTY", ProviderExchange: "NSE", ProviderSegment: "INDICES", ProviderInstrumentType: "EQ", TradingSymbol: "NIFTY 50", CanonicalSegment: domain.SegmentIndex, Underlying: "NIFTY", Type: domain.InstrumentIndex}, {Key: "NFO:FUTURE:NIFTY:" + futures[0].Expiry, ProviderExchange: "NFO", ProviderSegment: "NFO-FUT", ProviderInstrumentType: "FUT", TradingSymbol: futures[0].TradingSymbol, CanonicalSegment: domain.SegmentFutures, Underlying: "NIFTY", Type: domain.InstrumentFuture}}}
	for _, strike := range strikes[center-2 : center+3] {
		matches := byStrike[strike]
		if len(matches) != 1 {
			return MappingGeneration{}, MappingSelection{}, ErrInvalidMappingSelection
		}
		r := matches[0]
		selection.Instruments = append(selection.Instruments, MappingSelectionItem{Key: fmt.Sprintf("NFO:OPTION:NIFTY:%s:%d:CE", r.Expiry, strike), ProviderExchange: "NFO", ProviderSegment: "NFO-OPT", ProviderInstrumentType: "CE", TradingSymbol: r.TradingSymbol, CanonicalSegment: domain.SegmentOptions, Underlying: "NIFTY", Type: domain.InstrumentOption})
	}
	generated, err := GenerateMappings(dumpRaw, selection, asOf, validFrom, validUntil)
	return generated, selection, err
}
func date(at time.Time) string { y, m, d := at.Date(); return fmt.Sprintf("%04d-%02d-%02d", y, m, d) }
func isNIFTYDerivativeSymbol(value string) bool {
	return len(value) > 5 && strings.HasPrefix(value, "NIFTY") && value[5] >= '0' && value[5] <= '9'
}
