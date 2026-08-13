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
	"github.com/bibhuyash/tradeedge/internal/config"
	"github.com/bibhuyash/tradeedge/internal/marketvalidation"
	"github.com/bibhuyash/tradeedge/internal/notification"
	"github.com/bibhuyash/tradeedge/internal/shadowruntime"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tradeedge-validation <readiness|telegram-check|calendar-check|generate-mappings|generate-derivatives|generate-shadow-derivatives|authorize|day0-gate|day1-gate|close-day0|finalize-day|scorecard>")
	}
	switch args[0] {
	case "readiness":
		return readiness(args[1:])
	case "telegram-check":
		return telegramCheck(args[1:])
	case "calendar-check":
		return calendarCheck(args[1:])
	case "generate-mappings":
		return generateMappings(args[1:])
	case "generate-derivatives":
		return generateDerivativeMappings(args[1:])
	case "generate-shadow-derivatives":
		return generateShadowDerivativeMappings(args[1:])
	case "build-shadow-bundle":
		return buildShadowBundle(args[1:])
	case "finalize-shadow-session":
		return finalizeShadowSession(args[1:])
	case "authorize":
		return authorize(args[1:])
	case "day0-gate":
		return day0Gate(args[1:])
	case "day1-gate":
		return day1Gate(args[1:])
	case "close-day0":
		return closeDay0(args[1:])
	case "finalize-day":
		return finalizeDay(args[1:])
	case "scorecard":
		return scorecard(args[1:])
	default:
		return errors.New("unknown market-validation command")
	}
}

func finalizeShadowSession(args []string) error {
	set := flag.NewFlagSet("finalize-shadow-session", flag.ContinueOnError)
	input, output := set.String("input", "", "closed real SHADOW session draft"), set.String("output", "", "evidence/real-market/YYYY-MM-DD/shadow-session.json")
	if err := set.Parse(args); err != nil || *input == "" || *output == "" {
		return errors.New("finalize-shadow-session requires input and output")
	}
	clean := filepath.ToSlash(filepath.Clean(*output))
	if !strings.Contains(clean, "evidence/real-market/") || !strings.HasSuffix(clean, "/shadow-session.json") {
		return errors.New("real SHADOW evidence output path required")
	}
	var draft shadowruntime.RealMarketEvidence
	if err := decodeFile(*input, &draft); err != nil {
		return err
	}
	value, err := shadowruntime.FinalizeRealMarketEvidence(draft)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeEvidence(*output, append(raw, '\n'))
}

func buildShadowBundle(args []string) error {
	set := flag.NewFlagSet("build-shadow-bundle", flag.ContinueOnError)
	calendar := set.String("calendar", "", "approved calendar")
	master := set.String("instrument-master", "", "generated instrument master")
	watchlist := set.String("watchlist", "", "generated bounded watchlist")
	strategies := set.String("strategies", "", "SHADOW strategy configuration")
	portfolio := set.String("portfolio", "", "portfolio configuration")
	risk := set.String("risk", "", "Phase 3 risk configuration")
	nifty := set.String("qualification-nifty", "", "NIFTY qualification configuration")
	bank := set.String("qualification-banknifty", "", "BANKNIFTY qualification configuration")
	output := set.String("output", "", "runtime bundle output")
	if err := set.Parse(args); err != nil || *calendar == "" || *master == "" || *watchlist == "" || *strategies == "" || *portfolio == "" || *risk == "" || *nifty == "" || *bank == "" || *output == "" {
		return errors.New("build-shadow-bundle requires all checksum-pinned input paths and output")
	}
	base := filepath.Dir(*output)
	ref := func(path string) (config.FileReference, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return config.FileReference{}, err
		}
		sum := sha256.Sum256(raw)
		relative, err := filepath.Rel(base, path)
		if err != nil {
			return config.FileReference{}, err
		}
		return config.FileReference{Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(sum[:])}, nil
	}
	paths := []string{*calendar, *master, *watchlist, *strategies, *portfolio, *risk, *nifty, *bank}
	refs := make([]config.FileReference, len(paths))
	for index, path := range paths {
		value, err := ref(path)
		if err != nil {
			return err
		}
		refs[index] = value
	}
	manifest := config.RuntimeBundleManifest{SchemaVersion: config.RuntimeBundleSchemaVersion, Mode: config.ZerodhaModeShadow, Calendar: refs[0], InstrumentMaster: refs[1], Watchlist: refs[2], Strategies: refs[3], Portfolio: refs[4], Risk: refs[5], QualificationNIFTY: &refs[6], QualificationBANKNIFTY: &refs[7]}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeEvidence(*output, append(raw, '\n'))
}

func generateShadowDerivativeMappings(args []string) error {
	set := flag.NewFlagSet("generate-shadow-derivatives", flag.ContinueOnError)
	dump := set.String("dump", "", "current Zerodha instruments CSV")
	nifty := set.Int64("nifty-forward-minor", 0, "accepted NIFTY future reference in INR minor units")
	bank := set.Int64("banknifty-forward-minor", 0, "accepted BANKNIFTY future reference in INR minor units")
	asOfText, fromText, untilText := set.String("as-of", "", "RFC3339 dump time"), set.String("valid-from", "", "RFC3339 mapping start"), set.String("valid-until", "", "RFC3339 mapping expiry")
	masterOut, watchlistOut, selectionOut := set.String("master-output", "", "instrument master JSON"), set.String("watchlist-output", "", "watchlist JSON"), set.String("selection-output", "", "resolved selection JSON")
	if err := set.Parse(args); err != nil || *dump == "" || *nifty <= 0 || *bank <= 0 || *asOfText == "" || *fromText == "" || *untilText == "" || *masterOut == "" || *watchlistOut == "" || *selectionOut == "" {
		return errors.New("generate-shadow-derivatives requires dump, both forward references, timestamps, and all outputs")
	}
	dumpRaw, err := os.ReadFile(*dump)
	if err != nil {
		return err
	}
	asOf, err := time.Parse(time.RFC3339, *asOfText)
	if err != nil {
		return err
	}
	validFrom, err := time.Parse(time.RFC3339, *fromText)
	if err != nil {
		return err
	}
	validUntil, err := time.Parse(time.RFC3339, *untilText)
	if err != nil {
		return err
	}
	generated, selection, err := marketvalidation.GenerateShadowDerivativeMappings(dumpRaw, *nifty, *bank, asOf, validFrom, validUntil)
	if err != nil {
		return err
	}
	selectionRaw, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return err
	}
	if err = writeEvidence(*masterOut, generated.InstrumentMaster); err != nil {
		return err
	}
	if err = writeEvidence(*watchlistOut, generated.Watchlist); err != nil {
		return err
	}
	return writeEvidence(*selectionOut, append(selectionRaw, '\n'))
}

func generateDerivativeMappings(args []string) error {
	set := flag.NewFlagSet("generate-derivatives", flag.ContinueOnError)
	dump := set.String("dump", "", "current Zerodha instruments CSV")
	reference := set.Int64("forward-reference-minor", 0, "accepted NIFTY future reference in INR minor units")
	asOfText, fromText, untilText := set.String("as-of", "", "RFC3339 dump time"), set.String("valid-from", "", "RFC3339 mapping start"), set.String("valid-until", "", "RFC3339 mapping expiry")
	masterOut, watchlistOut, selectionOut := set.String("master-output", "", "instrument master JSON"), set.String("watchlist-output", "", "watchlist JSON"), set.String("selection-output", "", "resolved selection JSON")
	if err := set.Parse(args); err != nil || *dump == "" || *reference <= 0 || *asOfText == "" || *fromText == "" || *untilText == "" || *masterOut == "" || *watchlistOut == "" || *selectionOut == "" {
		return errors.New("generate-derivatives requires dump, forward reference, timestamps, and all outputs")
	}
	dumpRaw, err := os.ReadFile(*dump)
	if err != nil {
		return err
	}
	asOf, err := time.Parse(time.RFC3339, *asOfText)
	if err != nil {
		return err
	}
	validFrom, err := time.Parse(time.RFC3339, *fromText)
	if err != nil {
		return err
	}
	validUntil, err := time.Parse(time.RFC3339, *untilText)
	if err != nil {
		return err
	}
	generated, selection, err := marketvalidation.GenerateNIFTYDerivativeMappings(dumpRaw, *reference, asOf, validFrom, validUntil)
	if err != nil {
		return err
	}
	selectionRaw, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return err
	}
	if err = writeEvidence(*masterOut, generated.InstrumentMaster); err != nil {
		return err
	}
	if err = writeEvidence(*watchlistOut, generated.Watchlist); err != nil {
		return err
	}
	return writeEvidence(*selectionOut, append(selectionRaw, '\n'))
}

func closeDay0(args []string) error {
	set := flag.NewFlagSet("close-day0", flag.ContinueOnError)
	input, output := set.String("input", "", "Day-0 closure draft JSON"), set.String("output", "", "final Day-0 closure JSON")
	if err := set.Parse(args); err != nil || *input == "" || *output == "" {
		return errors.New("close-day0 requires -input and -output")
	}
	raw, err := os.ReadFile(*input)
	if err != nil {
		return err
	}
	draft, err := marketvalidation.DecodeDay0ClosureDraft(raw)
	if err != nil {
		return err
	}
	closure, err := marketvalidation.FinalizeDay0Closure(*output, draft)
	if err != nil {
		return err
	}
	raw, err = marketvalidation.Marshal(closure)
	if err != nil {
		return err
	}
	if err = writeEvidence(*output, raw); err != nil {
		return err
	}
	if closure.Classification == marketvalidation.Day0SessionFail {
		return errors.New("Day-0 session classified SESSION_FAIL; evidence was preserved")
	}
	return nil
}

func calendarCheck(args []string) error {
	set := flag.NewFlagSet("calendar-check", flag.ContinueOnError)
	calendarPath := set.String("calendar", "", "calendar JSON")
	sourcesPath := set.String("sources", "", "approved source references JSON")
	from, to := set.String("from", "", "coverage start"), set.String("to", "", "coverage end")
	cas := set.Bool("cas-required", true, "require PRE_CAS/CAS/POST_CAS regimes")
	output := set.String("output", "", "approval evidence JSON")
	if err := set.Parse(args); err != nil || *calendarPath == "" || *sourcesPath == "" || *from == "" || *to == "" || *output == "" {
		return errors.New("calendar-check requires -calendar, -sources, -from, -to, and -output")
	}
	raw, err := os.ReadFile(*sourcesPath)
	if err != nil {
		return err
	}
	sources, err := marketvalidation.DecodeCalendarSources(raw)
	if err != nil {
		return err
	}
	approval, err := marketvalidation.ApproveCalendar(*calendarPath, sources, *from, *to, *cas)
	if err != nil {
		return err
	}
	raw, err = marketvalidation.Marshal(approval)
	if err != nil {
		return err
	}
	return writeEvidence(*output, raw)
}

func generateMappings(args []string) error {
	set := flag.NewFlagSet("generate-mappings", flag.ContinueOnError)
	dump := set.String("dump", "", "current Zerodha instruments CSV")
	selectionPath := set.String("selection", "", "canonical selection JSON")
	asOfText, fromText, untilText := set.String("as-of", "", "RFC3339 dump time"), set.String("valid-from", "", "RFC3339 mapping start"), set.String("valid-until", "", "RFC3339 mapping expiry")
	masterOut, watchlistOut := set.String("master-output", "", "instrument master JSON"), set.String("watchlist-output", "", "watchlist JSON")
	if err := set.Parse(args); err != nil || *dump == "" || *selectionPath == "" || *asOfText == "" || *fromText == "" || *untilText == "" || *masterOut == "" || *watchlistOut == "" {
		return errors.New("generate-mappings requires dump, selection, timestamps, and both outputs")
	}
	dumpRaw, err := os.ReadFile(*dump)
	if err != nil {
		return err
	}
	selectionRaw, err := os.ReadFile(*selectionPath)
	if err != nil {
		return err
	}
	selection, err := marketvalidation.DecodeMappingSelection(selectionRaw)
	if err != nil {
		return err
	}
	asOf, err := time.Parse(time.RFC3339, *asOfText)
	if err != nil {
		return err
	}
	validFrom, err := time.Parse(time.RFC3339, *fromText)
	if err != nil {
		return err
	}
	validUntil, err := time.Parse(time.RFC3339, *untilText)
	if err != nil {
		return err
	}
	generated, err := marketvalidation.GenerateMappings(dumpRaw, selection, asOf, validFrom, validUntil)
	if err != nil {
		return err
	}
	if err = writeEvidence(*masterOut, generated.InstrumentMaster); err != nil {
		return err
	}
	return writeEvidence(*watchlistOut, generated.Watchlist)
}

func authorize(args []string) error {
	set := flag.NewFlagSet("authorize", flag.ContinueOnError)
	input, output := set.String("input", "", "authorization draft JSON"), set.String("output", "", "final authorization JSON")
	if err := set.Parse(args); err != nil || *input == "" || *output == "" {
		return errors.New("authorize requires -input and -output")
	}
	var draft marketvalidation.AuthorizationManifest
	if err := decodeFile(*input, &draft); err != nil {
		return err
	}
	final, err := marketvalidation.FinalizeAuthorization(*output, draft)
	if err != nil {
		return err
	}
	raw, err := marketvalidation.Marshal(final)
	if err != nil {
		return err
	}
	return writeEvidence(*output, raw)
}

func day0Gate(args []string) error {
	set := flag.NewFlagSet("day0-gate", flag.ContinueOnError)
	evidencePath, authPath, output := set.String("evidence", "", "Day-0 evidence JSON"), set.String("authorization", "", "authorization manifest"), set.String("output", "", "Day-0 gate JSON")
	if err := set.Parse(args); err != nil || *evidencePath == "" || *authPath == "" || *output == "" {
		return errors.New("day0-gate requires -evidence, -authorization, and -output")
	}
	auth, err := marketvalidation.LoadAuthorization(*authPath)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*evidencePath)
	if err != nil {
		return err
	}
	report, err := marketvalidation.EvaluateDay0(raw, auth, time.Now())
	if err != nil {
		return err
	}
	encoded, err := marketvalidation.Marshal(report)
	if err != nil {
		return err
	}
	if err = writeEvidence(*output, encoded); err != nil {
		return err
	}
	if !report.Passed {
		return errors.New("Day-0 gate failed; inspect evidence")
	}
	return nil
}

func day1Gate(args []string) error {
	set := flag.NewFlagSet("day1-gate", flag.ContinueOnError)
	day0Path, authPath, output := set.String("day0", "", "final Day-0 gate JSON"), set.String("authorization", "", "Day-1 authorization manifest"), set.String("output", "", "Day-1 gate JSON")
	if err := set.Parse(args); err != nil || *day0Path == "" || *authPath == "" || *output == "" {
		return errors.New("day1-gate requires -day0, -authorization, and -output")
	}
	auth, err := marketvalidation.LoadAuthorization(*authPath)
	if err != nil {
		return err
	}
	var day0 marketvalidation.GateReport
	if err := decodeFile(*day0Path, &day0); err != nil {
		return err
	}
	report := marketvalidation.EvaluateDay1(day0, auth, time.Now())
	encoded, err := marketvalidation.Marshal(report)
	if err != nil {
		return err
	}
	if err = writeEvidence(*output, encoded); err != nil {
		return err
	}
	if !report.Passed {
		return errors.New(strings.Join(report.Reasons, ","))
	}
	return nil
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
	kind := set.String("kind", "test", "test, critical, or eod")
	output := set.String("output", "", "Telegram evidence JSON")
	if err := set.Parse(args); err != nil || *date == "" || *output == "" || (*mode != "PAPER" && *mode != "SHADOW") || (*kind != "test" && *kind != "critical" && *kind != "eod") {
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
	identity := sha256.Sum256([]byte("market-validation-telegram-check/v1|" + *date + "|" + *mode + "|" + *kind))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = adapter.Send(ctx, notification.RenderedMessage{
		NotificationID: hex.EncodeToString(identity[:]),
		Text:           "TradeEdge " + *mode + " market-validation " + *kind + " notification acceptance. No trading action was taken.",
	})
	value := struct {
		SchemaVersion string    `json:"schema_version"`
		TradingDate   string    `json:"trading_date"`
		Mode          string    `json:"mode"`
		Kind          string    `json:"kind"`
		Delivered     bool      `json:"delivered"`
		CheckedAt     time.Time `json:"checked_at"`
	}{"market-validation-telegram-check/v1", *date, *mode, *kind, err == nil, time.Now().UTC()}
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
