// Command tradeedge-research performs deterministic offline research replay.
// It has no broker, credential, order-submission, or network capability.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/bibhuyash/tradeedge/internal/adapters/researchdata/csvbundle"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/research/backtest"
	"github.com/bibhuyash/tradeedge/internal/research/cost"
	"github.com/bibhuyash/tradeedge/internal/research/dataset"
	"github.com/bibhuyash/tradeedge/internal/research/features"
	"github.com/bibhuyash/tradeedge/internal/research/model"
	"github.com/bibhuyash/tradeedge/internal/research/report"
)

const configSchemaV1 = "tradeedge.research.run/v1"

type runConfig struct {
	SchemaVersion        string      `json:"schema_version"`
	EnginePolicy         string      `json:"engine_policy"`
	FillPolicy           string      `json:"fill_policy"`
	Strategy             string      `json:"strategy"`
	Direction            domain.Side `json:"direction"`
	StartingCapitalMinor int64       `json:"starting_capital_minor"`
	Currency             string      `json:"currency"`
	Quantity             int64       `json:"quantity"`
	SlippageBPS          int64       `json:"slippage_bps"`
	Costs                cost.Config `json:"costs"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] == "dataset" {
		return runDataset(args[1:], stdout)
	}
	set := flag.NewFlagSet("tradeedge-research", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	datasetPath := set.String("dataset", "", "versioned historical dataset JSON")
	configPath := set.String("config", "", "versioned research run configuration JSON")
	allowDegraded := set.Bool("allow-degraded", false, "explicitly permit a DEGRADED canonical dataset")
	if err := set.Parse(args); err != nil || set.NArg() != 0 || strings.TrimSpace(*datasetPath) == "" || strings.TrimSpace(*configPath) == "" {
		return errors.New("usage: tradeedge-research -dataset <json> -config <json>")
	}
	datasetRaw, err := os.ReadFile(*datasetPath)
	if err != nil {
		return fmt.Errorf("read dataset: %w", err)
	}
	var researchDataset model.Dataset
	if artifact, loadErr := dataset.Load(*datasetPath); loadErr == nil {
		report, validateErr := dataset.Revalidate(artifact)
		if validateErr != nil {
			return fmt.Errorf("validate canonical dataset: %w", validateErr)
		}
		artifact.Quality = report
		if *allowDegraded {
			researchDataset, err = artifact.HistoricalSourceAllowDegraded()
		} else {
			researchDataset, err = artifact.HistoricalSource()
		}
	} else {
		researchDataset, err = model.DecodeDataset(datasetRaw)
	}
	if err != nil {
		return fmt.Errorf("decode dataset: %w", err)
	}
	configRaw, err := os.ReadFile(*configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	config, err := decodeConfig(configRaw)
	if err != nil {
		return err
	}
	capital, err := domain.NewMoney(config.StartingCapitalMinor, config.Currency)
	if err != nil || capital.MinorUnits() < 0 {
		return errors.New("invalid starting capital")
	}
	quantity, err := domain.NewQuantity(config.Quantity)
	if err != nil {
		return err
	}
	strategy, err := backtest.NewControlStrategy(config.Direction)
	if err != nil {
		return err
	}
	fillModel, err := backtest.NewBaselineFillModel(config.FillPolicy, config.SlippageBPS)
	if err != nil {
		return err
	}
	costModel, err := cost.New(config.Costs)
	if err != nil {
		return err
	}
	engine, err := backtest.NewEngine(backtest.NewDatasetSource(researchDataset), features.Engine{}, strategy, fillModel, costModel, backtest.Config{PolicyVersion: config.EnginePolicy, StartingCapital: capital, Quantity: quantity})
	if err != nil {
		return err
	}
	result, err := engine.Run(context.Background())
	if err != nil {
		return err
	}
	raw, err := (report.Reporter{}).Report(result)
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(raw, '\n'))
	return err
}

func runDataset(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: tradeedge-research dataset <import|validate|inspect>")
	}
	switch args[0] {
	case "import":
		set := flag.NewFlagSet("dataset import", flag.ContinueOnError)
		set.SetOutput(io.Discard)
		input := set.String("input", "", "historical observations CSV")
		instruments := set.String("instrument-master", "", "point-in-time instrument CSV")
		calendarPath := set.String("calendar", "", "explicit calendar JSON")
		output := set.String("output", "", "canonical dataset JSON")
		source := set.String("source", "", "source name")
		sourceVersion := set.String("source-version", "", "source version")
		interval := set.Int("interval-minutes", 1, "expected interval")
		adapter := set.String("adapter", "", "historical source adapter")
		bundle := set.String("bundle", "", "mapped CSV bundle manifest")
		if set.Parse(args[1:]) != nil || set.NArg() != 0 {
			return errors.New("usage: tradeedge-research dataset import --input <csv> --instrument-master <csv> --calendar <json> --output <json> --source <name>")
		}
		if *adapter != "" || *bundle != "" {
			if *adapter != "mapped-csv" || *bundle == "" || *output == "" {
				return errors.New("usage: tradeedge-research dataset import --adapter mapped-csv --bundle <json> --output <json>")
			}
			artifact, err := dataset.ImportSource(csvbundle.New(*bundle))
			if err != nil {
				return fmt.Errorf("import mapped CSV bundle: %w", err)
			}
			if err = dataset.Save(*output, artifact); err != nil {
				return fmt.Errorf("save dataset: %w", err)
			}
			return printDatasetSummary(stdout, artifact)
		}
		if *input == "" || *instruments == "" || *calendarPath == "" || *output == "" || *source == "" {
			return errors.New("usage: tradeedge-research dataset import --input <csv> --instrument-master <csv> --calendar <json> --output <json> --source <name>")
		}
		cal, err := dataset.LoadCalendar(*calendarPath)
		if err != nil {
			return fmt.Errorf("load calendar: %w", err)
		}
		artifact, err := dataset.ImportCSV(*input, *instruments, cal, dataset.ImportConfig{Source: *source, SourceVersion: *sourceVersion, IntervalMinutes: *interval})
		if err != nil {
			return fmt.Errorf("import dataset: %w", err)
		}
		if err = dataset.Save(*output, artifact); err != nil {
			return fmt.Errorf("save dataset: %w", err)
		}
		return printDatasetSummary(stdout, artifact)
	case "validate", "inspect":
		set := flag.NewFlagSet("dataset "+args[0], flag.ContinueOnError)
		set.SetOutput(io.Discard)
		path := set.String("dataset", "", "canonical dataset JSON")
		qualityOutput := set.String("output", "", "machine-readable quality report JSON")
		if set.Parse(args[1:]) != nil || set.NArg() != 0 || *path == "" {
			return errors.New("usage: tradeedge-research dataset " + args[0] + " --dataset <json>")
		}
		artifact, err := dataset.Load(*path)
		if err != nil {
			return fmt.Errorf("load dataset: %w", err)
		}
		report, err := dataset.Revalidate(artifact)
		if err != nil {
			return fmt.Errorf("validate dataset: %w", err)
		}
		artifact.Quality = report
		if args[0] == "validate" && *qualityOutput != "" {
			raw, e := json.MarshalIndent(report, "", "  ")
			if e != nil {
				return e
			}
			raw = append(raw, '\n')
			f, e := os.OpenFile(*qualityOutput, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0444)
			if e != nil {
				return e
			}
			if _, e = f.Write(raw); e != nil {
				_ = f.Close()
				return e
			}
			if e = f.Close(); e != nil {
				return e
			}
		}
		return printDatasetSummary(stdout, artifact)
	default:
		return errors.New("usage: tradeedge-research dataset <import|validate|inspect>")
	}
}

func printDatasetSummary(w io.Writer, d dataset.CanonicalDataset) error {
	underlyingSet := map[string]bool{}
	for _, i := range d.Instruments {
		underlyingSet[i.Underlying] = true
	}
	underlyings := make([]string, 0, len(underlyingSet))
	for v := range underlyingSet {
		underlyings = append(underlyings, v)
	}
	sort.Strings(underlyings)
	status := d.Quality.QualificationState
	if status == dataset.Qualified {
		status = dataset.ResearchReady
	}
	rawChecksum := d.Manifest.RawChecksum
	if rawChecksum == "" {
		rawChecksum = "N/A"
	}
	normalized := d.Manifest.NormalizedChecksum
	if normalized == "" {
		normalized = d.Manifest.ContentChecksum
	}
	barCount := len(d.Bars)
	if barCount == 0 {
		barCount = len(d.Observations)
	}
	_, err := fmt.Fprintf(w, "DATASET_ID=%s\nSOURCE=%s\nREAL_DATA=%t\nUNDERLYINGS=%s\nSTART=%s\nEND=%s\nINTERVAL=%s\nTRADING_DAYS=%d\nBAR_COUNT=%d\nCONTRACT_COUNT=%d\nMISSING_BARS=%d\nDUPLICATES=%d\nQUALITY_STATUS=%s\nRAW_CHECKSUM=%s\nNORMALIZED_CHECKSUM=%s\n", d.Manifest.DatasetVersion, d.Manifest.Source, d.Manifest.RealData, strings.Join(underlyings, ","), d.Manifest.Start.UTC().Format("2006-01-02T15:04:05Z"), d.Manifest.End.UTC().Format("2006-01-02T15:04:05Z"), d.Manifest.Interval, d.Quality.TradingDaysPresent, barCount, d.Manifest.ContractCount, d.Quality.MissingBars, d.Quality.DuplicateEvents, status, rawChecksum, normalized)
	return err
}
func decodeConfig(raw []byte) (runConfig, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var config runConfig
	if err := decoder.Decode(&config); err != nil {
		return runConfig{}, errors.New("invalid research configuration")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return runConfig{}, errors.New("invalid research configuration")
	}
	if config.SchemaVersion != configSchemaV1 || config.EnginePolicy != backtest.EnginePolicyV1 || config.FillPolicy != backtest.FillPolicyV1 || config.Strategy != backtest.ControlStrategyV1 {
		return runConfig{}, errors.New("unsupported research configuration")
	}
	return config, nil
}
