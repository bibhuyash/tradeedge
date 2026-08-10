package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type eodRunner struct {
	calls atomic.Int64
	err   error
}

func (r *eodRunner) RunEOD(context.Context) error { r.calls.Add(1); return r.err }

func TestStopNewExposureIsDurableIdempotentAndOneWay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controls.json")
	controller, err := New(path, nil, func() time.Time { return time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	first, err := controller.StopNewExposure(context.Background(), "REQ-1", "OPERATOR_REQUEST")
	if err != nil || !first.NewExposureBlocked || first.Revision != 1 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := controller.StopNewExposure(context.Background(), "REQ-1", "OPERATOR_REQUEST")
	if err != nil || second.Revision != first.Revision {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	if _, err := controller.StopNewExposure(context.Background(), "REQ-1", "DIFFERENT"); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict=%v", err)
	}
	restored, err := New(path, nil, nil)
	if err != nil || !restored.Snapshot().NewExposureBlocked {
		t.Fatalf("restored=%#v err=%v", restored, err)
	}
	controls, _ := restored.Controls(context.Background())
	if !controls.NewExposureBlocked || controls.GlobalBlocked {
		t.Fatalf("controls=%#v", controls)
	}
}

func TestEODHandlerRunsOnlyApprovedSequence(t *testing.T) {
	runner := &eodRunner{}
	controller, err := New(filepath.Join(t.TempDir(), "controls.json"), runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(Handler{Controller: controller})
	defer server.Close()
	response, err := http.Post(server.URL+"/v1/eod-close", "application/json", strings.NewReader(`{"request_id":"EOD-1","reason":"SESSION_CLOSE"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d", response.StatusCode)
	}
	deadline := time.Now().Add(time.Second)
	for controller.Snapshot().EOD != EODCompleted && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runner.calls.Load() != 1 || controller.Snapshot().EOD != EODCompleted {
		t.Fatalf("calls=%d snapshot=%#v", runner.calls.Load(), controller.Snapshot())
	}
	var body map[string]any
	_ = json.NewDecoder(response.Body).Decode(&body)
	if _, ok := body["commands"]; !ok {
		t.Fatal("audit commands missing")
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/buy", strings.NewReader(`{}`))
	response, _ = http.DefaultClient.Do(request)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("buy status=%d", response.StatusCode)
	}
}

func TestCorruptControlStateFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controls.json")
	controller, _ := New(path, nil, nil)
	_, _ = controller.StopNewExposure(context.Background(), "REQ-1", "TEST")
	if err := os.WriteFile(path, []byte(`{"schema_version":"bad"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, nil, nil); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("error=%v", err)
	}
}
