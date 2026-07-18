package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bibhuyash/tradeedge/internal/adapters/marketdata/calendarfile"
	fileadapter "github.com/bibhuyash/tradeedge/internal/adapters/marketdata/file"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	"github.com/bibhuyash/tradeedge/internal/marketdata"
	"github.com/bibhuyash/tradeedge/internal/marketdata/ingest"
	"github.com/bibhuyash/tradeedge/internal/marketdata/loadtest"
	"github.com/bibhuyash/tradeedge/internal/marketdata/model"
	"github.com/bibhuyash/tradeedge/internal/marketdata/replay"
	"github.com/bibhuyash/tradeedge/internal/marketdata/storage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: tradeedge-marketdata <ingest|verify|replay|rebuild|publish|rollback|lineage|loadtest> [flags]")
	}
	switch args[0] {
	case "ingest":
		return runIngest(ctx, args[1:], output)
	case "verify":
		return runVerify(ctx, args[1:], output)
	case "replay":
		return runReplay(ctx, args[1:], output)
	case "rebuild":
		return runRebuild(ctx, args[1:], output)
	case "publish":
		return runPublication(ctx, args[1:], output, storage.PublicationPublish)
	case "rollback":
		return runPublication(ctx, args[1:], output, storage.PublicationRollback)
	case "lineage":
		return runLineage(ctx, args[1:], output)
	case "loadtest":
		return runLoadTest(ctx, args[1:], output)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runLoadTest(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("loadtest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	profile := flags.String("profile", "normal", "normal, burst, duplicate, late, malformed, slow-consumer, or soak")
	if err := flags.Parse(args); err != nil {
		return err
	}
	config, err := loadtest.DefaultConfig(loadtest.Profile(*profile))
	if err != nil {
		return err
	}
	report, err := loadtest.Run(ctx, config)
	if err != nil {
		return err
	}
	if encodeErr := json.NewEncoder(output).Encode(report); encodeErr != nil {
		return encodeErr
	}
	if !report.Passed {
		return errors.New("load profile failed its acceptance thresholds")
	}
	return nil
}

func runIngest(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("ingest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	inputPath := flags.String("input", "", "NDJSON or NDJSON.gz observation fixture")
	masterPath := flags.String("master", "", "instrument master JSON")
	calendarPath := flags.String("calendar", "", "versioned exchange calendar JSON")
	root := flags.String("root", "", "dataset repository directory")
	lateness := flags.Duration("allowed-lateness", 2*time.Second, "out-of-order watermark allowance")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *inputPath == "" || *masterPath == "" || *calendarPath == "" || *root == "" {
		return errors.New("ingest requires -input, -master, -calendar, and -root")
	}
	return buildDataset(ctx, output, buildOptions{
		inputPath: *inputPath, masterPath: *masterPath, calendarPath: *calendarPath,
		root: *root, lateness: *lateness,
	})
}

type buildOptions struct {
	inputPath, masterPath, calendarPath, root string
	lateness                                  time.Duration
	parent                                    storage.DatasetID
	reason, requestID, series                 string
}

func buildDataset(ctx context.Context, output io.Writer, options buildOptions) error {
	inputBytes, err := os.ReadFile(options.inputPath)
	if err != nil {
		return err
	}
	masterBytes, err := os.ReadFile(options.masterPath)
	if err != nil {
		return err
	}
	master, err := decodeMaster(masterBytes)
	if err != nil {
		return err
	}
	schedule, err := calendarfile.Load(options.calendarPath)
	if err != nil {
		return err
	}
	masterRepository := instrumentmaster.NewMemoryRepository()
	if err := masterRepository.Put(ctx, master); err != nil {
		return err
	}
	repository := fileadapter.Repository{Root: options.root}
	sourceDigest := sha256.Sum256(inputBytes)
	writer, err := repository.Create(ctx, storage.DraftManifest{
		ParentID:      options.parent,
		MasterVersion: string(master.Version()), InstrumentMaster: masterBytes,
		CalendarVersion: string(schedule.Version()),
		Source:          "file-fixture", OrderingVersion: "exchange-sequence-event-id/v1",
		SourceSHA256: fmt.Sprintf("%x", sourceDigest[:]), CreatedAt: time.Now().UTC(),
		CorrectionReason: options.reason, RequestID: options.requestID, Series: options.series,
	})
	if err != nil {
		return err
	}
	service := ingest.Service{
		Normalizer: ingest.Normalizer{
			Resolver: instrumentmaster.Resolver{Repository: masterRepository},
			Calendar: schedule,
		},
		AllowedLateness: options.lateness, BufferCapacity: 10000,
	}
	if err := service.Ingest(ctx, fileadapter.Source{Path: options.inputPath},
		marketdata.SourceQuery{Mode: marketdata.SourceHistorical}, writer); err != nil {
		_ = writer.Abort(context.Background())
		return err
	}
	manifest, err := writer.Commit(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(manifest)
}

func runRebuild(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("rebuild", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("input", "", "corrected NDJSON fixture")
	master := flags.String("master", "", "instrument master JSON")
	calendarPath := flags.String("calendar", "", "versioned exchange calendar JSON")
	root := flags.String("root", "", "dataset repository directory")
	parent := flags.String("parent", "", "verified parent dataset ID")
	reason := flags.String("reason", "", "correction reason")
	requestID := flags.String("request-id", "", "stable correction request ID")
	series := flags.String("series", "", "publication series")
	lateness := flags.Duration("allowed-lateness", 2*time.Second, "out-of-order watermark allowance")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" || *master == "" || *calendarPath == "" || *root == "" ||
		*parent == "" || *reason == "" || *requestID == "" || *series == "" {
		return errors.New("rebuild requires -input, -master, -calendar, -root, -parent, -reason, -request-id, and -series")
	}
	return buildDataset(ctx, output, buildOptions{
		inputPath: *input, masterPath: *master, calendarPath: *calendarPath, root: *root,
		parent: storage.DatasetID(*parent), reason: *reason, requestID: *requestID,
		series: *series, lateness: *lateness,
	})
}

func runPublication(
	ctx context.Context,
	args []string,
	output io.Writer,
	action storage.PublicationAction,
) error {
	name := strings.ToLower(string(action))
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "dataset repository directory")
	series := flags.String("series", "", "publication series")
	dataset := flags.String("dataset", "", "verified target dataset ID")
	expected := flags.String("expected-current", "", "expected current dataset ID; empty only for first publication")
	reason := flags.String("reason", "", "publication reason")
	requestID := flags.String("request-id", "", "stable publication request ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *series == "" || *dataset == "" || *reason == "" || *requestID == "" {
		return fmt.Errorf("%s requires -root, -series, -dataset, -reason, and -request-id", name)
	}
	if action == storage.PublicationRollback && *expected == "" {
		return errors.New("rollback requires -expected-current")
	}
	publication, err := (fileadapter.Repository{Root: *root}).Publish(ctx, storage.PublicationRequest{
		Series: *series, DatasetID: storage.DatasetID(*dataset),
		ExpectedCurrentID: storage.DatasetID(*expected), Action: action,
		Reason: *reason, RequestID: *requestID, PublishedAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(publication)
}

func runLineage(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("lineage", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "dataset repository directory")
	dataset := flags.String("dataset", "", "dataset ID")
	series := flags.String("series", "", "optional publication series")
	limit := flags.Int("limit", 100, "maximum lineage entries")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *dataset == "" || *limit <= 0 || *limit > 100 {
		return errors.New("lineage requires -root, -dataset, and a limit from 1 to 100")
	}
	repository := fileadapter.Repository{Root: *root}
	lineage, err := repository.Lineage(ctx, storage.DatasetID(*dataset), *limit)
	if err != nil {
		return err
	}
	publications := []storage.Publication{}
	if *series != "" {
		publications, err = repository.Publications(ctx, *series, *limit)
		if err != nil && !errors.Is(err, storage.ErrDatasetNotFound) {
			return err
		}
	}
	return json.NewEncoder(output).Encode(map[string]any{
		"lineage": lineage, "publications": publications,
	})
}

func runVerify(ctx context.Context, args []string, output io.Writer) error {
	root, id, err := repositoryFlags("verify", args)
	if err != nil {
		return err
	}
	reader, err := (fileadapter.Repository{Root: root}).Open(ctx, id)
	if err != nil {
		return err
	}
	defer reader.Close()
	if err := reader.Scan(ctx, storage.EventQuery{}, func(context.Context, model.Event) error {
		return nil
	}); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(reader.Manifest())
}

func runReplay(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("replay", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "dataset repository directory")
	dataset := flags.String("dataset", "", "dataset ID")
	speed := flags.String("speed", "max", "max, 1x, or an integer acceleration such as 10x")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *root == "" || *dataset == "" {
		return errors.New("replay requires -root and -dataset")
	}
	rate, err := parseRate(*speed)
	if err != nil {
		return err
	}
	reader, err := (fileadapter.Repository{Root: *root}).Open(ctx, storage.DatasetID(*dataset))
	if err != nil {
		return err
	}
	defer reader.Close()
	clock := replay.RealClock{}
	engine := replay.NewEngine(clock, nil)
	if err := engine.Replay(ctx, reader, replay.Request{Rate: rate},
		func(context.Context, model.Event) error { return nil }); err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(engine.Metrics())
}

func repositoryFlags(name string, args []string) (string, storage.DatasetID, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "dataset repository directory")
	dataset := flags.String("dataset", "", "dataset ID")
	if err := flags.Parse(args); err != nil {
		return "", "", err
	}
	if *root == "" || *dataset == "" {
		return "", "", fmt.Errorf("%s requires -root and -dataset", name)
	}
	return *root, storage.DatasetID(*dataset), nil
}

func parseRate(value string) (replay.Rate, error) {
	if value == "max" {
		return replay.MaximumRate(), nil
	}
	if !strings.HasSuffix(value, "x") {
		return replay.Rate{}, fmt.Errorf("invalid replay speed %q", value)
	}
	multiplier, err := strconv.ParseInt(strings.TrimSuffix(value, "x"), 10, 64)
	if err != nil || multiplier <= 0 {
		return replay.Rate{}, fmt.Errorf("invalid replay speed %q", value)
	}
	return replay.Rate{EventsTime: time.Duration(multiplier) * time.Second, WallTime: time.Second}, nil
}

type masterFile struct {
	AsOf        time.Time          `json:"as_of"`
	Instruments []masterInstrument `json:"instruments"`
	Mappings    []masterMapping    `json:"mappings"`
}

type masterInstrument struct {
	Key         string                `json:"key"`
	Exchange    domain.Exchange       `json:"exchange"`
	Segment     domain.Segment        `json:"segment"`
	Underlying  string                `json:"underlying"`
	Type        domain.InstrumentType `json:"type"`
	Symbol      string                `json:"symbol"`
	Expiry      string                `json:"expiry,omitempty"`
	StrikeMinor int64                 `json:"strike_minor,omitempty"`
	OptionType  domain.OptionType     `json:"option_type,omitempty"`
	LotSize     int64                 `json:"lot_size"`
	TickMinor   int64                 `json:"tick_minor"`
	Currency    string                `json:"currency"`
}

type masterMapping struct {
	Provider      domain.Provider `json:"provider"`
	Token         string          `json:"token"`
	TradingSymbol string          `json:"trading_symbol"`
	InstrumentKey string          `json:"instrument_key"`
	ValidFrom     time.Time       `json:"valid_from"`
	ValidUntil    time.Time       `json:"valid_until"`
}

func decodeMaster(data []byte) (instrumentmaster.Master, error) {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var encoded masterFile
	if err := decoder.Decode(&encoded); err != nil {
		return instrumentmaster.Master{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return instrumentmaster.Master{}, errors.New("instrument master must contain exactly one JSON document")
	}
	instruments := make([]domain.Instrument, 0, len(encoded.Instruments))
	byKey := make(map[string]domain.InstrumentID, len(encoded.Instruments))
	for _, item := range encoded.Instruments {
		underlying, err := domain.NewUnderlyingID(item.Underlying)
		if err != nil {
			return instrumentmaster.Master{}, err
		}
		lot, err := domain.NewQuantity(item.LotSize)
		if err != nil {
			return instrumentmaster.Master{}, err
		}
		tick, err := domain.NewPrice(item.TickMinor, item.Currency)
		if err != nil {
			return instrumentmaster.Master{}, err
		}
		currency, err := domain.NewCurrency(item.Currency)
		if err != nil {
			return instrumentmaster.Master{}, err
		}
		var derivative *domain.DerivativeSpec
		if item.Expiry != "" {
			parsed, err := time.Parse("2006-01-02", item.Expiry)
			if err != nil {
				return instrumentmaster.Master{}, err
			}
			expiry, _ := domain.NewCivilDate(parsed.Year(), parsed.Month(), parsed.Day())
			var strike domain.Price
			if item.Type == domain.InstrumentOption {
				strike, err = domain.NewPrice(item.StrikeMinor, item.Currency)
				if err != nil {
					return instrumentmaster.Master{}, err
				}
			}
			optionType := item.OptionType
			if optionType == "" {
				optionType = domain.OptionNone
			}
			derivative = &domain.DerivativeSpec{Expiry: expiry, Strike: strike, OptionType: optionType}
		}
		instrument, err := domain.NewInstrument(domain.InstrumentSpec{
			Exchange: item.Exchange, Segment: item.Segment, UnderlyingID: underlying,
			Type: item.Type, ExchangeSymbol: item.Symbol, Derivative: derivative,
			LotSize: lot, TickSize: tick, Currency: currency,
		})
		if err != nil {
			return instrumentmaster.Master{}, err
		}
		if item.Key == "" || !byKey[item.Key].IsZero() {
			return instrumentmaster.Master{}, errors.New("instrument keys must be non-empty and unique")
		}
		byKey[item.Key] = instrument.ID()
		instruments = append(instruments, instrument)
	}
	mappings := make([]domain.ProviderInstrumentRef, 0, len(encoded.Mappings))
	for _, item := range encoded.Mappings {
		id := byKey[item.InstrumentKey]
		if id.IsZero() {
			return instrumentmaster.Master{}, fmt.Errorf("unknown instrument key %q", item.InstrumentKey)
		}
		mappings = append(mappings, domain.ProviderInstrumentRef{
			Provider: item.Provider, Token: item.Token, TradingSymbol: item.TradingSymbol,
			InstrumentID: id, ValidFrom: item.ValidFrom, ValidUntil: item.ValidUntil,
		})
	}
	return instrumentmaster.New(encoded.AsOf, instruments, mappings)
}
