package main

import (
	"context"
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

	fileadapter "github.com/bibhuyash/tradeedge/internal/adapters/marketdata/file"
	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
	"github.com/bibhuyash/tradeedge/internal/marketdata"
	"github.com/bibhuyash/tradeedge/internal/marketdata/ingest"
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
		return errors.New("usage: tradeedge-marketdata <ingest|verify|replay> [flags]")
	}
	switch args[0] {
	case "ingest":
		return runIngest(ctx, args[1:], output)
	case "verify":
		return runVerify(ctx, args[1:], output)
	case "replay":
		return runReplay(ctx, args[1:], output)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runIngest(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("ingest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	inputPath := flags.String("input", "", "NDJSON or NDJSON.gz observation fixture")
	masterPath := flags.String("master", "", "instrument master JSON")
	root := flags.String("root", "", "dataset repository directory")
	lateness := flags.Duration("allowed-lateness", 2*time.Second, "out-of-order watermark allowance")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *inputPath == "" || *masterPath == "" || *root == "" {
		return errors.New("ingest requires -input, -master, and -root")
	}
	masterBytes, err := os.ReadFile(*masterPath)
	if err != nil {
		return err
	}
	master, err := decodeMaster(masterBytes)
	if err != nil {
		return err
	}
	calendar, err := decodeCalendar(masterBytes)
	if err != nil {
		return err
	}
	masterRepository := instrumentmaster.NewMemoryRepository()
	if err := masterRepository.Put(ctx, master); err != nil {
		return err
	}
	repository := fileadapter.Repository{Root: *root}
	writer, err := repository.Create(ctx, storage.DraftManifest{
		MasterVersion: string(master.Version()), InstrumentMaster: masterBytes,
		Source: "file-fixture", OrderingVersion: "exchange-sequence-event-id/v1",
		CreatedAt: master.AsOf(),
	})
	if err != nil {
		return err
	}
	service := ingest.Service{
		Normalizer: ingest.Normalizer{
			Resolver: instrumentmaster.Resolver{Repository: masterRepository},
			Calendar: calendar,
		},
		AllowedLateness: *lateness, BufferCapacity: 10000,
	}
	if err := service.Ingest(ctx, fileadapter.Source{Path: *inputPath},
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
	Sessions    []masterSession    `json:"sessions"`
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

type masterSession struct {
	Exchange    domain.Exchange `json:"exchange"`
	TradingDate string          `json:"trading_date"`
	Open        time.Time       `json:"open"`
	Close       time.Time       `json:"close"`
	Version     string          `json:"version"`
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

func decodeCalendar(data []byte) (*model.StaticCalendar, error) {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var encoded masterFile
	if err := decoder.Decode(&encoded); err != nil {
		return nil, err
	}
	if len(encoded.Sessions) == 0 {
		return nil, errors.New("instrument master must include at least one explicit market session")
	}
	sessions := make([]model.MarketSession, 0, len(encoded.Sessions))
	for _, item := range encoded.Sessions {
		parsed, err := time.Parse("2006-01-02", item.TradingDate)
		if err != nil {
			return nil, err
		}
		date, err := domain.NewCivilDate(parsed.Year(), parsed.Month(), parsed.Day())
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, model.MarketSession{
			Exchange: item.Exchange, TradingDate: date,
			Open: item.Open, Close: item.Close, Version: item.Version,
		})
	}
	return model.NewStaticCalendar(sessions)
}
