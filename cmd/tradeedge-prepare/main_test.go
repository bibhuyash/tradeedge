package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	failCommand string
	authError   string
	failed      bool
	dirty       bool
	calls       []string
}

func (runner *fakeRunner) Run(name string, args ...string) (string, error) {
	command := name + " " + strings.Join(args, " ")
	runner.calls = append(runner.calls, command)
	if runner.failCommand != "" && strings.Contains(command, runner.failCommand) && !runner.failed {
		runner.failed = true
		if strings.Contains(command, "auth authenticate") {
			errorType := runner.authError
			if errorType == "" {
				errorType = "TokenException"
			}
			return "AUTHENTICATION=FAIL\nERROR_TYPE=" + errorType + "\n", errors.New("fixture failure")
		}
		return "", errors.New("fixture failure")
	}
	if name == "git" && len(args) > 0 && args[0] == "status" {
		if runner.dirty {
			return " M cmd/tradeedge-prepare/main.go\n", nil
		}
		return "", nil
	}
	if name == "git" && len(args) > 0 && args[0] == "branch" {
		return "main\n", nil
	}
	if name == "git" && len(args) > 0 && args[0] == "rev-parse" {
		return strings.Repeat("a", 40) + "\n", nil
	}
	if len(args) > 0 && args[0] == "authenticate" {
		return "AUTHENTICATION=PASS\nACCESS_TOKEN_LIFECYCLE=REUSED\n", nil
	}
	if len(args) > 0 && args[0] == "login-url" {
		return "https://kite.zerodha.com/connect/login?api_key=public&v=3\n", nil
	}
	if len(args) > 0 && args[0] == "instrument-snapshot" {
		writeFlagFile(args, "-output")
		return "INSTRUMENTS=PASS\nNIFTY_FORWARD_MINOR=2500000\nBANKNIFTY_FORWARD_MINOR=5500000\n", nil
	}
	for _, flag := range []string{"-calendar-output", "-sources-output", "-master-output", "-watchlist-output", "-selection-output", "-output"} {
		writeFlagFile(args, flag)
	}
	return "PASS\n", nil
}

func TestNormalPreparationRejectsDirtyTreeAndAcceptanceOnlyIsNonAuthorizing(t *testing.T) {
	value, credentials := preparationFixture(t)
	runner := &fakeRunner{dirty: true}
	var output bytes.Buffer
	if err := prepare(value, runner, &output); err == nil || !strings.Contains(err.Error(), "working tree is not clean") {
		t.Fatalf("normal dirty preparation was not blocked: err=%v output=%q", err, output.String())
	}

	value.acceptanceOnly = true
	output.Reset()
	runner.calls = nil
	if err := prepare(value, runner, &output); err != nil {
		t.Fatalf("acceptance-only: %v", err)
	}
	for _, expected := range []string{"SESSION_PREPARATION=ACCEPTANCE_PASS", "AUTHORIZATION=NOT_GENERATED", "SHADOW_STARTABLE=NO"} {
		if !strings.Contains(output.String(), expected+"\n") {
			t.Fatalf("missing %q in %q", expected, output.String())
		}
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "build-shadow-authorization") || strings.Contains(call, "prepare-session") || strings.Contains(call, "tradeedge-zerodha-auth") {
			t.Fatalf("acceptance invoked release/auth path: %q", call)
		}
	}
	raw, err := os.ReadFile(credentials)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "authorization-aaaaaaa.json") || strings.Contains(string(raw), "runtime-bundle-aaaaaaa.json") {
		t.Fatalf("acceptance persisted release selectors: %q", raw)
	}
	if _, err = os.Stat(value.selectorsFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("acceptance created selector file: %v", err)
	}
}

func writeFlagFile(args []string, name string) {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			_ = os.MkdirAll(filepath.Dir(args[index+1]), 0o750)
			_ = os.WriteFile(args[index+1], []byte("fixture\n"), 0o640)
		}
	}
}

func preparationFixture(t *testing.T) (options, string) {
	t.Helper()
	repository := t.TempDir()
	credentials := filepath.Join(repository, ".env")
	if err := os.WriteFile(credentials, []byte("TRADEEDGE_AUTHORIZATION_MANIFEST_HOST=\nTRADEEDGE_RUNTIME_BUNDLE_HOST=\nUNCHANGED=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	readOnly := func(args []string, _ string) (string, error) {
		writeFlagFile(args, "-output")
		if args[0] == "instrument-snapshot" {
			return "INSTRUMENTS=PASS\nNIFTY_FORWARD_MINOR=2500000\nBANKNIFTY_FORWARD_MINOR=5500000\n", nil
		}
		return "AUTHENTICATION=PASS\nREST_AUTH=PASS\nWEBSOCKET_AUTH=PASS\n", nil
	}
	return options{repository: repository, credentialsFile: credentials, selectorsFile: filepath.Join(repository, ".cache", "tradeedge", "preparation.env"), sessionFile: filepath.Join(repository, "zerodha.json"), validationCommand: "validation", now: time.Date(2026, 9, 18, 4, 30, 0, 0, time.UTC), readOnly: readOnly}, credentials
}

func TestPrepareReadyAndRepeatedInvocation(t *testing.T) {
	value, _ := preparationFixture(t)
	runner := &fakeRunner{}
	for attempt := 0; attempt < 2; attempt++ {
		var output bytes.Buffer
		if err := prepare(value, runner, &output); err != nil {
			t.Fatalf("attempt %d: %v", attempt+1, err)
		}
		for _, expected := range []string{"SESSION_PREPARATION=READY", "ACCESS_TOKEN=STORED_SESSION", "INSTRUMENTS=PASS", "MAPPINGS=PASS", "ZERODHA_PREFLIGHT=PASS", "PAPER=DISABLED", "LIVE=DISABLED", "REAL_BROKER_MUTATION=UNREACHABLE"} {
			if !strings.Contains(output.String(), expected+"\n") {
				t.Fatalf("missing %q in %q", expected, output.String())
			}
		}
	}
	raw, err := os.ReadFile(value.selectorsFile)
	if err != nil || !strings.Contains(string(raw), "TRADEEDGE_AUTHORIZATION_MANIFEST_HOST=.cache/market-validation/2026-09-18/authorization-aaaaaaa.json\n") || !strings.Contains(string(raw), "TRADEEDGE_RUNTIME_BUNDLE_HOST=.cache/market-validation/2026-09-18/runtime-bundle-aaaaaaa.json\n") {
		t.Fatalf("selectors=%q err=%v", raw, err)
	}
}

func TestPrepareMappingAsOfFallsWithinValidity(t *testing.T) {
	value, _ := preparationFixture(t)
	runner := &fakeRunner{}
	if err := prepare(value, runner, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "generate-shadow-derivatives") {
			if !strings.Contains(call, "-as-of 2026-09-18T10:00:00+05:30") || !strings.Contains(call, "-valid-from 2026-09-18T09:15:00+05:30") {
				t.Fatalf("invalid mapping interval: %q", call)
			}
			return
		}
	}
	t.Fatal("mapping generation was not invoked")
}

func TestPrepareFailuresAreFailClosedAndResume(t *testing.T) {
	for _, stage := range []string{"generate-calendar", "calendar-check", "generate-shadow-derivatives", "build-shadow-bundle", "telegram-check", "build-shadow-authorization", "prepare-session"} {
		t.Run(stage, func(t *testing.T) {
			value, _ := preparationFixture(t)
			runner := &fakeRunner{failCommand: stage}
			var output bytes.Buffer
			if err := prepare(value, runner, &output); err == nil || strings.Contains(output.String(), "SESSION_PREPARATION=READY") {
				t.Fatalf("stage %s did not fail closed: err=%v output=%q", stage, err, output.String())
			}
			output.Reset()
			if err := prepare(value, runner, &output); err != nil || !strings.Contains(output.String(), "SESSION_PREPARATION=READY") {
				t.Fatalf("stage %s did not resume: err=%v output=%q", stage, err, output.String())
			}
		})
	}
}

func TestPrepareGeneratedSelectorsOverrideStaleProcessValue(t *testing.T) {
	value, _ := preparationFixture(t)
	t.Setenv("TRADEEDGE_AUTHORIZATION_MANIFEST_HOST", "stale/session.json")
	var output bytes.Buffer
	if err := prepare(value, &fakeRunner{}, &output); err != nil || !strings.Contains(output.String(), "SESSION_PREPARATION=READY") {
		t.Fatalf("generated selectors were not authoritative: err=%v output=%q", err, output.String())
	}
}

func TestBlockedPreparationAlwaysIncludesBlocker(t *testing.T) {
	var output bytes.Buffer
	writePreparationFailure(&output, errors.New("authentication failed: access_token=secret-value"))

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || lines[0] != "SESSION_PREPARATION=BLOCKED" {
		t.Fatalf("unexpected blocked output: %q", output.String())
	}
	if !strings.HasPrefix(lines[1], "BLOCKER=") || strings.TrimSpace(strings.TrimPrefix(lines[1], "BLOCKER=")) == "" {
		t.Fatalf("blocked output omitted blocker: %q", output.String())
	}
	if strings.Contains(output.String(), "secret-value") {
		t.Fatalf("blocked output leaked secret: %q", output.String())
	}
}

func TestPreflightDiagnosticIsUsefulAndSecretSafe(t *testing.T) {
	raw := "AUTHENTICATION=PASS\nREST_AUTH=PASS\nWEBSOCKET_AUTH=FAIL\nERROR_TYPE=WebSocketTimeout\nLAST_FAILURE_STAGE=FRESHNESS\nOBSERVATIONS_RECEIVED=0\nACCESS_TOKEN=secret\n"
	got := preflightDiagnostic(raw)
	for _, expected := range []string{"AUTHENTICATION=PASS", "WEBSOCKET_AUTH=FAIL", "LAST_FAILURE_STAGE=FRESHNESS", "OBSERVATIONS_RECEIVED=0"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("missing %q in %q", expected, got)
		}
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "ACCESS_TOKEN") {
		t.Fatalf("secret leaked in %q", got)
	}
}
