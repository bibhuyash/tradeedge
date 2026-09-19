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
