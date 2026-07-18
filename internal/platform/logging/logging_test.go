package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewProducesStructuredJSONAtConfiguredLevel(t *testing.T) {
	var output bytes.Buffer
	logger, err := New("info", &output)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	logger.Debug("hidden")
	logger.Info("started", "component", "test")

	if strings.Contains(output.String(), "hidden") {
		t.Fatal("debug log was emitted at info level")
	}
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("log is not JSON: %v", err)
	}
	if record["msg"] != "started" || record["component"] != "test" {
		t.Fatalf("unexpected record: %#v", record)
	}
	if strings.Contains(strings.ToLower(output.String()), "token") {
		t.Fatalf("unexpected secret-bearing field in output: %s", output.String())
	}
}

func TestNewRejectsUnknownLevel(t *testing.T) {
	if _, err := New("trace", &bytes.Buffer{}); err == nil {
		t.Fatal("New() error = nil, want error")
	}
}
