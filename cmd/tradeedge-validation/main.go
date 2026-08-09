// Command tradeedge-validation produces fail-closed PAPER/SHADOW readiness
// and evidence artifacts. It never starts the runtime or grants authority.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	telegramadapter "github.com/bibhuyash/tradeedge/internal/adapters/notification/telegram"
	"github.com/bibhuyash/tradeedge/internal/marketvalidation"
	"github.com/bibhuyash/tradeedge/internal/notification"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tradeedge-validation <readiness|telegram-check|finalize-day|scorecard>")
	}
	switch args[0] {
	case "readiness":
		return readiness(args[1:])
	case "telegram-check":
		return telegramCheck(args[1:])
	case "finalize-day":
		return finalizeDay(args[1:])
	case "scorecard":
		return scorecard(args[1:])
	default:
		return errors.New("unknown market-validation command")
	}
}

func readiness(args []string) error {
	set := flag.NewFlagSet("readiness", flag.ContinueOnError)
	configPath := set.String("config", "", "readiness configuration JSON")
	output := set.String("output", "", "readiness evidence JSON")
	repo := set.String("repo", ".", "repository root")
	if err := set.Parse(args); err != nil || *configPath == "" || *output == "" {
		return errors.New("readiness requires -config and -output")
	}
	raw, err := os.ReadFile(*configPath)
	if err != nil {
		return fmt.Errorf("read readiness configuration: %w", err)
	}
	cfg, err := marketvalidation.DecodeReadinessConfig(raw)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := marketvalidation.RunReadiness(ctx, cfg, *repo, nil, time.Now)
	if err != nil {
		return err
	}
	raw, err = marketvalidation.Marshal(report)
	if err != nil {
		return err
	}
	if err := writeEvidence(*output, raw); err != nil {
		return err
	}
	if !report.Ready {
		return errors.New("market-validation readiness check failed; inspect the evidence report")
	}
	return nil
}

func telegramCheck(args []string) error {
	set := flag.NewFlagSet("telegram-check", flag.ContinueOnError)
	date := set.String("date", "", "trading date YYYY-MM-DD")
	mode := set.String("mode", "", "PAPER or SHADOW")
	output := set.String("output", "", "Telegram evidence JSON")
	if err := set.Parse(args); err != nil || *date == "" || *output == "" || (*mode != "PAPER" && *mode != "SHADOW") {
		return errors.New("telegram-check requires -date, -mode PAPER|SHADOW, and -output")
	}
	if _, err := time.Parse("2006-01-02", *date); err != nil {
		return errors.New("invalid trading date")
	}
	token, tokenOK := os.LookupEnv("TRADEEDGE_TELEGRAM_BOT_TOKEN")
	chatID, chatOK := os.LookupEnv("TRADEEDGE_TELEGRAM_CHAT_ID")
	if !tokenOK || !chatOK {
		return errors.New("Telegram runtime credentials are unavailable")
	}
	adapter, err := telegramadapter.New(telegramadapter.Config{Enabled: true, Token: token, ChatID: chatID}, &http.Client{Timeout: 5 * time.Second}, time.Now)
	if err != nil {
		return errors.New("Telegram runtime credentials are invalid")
	}
	identity := sha256.Sum256([]byte("market-validation-telegram-check/v1|" + *date + "|" + *mode))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = adapter.Send(ctx, notification.RenderedMessage{
		NotificationID: hex.EncodeToString(identity[:]),
		Text:           "TradeEdge " + *mode + " market-validation notification check. No trading action was taken.",
	})
	value := struct {
		SchemaVersion string    `json:"schema_version"`
		TradingDate   string    `json:"trading_date"`
		Mode          string    `json:"mode"`
		Delivered     bool      `json:"delivered"`
		CheckedAt     time.Time `json:"checked_at"`
	}{"market-validation-telegram-check/v1", *date, *mode, err == nil, time.Now().UTC()}
	raw, marshalErr := marketvalidation.Marshal(value)
	if marshalErr != nil {
		return marshalErr
	}
	if writeErr := writeEvidence(*output, raw); writeErr != nil {
		return writeErr
	}
	if err != nil {
		return errors.New("Telegram delivery check failed")
	}
	return nil
}

func finalizeDay(args []string) error {
	set := flag.NewFlagSet("finalize-day", flag.ContinueOnError)
	input := set.String("input", "", "draft daily record JSON")
	output := set.String("output", "", "final daily record JSON")
	if err := set.Parse(args); err != nil || *input == "" || *output == "" {
		return errors.New("finalize-day requires -input and -output")
	}
	var value marketvalidation.Record
	if err := decodeFile(*input, &value); err != nil {
		return err
	}
	final, err := marketvalidation.Finalize(value)
	if err != nil {
		return err
	}
	raw, err := marketvalidation.Marshal(final)
	if err != nil {
		return err
	}
	if err := writeEvidence(*output, raw); err != nil {
		return err
	}
	if final.FinalStatus == marketvalidation.StatusInvalid {
		return errors.New("session classified INVALID; evidence was preserved")
	}
	return nil
}

func scorecard(args []string) error {
	set := flag.NewFlagSet("scorecard", flag.ContinueOnError)
	recordsRoot := set.String("records", "", "directory containing *.day.json records")
	output := set.String("output", "", "scorecard JSON")
	if err := set.Parse(args); err != nil || *recordsRoot == "" || *output == "" {
		return errors.New("scorecard requires -records and -output")
	}
	paths, err := filepath.Glob(filepath.Join(*recordsRoot, "*.day.json"))
	if err != nil || len(paths) == 0 {
		return errors.New("no daily validation records found")
	}
	sort.Strings(paths)
	values := make([]marketvalidation.Record, len(paths))
	for index, path := range paths {
		if err := decodeFile(path, &values[index]); err != nil {
			return err
		}
	}
	value, err := marketvalidation.BuildScorecard(values)
	if err != nil {
		return err
	}
	raw, err := marketvalidation.Marshal(value)
	if err != nil {
		return err
	}
	return writeEvidence(*output, raw)
}

func decodeFile(path string, destination any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func writeEvidence(path string, raw []byte) error {
	clean := filepath.Clean(path)
	lower := strings.ToLower(clean)
	if clean == "." || strings.Contains(lower, "secret") || strings.Contains(lower, "credential") || strings.Contains(lower, "access_token") {
		return errors.New("unsafe evidence path")
	}
	if err := os.MkdirAll(filepath.Dir(clean), 0o750); err != nil {
		return err
	}
	file, err := os.OpenFile(clean, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(clean)
		if readErr == nil && bytes.Equal(existing, raw) {
			return nil
		}
		return errors.New("evidence file already exists with different content")
	}
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(raw); err != nil {
		return err
	}
	return file.Sync()
}
