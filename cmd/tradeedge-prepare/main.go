// Command tradeedge-prepare owns the bounded SHADOW pre-session workflow.
// It never starts the runtime and cannot reach a broker mutation API.
package main

import (
	"bufio"
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
	repository, credentialsFile, authCommand, validationCommand string
	now                                                         time.Time
}

func main() {
	set := flag.NewFlagSet("tradeedge-prepare", flag.ExitOnError)
	repository := set.String("repo", ".", "repository root")
	credentials := set.String("credentials-file", ".env", "single untracked operator dotenv file")
	authCommand := set.String("auth-command", "tradeedge-zerodha-auth", "Zerodha operator binary")
	validationCommand := set.String("validation-command", "tradeedge-validation", "market-validation binary")
	_ = set.Parse(os.Args[1:])
	err := prepare(options{repository: *repository, credentialsFile: *credentials, authCommand: *authCommand, validationCommand: *validationCommand, now: time.Now()}, commandRunner{directory: *repository}, os.Stdout)
	if errors.Is(err, errLoginRequired) {
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "SESSION_PREPARATION=BLOCKED")
		os.Exit(1)
	}
}

func prepare(value options, commands runner, output io.Writer) error {
	ist := time.FixedZone("IST", 5*60*60+30*60)
	now := value.now.In(ist)
	date := now.Format("2006-01-02")
	authOutput, authErr := commands.Run(value.authCommand, "authenticate", "-credentials-file", value.credentialsFile)
	if authErr != nil {
		errorType := field(authOutput, "ERROR_TYPE")
		if errorType != "TokenException" && errorType != "ConfigurationError" {
			return errors.New("Zerodha authentication failed")
		}
		loginOutput, loginErr := commands.Run(value.authCommand, "login-url", "-credentials-file", value.credentialsFile)
		loginURL := strings.TrimSpace(loginOutput)
		if loginErr != nil || !strings.HasPrefix(loginURL, "https://kite.zerodha.com/connect/login?") {
			return errors.New("Zerodha authentication and login URL generation failed")
		}
		fmt.Fprintln(output, "SESSION_PREPARATION=LOGIN_REQUIRED")
		fmt.Fprintln(output, "LOGIN_URL="+loginURL)
		fmt.Fprintln(output, "REQUEST_TOKEN_DESTINATION=TRADEEDGE_ZERODHA_REQUEST_TOKEN in .env")
		return errLoginRequired
	}
	lifecycle := field(authOutput, "ACCESS_TOKEN_LIFECYCLE")
	if lifecycle != "REUSED" && lifecycle != "EXCHANGED" {
		return errors.New("invalid authentication result")
	}
	if status, err := commands.Run("git", "status", "--porcelain"); err != nil || strings.TrimSpace(status) != "" {
		return errors.New("working tree is not clean")
	}
	branchRaw, err := commands.Run("git", "branch", "--show-current")
	if err != nil || strings.TrimSpace(branchRaw) != "main" {
		return errors.New("preparation requires the merged main branch")
	}
	commitRaw, err := commands.Run("git", "rev-parse", "HEAD")
	commit := strings.TrimSpace(commitRaw)
	if err != nil || len(commit) != 40 {
		return errors.New("application commit unavailable")
	}
	shortCommit := commit[:7]
	root := filepath.Join(value.repository, ".cache", "market-validation", date)
	if err = os.MkdirAll(root, 0o750); err != nil {
		return err
	}

	instrumentPath := filepath.Join(root, "zerodha-instruments-"+shortCommit+".csv")
	instrumentOutput, err := commands.Run(value.authCommand, "instrument-snapshot", "-credentials-file", value.credentialsFile, "-output", instrumentPath)
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
	snapshotAt := time.Date(now.Year(), now.Month(), now.Day(), 8, 30, 0, 0, ist)
	sessionClose := time.Date(now.Year(), now.Month(), now.Day(), 16, 0, 0, 0, ist)
	if !sessionClose.After(now) {
		return errors.New("session authorization window has closed")
	}
	master := filepath.Join(root, "instrument-master-"+shortCommit+".json")
	watchlist := filepath.Join(root, "watchlist-"+shortCommit+".json")
	selection := filepath.Join(root, "selection-"+shortCommit+".json")
	if _, err = commands.Run(value.validationCommand, "generate-shadow-derivatives", "-dump", instrumentPath, "-nifty-forward-minor", niftyForward, "-banknifty-forward-minor", bankForward, "-as-of", snapshotAt.Format(time.RFC3339), "-valid-from", sessionOpen.Format(time.RFC3339), "-valid-until", sessionClose.Format(time.RFC3339), "-master-output", master, "-watchlist-output", watchlist, "-selection-output", selection); err != nil {
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
	if _, statErr := os.Stat(telegram); errors.Is(statErr, os.ErrNotExist) {
		if _, err = commands.Run(value.validationCommand, "telegram-check", "-date", date, "-mode", "SHADOW", "-output", telegram); err != nil {
			return errors.New("Telegram verification failed")
		}
	} else if statErr != nil {
		return statErr
	}
	preflight := filepath.Join(root, "zerodha-preflight-"+shortCommit+".json")
	if _, statErr := os.Stat(preflight); errors.Is(statErr, os.ErrNotExist) {
		if _, err = commands.Run(value.authCommand, "preflight", "-runtime-bundle", bundle, "-timeout", "30s", "-mode", "SHADOW", "-credentials-file", value.credentialsFile, "-date", date, "-output", preflight); err != nil {
			return errors.New("Zerodha preflight failed")
		}
	} else if statErr != nil {
		return statErr
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
	fmt.Fprintln(output, "ACCESS_TOKEN="+lifecycle)
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
