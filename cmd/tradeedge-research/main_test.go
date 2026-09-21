package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/research/dataset"
)

func TestOfflineHarnessProducesCanonicalReport(t *testing.T) {
	underlying, _ := domain.NewUnderlyingID("TEST")
	lot, _ := domain.NewQuantity(1)
	tick, _ := domain.NewPrice(1, "INR")
	currency, _ := domain.NewCurrency("INR")
	instrument, err := domain.NewInstrument(domain.InstrumentSpec{Exchange: domain.ExchangeNSE, Segment: domain.SegmentCash, UnderlyingID: underlying, Type: domain.InstrumentEquity, ExchangeSymbol: "TEST", LotSize: lot, TickSize: tick, Currency: currency})
	if err != nil {
		t.Fatal(err)
	}
	id := instrument.ID().String()
	dataset := fmt.Sprintf(`{"schema_version":"tradeedge.research.dataset/v1","dataset_version":"SYNTHETIC_DATASET/v1","instruments":[{"instrument_id":%q,"symbol":"TEST","underlying":"TEST","exchange":"NSE","segment":"CASH","instrument_type":"EQUITY","option_type":"","lot_size":1,"tick_size_minor":1,"currency":"INR","available_from":"2024-01-01T09:00:00Z"}],"observations":[%s]}`,
		id, observationsJSON(id))
	zero := `{"numerator":0,"denominator":1,"base":"TOTAL_NOTIONAL","rounding":"FLOOR"}`
	config := fmt.Sprintf(`{"schema_version":"tradeedge.research.run/v1","engine_policy":"tradeedge.research.engine/v1","fill_policy":"tradeedge.research.fill/v1","strategy":"CONTROL_STRATEGY_V1","direction":"BUY","starting_capital_minor":100000,"currency":"INR","quantity":1,"slippage_bps":0,"costs":{"schema_version":"tradeedge.research.cost/v1","version":"SYNTHETIC_COST/v1","brokerage":%s,"stt":%s,"exchange_charges":%s,"gst":%s,"stamp_duty":%s,"regulatory_charges":%s}}`, zero, zero, zero, zero, zero, zero)
	directory := t.TempDir()
	datasetPath := filepath.Join(directory, "dataset.json")
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(datasetPath, []byte(dataset), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	var first, second bytes.Buffer
	if err := run([]string{"-dataset", datasetPath, "-config", configPath}, &first); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-dataset", datasetPath, "-config", configPath}, &second); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("CLI replay is not byte deterministic")
	}
	if !strings.Contains(first.String(), `"dataset_version":"SYNTHETIC_DATASET/v1"`) || !strings.Contains(first.String(), `"trades":1`) {
		t.Fatalf("unexpected report: %s", first.String())
	}
}

func TestDatasetInspectSummaryIncludesM3AcceptanceFields(t *testing.T) {
	d := dataset.CanonicalDataset{Manifest: dataset.DatasetManifest{DatasetVersion: "id", Source: "vendor", RealData: true, Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Interval: "1m", ContractCount: 2, RawChecksum: "raw", NormalizedChecksum: "normalized"}, Instruments: []dataset.Instrument{{Underlying: "NIFTY"}, {Underlying: "BANKNIFTY"}}, Bars: []dataset.HistoricalBar{{}, {}, {}}, Quality: dataset.DatasetQualityReport{TradingDaysPresent: 2, MissingBars: 1, DuplicateEvents: 0, QualificationState: dataset.ResearchReady}}
	var out bytes.Buffer
	if err := printDatasetSummary(&out, d); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"DATASET_ID=id", "SOURCE=vendor", "REAL_DATA=true", "UNDERLYINGS=BANKNIFTY,NIFTY", "BAR_COUNT=3", "MISSING_BARS=1", "QUALITY_STATUS=RESEARCH_READY", "RAW_CHECKSUM=raw", "NORMALIZED_CHECKSUM=normalized"} {
		if !strings.Contains(out.String(), field) {
			t.Errorf("missing %s in %s", field, out.String())
		}
	}
}

func observationsJSON(id string) string {
	values := []int{100, 110, 120, 130}
	records := make([]string, len(values))
	for index, value := range values {
		records[index] = fmt.Sprintf(`{"instrument_id":%q,"exchange_timestamp":"2024-01-01T09:0%d:00Z","price_minor":%d,"bid_minor":%d,"ask_minor":%d,"volume":1}`, id, index+1, value, value-1, value+1)
	}
	return strings.Join(records, ",")
}
