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
	"strings"

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
	if err := set.Parse(args); err != nil || set.NArg() != 0 || strings.TrimSpace(*datasetPath) == "" || strings.TrimSpace(*configPath) == "" {
		return errors.New("usage: tradeedge-research -dataset <json> -config <json>")
	}
	datasetRaw, err := os.ReadFile(*datasetPath)
	if err != nil {
		return fmt.Errorf("read dataset: %w", err)
	}
	dataset, err := model.DecodeDataset(datasetRaw)
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
	engine, err := backtest.NewEngine(backtest.NewDatasetSource(dataset), features.Engine{}, strategy, fillModel, costModel, backtest.Config{PolicyVersion: config.EnginePolicy, StartingCapital: capital, Quantity: quantity})
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
		if set.Parse(args[1:]) != nil || set.NArg() != 0 || *input == "" || *instruments == "" || *calendarPath == "" || *output == "" || *source == "" {
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
		if set.Parse(args[1:]) != nil || set.NArg() != 0 || *path == "" {
			return errors.New("usage: tradeedge-research dataset " + args[0] + " --dataset <json>")
		}
		artifact, err := dataset.Load(*path)
		if err != nil {
			return fmt.Errorf("load dataset: %w", err)
		}
		if err = dataset.ValidateArtifact(artifact); err != nil {
			return fmt.Errorf("validate dataset: %w", err)
		}
		return printDatasetSummary(stdout, artifact)
	default:
		return errors.New("usage: tradeedge-research dataset <import|validate|inspect>")
	}
}

func printDatasetSummary(w io.Writer, d dataset.CanonicalDataset) error {
	_, err := fmt.Fprintf(w, "DATASET_VERSION=%s\nOBSERVATIONS=%d\nTRADING_DAYS=%d\nQUALITY=%s\n", d.Manifest.DatasetVersion, len(d.Observations), d.Quality.TradingDaysPresent, d.Quality.QualificationState)
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
