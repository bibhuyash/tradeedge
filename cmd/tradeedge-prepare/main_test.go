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
	return options{repository: repository, credentialsFile: credentials, authCommand: "auth", validationCommand: "validation", now: time.Date(2026, 9, 18, 4, 30, 0, 0, time.UTC)}, credentials
}

func TestPrepareReadyAndRepeatedInvocation(t *testing.T) {
	value, credentials := preparationFixture(t)
	runner := &fakeRunner{}
	for attempt := 0; attempt < 2; attempt++ {
		var output bytes.Buffer
		if err := prepare(value, runner, &output); err != nil {
			t.Fatalf("attempt %d: %v", attempt+1, err)
		}
		for _, expected := range []string{"SESSION_PREPARATION=READY", "ACCESS_TOKEN=REUSED", "INSTRUMENTS=PASS", "MAPPINGS=PASS", "ZERODHA_PREFLIGHT=PASS", "PAPER=DISABLED", "LIVE=DISABLED", "REAL_BROKER_MUTATION=UNREACHABLE"} {
			if !strings.Contains(output.String(), expected+"\n") {
				t.Fatalf("missing %q in %q", expected, output.String())
			}
		}
	}
	raw, err := os.ReadFile(credentials)
	if err != nil || !strings.Contains(string(raw), "UNCHANGED=value\n") || !strings.Contains(string(raw), "TRADEEDGE_AUTHORIZATION_MANIFEST_HOST=.cache/market-validation/2026-09-18/authorization-aaaaaaa.json\n") {
		t.Fatalf("dotenv=%q err=%v", raw, err)
	}
}

func TestPrepareReturnsOneLoginInstruction(t *testing.T) {
	value, _ := preparationFixture(t)
	runner := &fakeRunner{failCommand: "auth authenticate"}
	var output bytes.Buffer
	err := prepare(value, runner, &output)
	if !errors.Is(err, errLoginRequired) || strings.Count(output.String(), "LOGIN_URL=") != 1 || !strings.Contains(output.String(), "REQUEST_TOKEN_DESTINATION=TRADEEDGE_ZERODHA_REQUEST_TOKEN in .env") {
		t.Fatalf("err=%v output=%q", err, output.String())
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "git ") {
			t.Fatalf("repository checks ran before login handoff: %q", call)
		}
	}
}

func TestPrepareDoesNotMisclassifyTransportFailureAsLoginRequired(t *testing.T) {
	value, _ := preparationFixture(t)
	runner := &fakeRunner{failCommand: "auth authenticate", authError: "NetworkError"}
	var output bytes.Buffer
	err := prepare(value, runner, &output)
	if err == nil || errors.Is(err, errLoginRequired) || strings.Contains(output.String(), "LOGIN_URL=") {
		t.Fatalf("transport failure was misclassified: err=%v output=%q", err, output.String())
	}
}

func TestPrepareFailuresAreFailClosedAndResume(t *testing.T) {
	for _, stage := range []string{"instrument-snapshot", "generate-calendar", "calendar-check", "generate-shadow-derivatives", "build-shadow-bundle", "telegram-check", "preflight", "build-shadow-authorization", "prepare-session"} {
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

func TestPrepareRejectsStaleProcessOverride(t *testing.T) {
	value, _ := preparationFixture(t)
	t.Setenv("TRADEEDGE_AUTHORIZATION_MANIFEST_HOST", "stale/session.json")
	var output bytes.Buffer
	if err := prepare(value, &fakeRunner{}, &output); err == nil || strings.Contains(output.String(), "SESSION_PREPARATION=READY") {
		t.Fatalf("stale override accepted: err=%v output=%q", err, output.String())
	}
}
