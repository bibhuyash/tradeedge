// Command tradeedge-prepare owns the bounded SHADOW pre-session workflow.
// It never starts the runtime and cannot reach a broker mutation API.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	brokerzerodha "github.com/bibhuyash/tradeedge/internal/adapters/broker/zerodha"
	zerodhaops "github.com/bibhuyash/tradeedge/internal/integration/zerodha"
)

var errLoginRequired = errors.New("Zerodha browser login required")

type runner interface {
	Run(string, ...string) (string, error)
}

type commandRunner struct{ directory string }

func (runner commandRunner) Run(name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	command.Dir = runner.directory
	raw, err := command.CombinedOutput()
	return string(raw), err
}

type options struct {
	repository, credentialsFile, sessionFile, validationCommand string
	now                                                         time.Time
	readOnly                                                    func([]string, string) (string, error)
	acceptanceOnly                                              bool
}

func main() {
	set := flag.NewFlagSet("tradeedge-prepare", flag.ExitOnError)
	repository := set.String("repo", ".", "repository root")
	credentials := set.String("credentials-file", ".env", "single untracked operator dotenv file")
	sessionFile := set.String("session-file", ".cache/tradeedge/session/zerodha.json", "persisted V2 Zerodha session")
	validationCommand := set.String("validation-command", "tradeedge-validation", "market-validation binary")
	acceptanceOnly := set.Bool("acceptance-only", false, "exercise the real V2 read-only path without producing release authorization")
	_ = set.Parse(os.Args[1:])
	readOnly := func(args []string, session string) (string, error) {
		var stdout, stderr bytes.Buffer
		if zerodhaops.ExecuteStoredReadOnly(args, session, &stdout, &stderr) != 0 {
			return stdout.String(), errors.New(strings.TrimSpace(stderr.String()))
		}
		return stdout.String(), nil
	}
	err := prepare(options{repository: *repository, credentialsFile: *credentials, sessionFile: *sessionFile, validationCommand: *validationCommand, now: time.Now(), readOnly: readOnly, acceptanceOnly: *acceptanceOnly}, commandRunner{directory: *repository}, os.Stdout)
	if errors.Is(err, errLoginRequired) {
		os.Exit(2)
	}
	if err != nil {
		writePreparationFailure(os.Stderr, err)
		os.Exit(1)
	}
}

func writePreparationFailure(output io.Writer, err error) {
	blocker := secretSafeBlocker(err)
	fmt.Fprintln(output, "SESSION_PREPARATION=BLOCKED")
	fmt.Fprintln(output, "BLOCKER="+blocker)
}

func secretSafeBlocker(err error) string {
	blocker := strings.NewReplacer("\r", " ", "\n", " ").Replace(strings.TrimSpace(err.Error()))
	for index, word := range strings.Fields(blocker) {
		for _, name := range []string{"api_key", "api_secret", "access_token", "password"} {
			prefix := name + "="
			if valueIndex := strings.Index(word, prefix); valueIndex >= 0 {
				words := strings.Fields(blocker)
				words[index] = word[:valueIndex] + prefix + "[REDACTED]"
				blocker = strings.Join(words, " ")
				break
			}
		}
	}
	return blocker
}

func prepare(value options, commands runner, output io.Writer) error {
	ist := time.FixedZone("IST", 5*60*60+30*60)
	now := value.now.In(ist)
	date := now.Format("2006-01-02")
	if value.readOnly == nil {
		return errors.New("read-only Zerodha operations unavailable")
	}
	if status, err := commands.Run("git", "status", "--porcelain"); err != nil || (!value.acceptanceOnly && strings.TrimSpace(status) != "") {
		return errors.New("working tree is not clean")
	}
	commitRaw, err := commands.Run("git", "rev-parse", "HEAD")
	commit := strings.TrimSpace(commitRaw)
	if err != nil || len(commit) != 40 {
		return errors.New("application commit unavailable")
	}
	shortCommit := commit[:7]
	root := filepath.Join(value.repository, ".cache", "market-validation", date)
	if value.acceptanceOnly {
		acceptanceRoot := filepath.Join(value.repository, ".cache", "market-validation")
		if err = os.MkdirAll(acceptanceRoot, 0o750); err != nil {
			return err
		}
		root, err = os.MkdirTemp(acceptanceRoot, "v2-acceptance-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(root) }()
	} else if err = os.MkdirAll(root, 0o750); err != nil {
		return err
	}

	instrumentPath := filepath.Join(root, "zerodha-instruments-"+shortCommit+".csv")
	instrumentOutput, err := value.readOnly([]string{"instrument-snapshot", "-output", instrumentPath}, value.sessionFile)
	if err != nil {
		return errors.New("instrument snapshot failed")
	}
	niftyForward, bankForward := field(instrumentOutput, "NIFTY_FORWARD_MINOR"), field(instrumentOutput, "BANKNIFTY_FORWARD_MINOR")
	if _, parseErr := strconv.ParseInt(niftyForward, 10, 64); parseErr != nil {
		return errors.New("invalid NIFTY forward reference")
	}
	if _, parseErr := strconv.ParseInt(bankForward, 10, 64); parseErr != nil {
		return errors.New("invalid BANKNIFTY forward reference")
	}
	policy := filepath.Join(value.repository, ".cache", "market-validation", "config", "nse-calendar-policy-2026.json")
	calendar := filepath.Join(root, "calendar-"+shortCommit+".json")
	calendarSources := filepath.Join(root, "calendar-sources-"+shortCommit+".json")
	calendarApproval := filepath.Join(root, "calendar-approval-"+shortCommit+".json")
	if _, err = commands.Run(value.validationCommand, "generate-calendar", "-policy", policy, "-date", date, "-calendar-output", calendar, "-sources-output", calendarSources); err != nil {
		return errors.New("calendar generation failed")
	}
	if _, err = commands.Run(value.validationCommand, "calendar-check", "-calendar", calendar, "-sources", calendarSources, "-from", date, "-to", date, "-output", calendarApproval); err != nil {
		return errors.New("calendar validation failed")
	}

	sessionOpen := time.Date(now.Year(), now.Month(), now.Day(), 9, 15, 0, 0, ist)
	snapshotAt := now
	mappingValidFrom := sessionOpen
	if snapshotAt.Before(sessionOpen) {
		mappingValidFrom = snapshotAt
	}
	sessionClose := time.Date(now.Year(), now.Month(), now.Day(), 16, 0, 0, 0, ist)
	if !sessionClose.After(now) {
		return errors.New("session authorization window has closed")
	}
	master := filepath.Join(root, "instrument-master-"+shortCommit+".json")
	watchlist := filepath.Join(root, "watchlist-"+shortCommit+".json")
	selection := filepath.Join(root, "selection-"+shortCommit+".json")
	if _, err = commands.Run(value.validationCommand, "generate-shadow-derivatives", "-dump", instrumentPath, "-nifty-forward-minor", niftyForward, "-banknifty-forward-minor", bankForward, "-as-of", snapshotAt.Format(time.RFC3339), "-valid-from", mappingValidFrom.Format(time.RFC3339), "-valid-until", sessionClose.Format(time.RFC3339), "-master-output", master, "-watchlist-output", watchlist, "-selection-output", selection); err != nil {
		return errors.New("mapping generation failed")
	}
	strategies := filepath.Join(value.repository, "configs", "validation", "strategies-shadow.json")
	portfolio := filepath.Join(value.repository, "configs", "validation", "portfolio.paper.json")
	risk := filepath.Join(value.repository, "configs", "validation", "risk.paper.json")
	niftyQualification := filepath.Join(value.repository, "configs", "validation", "qualification.nifty.shadow-collecting.json")
	bankQualification := filepath.Join(value.repository, "configs", "validation", "qualification.banknifty.shadow-collecting.json")
	bundle := filepath.Join(root, "runtime-bundle-"+shortCommit+".json")
	if _, err = commands.Run(value.validationCommand, "build-shadow-bundle", "-calendar", calendar, "-instrument-master", master, "-watchlist", watchlist, "-strategies", strategies, "-portfolio", portfolio, "-risk", risk, "-qualification-nifty", niftyQualification, "-qualification-banknifty", bankQualification, "-output", bundle); err != nil {
		return errors.New("runtime bundle generation failed")
	}
	telegram := filepath.Join(root, "telegram-check-"+shortCommit+".json")
	if !value.acceptanceOnly {
		if _, statErr := os.Stat(telegram); errors.Is(statErr, os.ErrNotExist) {
			if _, err = commands.Run(value.validationCommand, "telegram-check", "-date", date, "-mode", "SHADOW", "-output", telegram); err != nil {
				return errors.New("Telegram verification failed")
			}
		} else if statErr != nil {
			return statErr
		}
	}
	preflight := filepath.Join(root, "zerodha-preflight-"+shortCommit+".json")
	if _, statErr := os.Stat(preflight); errors.Is(statErr, os.ErrNotExist) {
		var preflightOutput string
		if preflightOutput, err = value.readOnly([]string{"preflight", "-runtime-bundle", bundle, "-timeout", "30s", "-mode", "SHADOW", "-credentials-file", "V2_SESSION_STORE", "-date", date, "-output", preflight}, value.sessionFile); err != nil {
			return errors.New("Zerodha preflight failed: " + preflightDiagnostic(preflightOutput))
		}
	} else if statErr != nil {
		return statErr
	}
	if value.acceptanceOnly {
		fmt.Fprintln(output, "SESSION_PREPARATION=ACCEPTANCE_PASS")
		fmt.Fprintln(output, "V2_SESSION=PASS")
		fmt.Fprintln(output, "INSTRUMENTS=PASS")
		fmt.Fprintln(output, "MARKET_VALIDATION=PASS")
		fmt.Fprintln(output, "RUNTIME_BUNDLE=NON_RELEASE_PASS")
		fmt.Fprintln(output, "ZERODHA_PREFLIGHT=PASS")
		fmt.Fprintln(output, "AUTHORIZATION=NOT_GENERATED")
		fmt.Fprintln(output, "SHADOW_STARTABLE=NO")
		fmt.Fprintln(output, "PAPER=DISABLED")
		fmt.Fprintln(output, "LIVE=DISABLED")
		fmt.Fprintln(output, "REAL_BROKER_MUTATION=UNREACHABLE")
		return nil
	}
	authorization := filepath.Join(root, "authorization-"+shortCommit+".json")
	if _, statErr := os.Stat(authorization); errors.Is(statErr, os.ErrNotExist) {
		authorizedAt := now
		if _, err = commands.Run(value.validationCommand, "build-shadow-authorization", "-date", date, "-commit", commit, "-authorized-at", authorizedAt.Format(time.RFC3339), "-expires-at", sessionClose.Format(time.RFC3339), "-runtime-bundle", bundle, "-calendar", calendar, "-calendar-approval", calendarApproval, "-instrument-master", master, "-watchlist", watchlist, "-strategies", strategies, "-portfolio", portfolio, "-risk", risk, "-qualification-nifty", niftyQualification, "-qualification-banknifty", bankQualification, "-telegram", telegram, "-preflight", preflight, "-output", authorization); err != nil {
			return errors.New("SHADOW authorization failed")
		}
	} else if statErr != nil {
		return statErr
	}
	if _, err = commands.Run(value.validationCommand, "prepare-session", "-date", date, "-commit", commit, "-authorization", authorization); err != nil {
		return errors.New("authorization inspection failed")
	}
	manifestHost, _ := filepath.Rel(value.repository, authorization)
	bundleHost, _ := filepath.Rel(value.repository, bundle)
	if err = rejectProcessOverride(value.credentialsFile, "TRADEEDGE_AUTHORIZATION_MANIFEST_HOST"); err != nil {
		return err
	}
	if err = rejectProcessOverride(value.credentialsFile, "TRADEEDGE_RUNTIME_BUNDLE_HOST"); err != nil {
		return err
	}
	if err = brokerzerodha.PersistPreparationPaths(value.credentialsFile, filepath.ToSlash(manifestHost), filepath.ToSlash(bundleHost)); err != nil {
		return err
	}
	fmt.Fprintln(output, "SESSION_PREPARATION=READY")
	fmt.Fprintln(output, "AUTHENTICATION=PASS")
	fmt.Fprintln(output, "ACCESS_TOKEN=STORED_SESSION")
	fmt.Fprintln(output, "INSTRUMENTS=PASS")
	fmt.Fprintln(output, "MAPPINGS=PASS")
	fmt.Fprintln(output, "RUNTIME_BUNDLE=PASS")
	fmt.Fprintln(output, "TELEGRAM=PASS")
	fmt.Fprintln(output, "ZERODHA_PREFLIGHT=PASS")
	fmt.Fprintln(output, "AUTHORIZATION=PASS")
	fmt.Fprintln(output, "COMPOSE=PASS")
	fmt.Fprintln(output, "PAPER=DISABLED")
	fmt.Fprintln(output, "LIVE=DISABLED")
	fmt.Fprintln(output, "REAL_BROKER_MUTATION=UNREACHABLE")
	fmt.Fprintln(output, "START_COMMAND=docker compose --env-file .env up -d tradeedge-shadow")
	return nil
}

func preflightDiagnostic(raw string) string {
	allowed := map[string]bool{
		"AUTHENTICATION": true, "REST_AUTH": true, "WEBSOCKET_AUTH": true,
		"ERROR_TYPE": true, "MESSAGE": true, "HTTP_STATUS": true,
		"BUNDLE_ERROR": true, "BUNDLE_DETAIL": true,
		"LAST_FAILURE_STAGE": true, "EXPECTED_TOKEN_COUNT": true,
		"FRESH_OBSERVATIONS": true, "OBSERVATIONS_RECEIVED": true,
		"HANDSHAKE": true, "SUBSCRIBE_SENT": true, "SHUTDOWN": true,
	}
	fields := make([]string, 0, len(allowed))
	for _, line := range strings.Split(raw, "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && allowed[name] && value != "" && !strings.ContainsAny(value, "\r\n\x00") {
			fields = append(fields, name+"="+value)
		}
	}
	if len(fields) == 0 {
		return "diagnostic unavailable"
	}
	return strings.Join(fields, "; ")
}

func field(raw, name string) string {
	prefix := name + "="
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func rejectProcessOverride(path, name string) error {
	fileValue, err := dotenvValue(path, name)
	if err != nil {
		return err
	}
	if processValue, exists := os.LookupEnv(name); exists && strings.TrimSpace(processValue) != "" && strings.TrimSpace(processValue) != fileValue {
		return errors.New("stale process-scoped environment override")
	}
	return nil
}

func dotenvValue(path, name string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	value := ""
	seen := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, name+"=") {
			if seen {
				return "", errors.New("duplicate dotenv entry")
			}
			seen = true
			value = strings.TrimSpace(strings.TrimPrefix(line, name+"="))
		}
	}
	return value, scanner.Err()
}
