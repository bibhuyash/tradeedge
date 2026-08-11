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
func symbolRow(strike int, symbol string) string {
	return fmt.Sprintf("%d,3,%s,NIFTY,0,2026-08-18,%d,0.05,65,CE,NFO-OPT,NFO", strike, symbol, strike)
}
