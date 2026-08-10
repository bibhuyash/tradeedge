package marketvalidation

import (
	"strings"
	"testing"
	"time"
)

const mappingDumpHeader = "instrument_token,exchange_token,tradingsymbol,name,last_price,expiry,strike,tick_size,lot_size,instrument_type,segment,exchange\n"

func indexSelection() MappingSelection {
	return MappingSelection{SchemaVersion: MappingSelectionSchemaVersion, WatchlistID: "day0-index-observation/v1", Instruments: []MappingSelectionItem{{Key: "NSE:INDEX:NIFTY", ProviderExchange: "NSE", ProviderSegment: "INDICES", TradingSymbol: "NIFTY 50", CanonicalSegment: "INDEX", Underlying: "NIFTY", Type: "INDEX"}}}
}

func TestGenerateMappingsBuildsCurrentFailClosedArtifacts(t *testing.T) {
	dump := mappingDumpHeader + "256265,1001,NIFTY 50,NIFTY,0,,0,0.05,1,,INDICES,NSE\n"
	from := time.Date(2026, 8, 10, 3, 30, 0, 0, time.UTC)
	got, err := GenerateMappings([]byte(dump), indexSelection(), from, from, from.Add(12*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.MasterVersion == "" || got.WatchlistVersion == "" || got.SourceSHA256 == "" || !strings.Contains(string(got.InstrumentMaster), `"source_sha256"`) {
		t.Fatal("mapping identities were not generated")
	}
}

func TestGenerateMappingsRejectsDuplicateAndExpiredContracts(t *testing.T) {
	from := time.Date(2026, 8, 10, 3, 30, 0, 0, time.UTC)
	duplicate := mappingDumpHeader + "1,1,NIFTY 50,NIFTY,0,,0,0.05,1,,INDICES,NSE\n2,2,NIFTY 50,NIFTY,0,,0,0.05,1,,INDICES,NSE\n"
	if _, err := GenerateMappings([]byte(duplicate), indexSelection(), from, from, from.Add(time.Hour)); err == nil {
		t.Fatal("duplicate mapping accepted")
	}
	selection := MappingSelection{SchemaVersion: MappingSelectionSchemaVersion, WatchlistID: "expired/v1", Instruments: []MappingSelectionItem{{Key: "NSE:OPTIONS:NIFTY:20260809:2500000:CALL", ProviderExchange: "NFO", ProviderSegment: "NFO-OPT", ProviderInstrumentType: "CE", TradingSymbol: "NIFTY26AUG25000CE", CanonicalSegment: "OPTIONS", Underlying: "NIFTY", Type: "OPTION"}}}
	expired := mappingDumpHeader + "123,45,NIFTY26AUG25000CE,NIFTY,0,2026-08-09,25000,0.05,65,CE,NFO-OPT,NFO\n"
	if _, err := GenerateMappings([]byte(expired), selection, from, from, from.Add(time.Hour)); err == nil {
		t.Fatal("expired contract accepted")
	}
}

func TestGenerateMappingsRejectsWrongSegmentAndInvalidMetadata(t *testing.T) {
	from := time.Date(2026, 8, 10, 3, 30, 0, 0, time.UTC)
	selection := indexSelection()
	selection.Instruments[0].ProviderSegment = "NFO-OPT"
	if _, err := GenerateMappings([]byte(mappingDumpHeader), selection, from, from, from.Add(time.Hour)); err == nil {
		t.Fatal("incorrect provider segment accepted")
	}
	badMetadata := mappingDumpHeader + "1,1,NIFTY 50,NIFTY,0,,0,0,0,,INDICES,NSE\n"
	if _, err := GenerateMappings([]byte(badMetadata), indexSelection(), from, from, from.Add(time.Hour)); err == nil {
		t.Fatal("invalid lot/tick metadata accepted")
	}
}
