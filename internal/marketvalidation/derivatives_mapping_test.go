package marketvalidation

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestGenerateNIFTYDerivativeMappingsResolvesBoundedUniverse(t *testing.T) {
	header := mappingDumpHeader
	rows := []string{"256265,1001,NIFTY 50,NIFTY,0,,0,0,0,EQ,INDICES,NSE", "14866434,2001,NIFTY26AUGFUT,NIFTY,0,2026-08-25,0,0.1,65,FUT,NFO-FUT,NFO"}
	for strike := 24700; strike <= 24900; strike += 50 {
		symbol := fmt.Sprintf("NIFTY26818%dCE", strike)
		rows = append(rows, symbolRow(strike, symbol))
	}
	dump := header + strings.Join(rows, "\n") + "\n"
	at := time.Date(2026, 8, 11, 4, 30, 0, 0, time.UTC)
	generated, selection, err := GenerateNIFTYDerivativeMappings([]byte(dump), 2481200, at, at, at.Add(8*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Instruments) != 7 || selection.Instruments[4].TradingSymbol != "NIFTY2681824800CE" || generated.SourceSHA256 == "" {
		t.Fatalf("bad resolved selection: %#v", selection)
	}
}

func TestGenerateNIFTYDerivativeMappingsFailsOnMissingOrAmbiguousUniverse(t *testing.T) {
	at := time.Date(2026, 8, 11, 4, 30, 0, 0, time.UTC)
	if _, _, err := GenerateNIFTYDerivativeMappings([]byte(mappingDumpHeader), 2481200, at, at, at.Add(time.Hour)); err == nil {
		t.Fatal("missing universe passed")
	}
}

func TestGenerateShadowDerivativeMappingsPartitionsBothBoundedUniverses(t *testing.T) {
	rows := []string{
		"256265,1001,NIFTY 50,NIFTY,0,,0,0,0,EQ,INDICES,NSE",
		"260105,1002,NIFTY BANK,BANKNIFTY,0,,0,0,0,EQ,INDICES,NSE",
		"14866434,2001,NIFTY26AUGFUT,NIFTY,0,2026-08-25,0,0.1,65,FUT,NFO-FUT,NFO",
		"14866435,2002,BANKNIFTY26AUGFUT,BANKNIFTY,0,2026-08-25,0,0.1,15,FUT,NFO-FUT,NFO",
	}
	for strike := 24700; strike <= 24900; strike += 50 {
		rows = append(rows, symbolRow(strike, fmt.Sprintf("NIFTY26818%dCE", strike)))
	}
	for strike := 51900; strike <= 52300; strike += 100 {
		rows = append(rows, fmt.Sprintf("%d,4,BANKNIFTY26818%dCE,BANKNIFTY,0,2026-08-18,%d,0.05,15,CE,NFO-OPT,NFO", strike, strike, strike))
	}
	dump := mappingDumpHeader + strings.Join(rows, "\n") + "\n"
	at := time.Date(2026, 8, 11, 4, 30, 0, 0, time.UTC)
	generated, selection, err := GenerateShadowDerivativeMappings([]byte(dump), 2481200, 5211200, at, at, at.Add(8*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Instruments) != 14 || selection.Instruments[0].Underlying != "NIFTY" || selection.Instruments[7].Underlying != "BANKNIFTY" || generated.MasterVersion == "" {
		t.Fatalf("wrong bounded universe: %#v", selection)
	}
	if _, _, err = GenerateShadowDerivativeMappings([]byte(dump+rows[3]+"\n"), 2481200, 5211200, at, at, at.Add(8*time.Hour)); err == nil {
		t.Fatal("ambiguous future passed")
	}
}
func symbolRow(strike int, symbol string) string {
	return fmt.Sprintf("%d,3,%s,NIFTY,0,2026-08-18,%d,0.05,65,CE,NFO-OPT,NFO", strike, symbol, strike)
}
