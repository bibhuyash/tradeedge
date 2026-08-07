package zerodha

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	zerodhaadapter "github.com/bibhuyash/tradeedge/internal/adapters/broker/zerodha"
	executionhealth "github.com/bibhuyash/tradeedge/internal/execution/health"
)

type connectivityFake struct {
	value   zerodhaadapter.ReadinessSnapshot
	stopped bool
}

func (fake *connectivityFake) Check(context.Context) zerodhaadapter.ReadinessSnapshot {
	return fake.value
}
func (fake *connectivityFake) Snapshot() zerodhaadapter.ReadinessSnapshot { return fake.value }
func (fake *connectivityFake) Shutdown()                                  { fake.stopped = true }

type streamFake struct {
	value   zerodhaadapter.StreamSnapshot
	stopped bool
}

func (fake *streamFake) Snapshot() zerodhaadapter.StreamSnapshot { return fake.value }
func (fake *streamFake) Shutdown()                               { fake.stopped = true }

type paperFake struct{}

func (paperFake) Health() executionhealth.PaperBroker {
	return executionhealth.PaperBroker{Available: true}
}

type reconcileFake struct{ blocked bool }

func (fake reconcileFake) Health() executionhealth.Reconciliation {
	return executionhealth.Reconciliation{Available: true, Blocked: fake.blocked}
}

type unknownFake struct {
	count int
	err   error
}

func (fake unknownFake) UnknownCount(context.Context) (int, error) { return fake.count, fake.err }

func runtimeDependencies() Dependencies {
	return Dependencies{Connectivity: &connectivityFake{value: zerodhaadapter.ReadinessSnapshot{State: zerodhaadapter.ReadinessReady, Session: zerodhaadapter.SessionAuthenticated, MappingVersion: "v1"}}, Stream: &streamFake{value: zerodhaadapter.StreamSnapshot{State: zerodhaadapter.StreamConnected}}, Paper: paperFake{}, Reconciliation: reconcileFake{}, Unknown: unknownFake{}, Clock: fixedRuntimeClock{}}
}

type fixedRuntimeClock struct{}

func (fixedRuntimeClock) Now() time.Time { return time.Date(2026, 8, 7, 4, 0, 0, 0, time.UTC) }

func TestRuntimeModesAreFailClosedAndNonMutating(t *testing.T) {
	for _, mode := range []Mode{ModeOffline, ModeLiveDisabled} {
		runtime, err := New(mode, Dependencies{Clock: fixedRuntimeClock{}})
		if err != nil {
			t.Fatal(err)
		}
		health := runtime.Health(context.Background())
		if health.MutationPermitted {
			t.Fatalf("%s permitted mutation", mode)
		}
		if mode == ModeLiveDisabled && health.State != StateBlocked {
			t.Fatalf("health=%#v", health)
		}
	}
	for _, mode := range []Mode{ModePaper, ModeShadow} {
		runtime, err := New(mode, runtimeDependencies())
		if err != nil {
			t.Fatal(err)
		}
		health := runtime.Health(context.Background())
		if health.State != StateReady || health.MutationPermitted {
			t.Fatalf("%s health=%#v", mode, health)
		}
	}
	if _, err := New(Mode("LIVE_ENABLED"), Dependencies{}); err == nil {
		t.Fatal("LIVE_ENABLED accepted")
	}
	if _, err := New(ModePaper, Dependencies{}); err == nil {
		t.Fatal("incomplete PAPER accepted")
	}
}

func TestRuntimeUnknownAndReconciliationRemainBlocked(t *testing.T) {
	dependencies := runtimeDependencies()
	dependencies.Unknown = unknownFake{count: 2}
	dependencies.Reconciliation = reconcileFake{blocked: true}
	runtime, _ := New(ModeShadow, dependencies)
	health := runtime.Health(context.Background())
	if health.State != StateBlocked || health.UnknownOrders != 2 || !health.ReconciliationBlocked {
		t.Fatalf("health=%#v", health)
	}
}

func TestOperationalAPIIsGETOnlyBoundedAndRedacted(t *testing.T) {
	runtime, _ := New(ModeOffline, Dependencies{Clock: fixedRuntimeClock{}})
	runtime.RecordError("session", "connect", true)
	handler := NewHandler(runtime, time.Second)
	for _, path := range []string{"health", "errors", "shadow"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/integrations/zerodha/"+path+"?limit=100", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		if strings.Contains(strings.ToLower(response.Body.String()), "token") {
			t.Fatalf("credential-shaped output: %s", response.Body.String())
		}
	}
	invalid := httptest.NewRecorder()
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodGet, "/api/v1/integrations/zerodha/errors?limit=101", nil))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status=%d", invalid.Code)
	}
	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/v1/integrations/zerodha/health", nil))
	if post.Code != http.StatusMethodNotAllowed || post.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST status=%d", post.Code)
	}
}

func TestOperationalAPIExposesBoundedUnknownAndReconciliationViews(t *testing.T) {
	runtime, _ := New(ModePaper, runtimeDependencies())
	handler := NewHandler(runtime, time.Second)
	for _, path := range []string{"unknown", "reconciliation"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/integrations/zerodha/"+path+"?limit=10", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestRuntimeShutdownIsIdempotent(t *testing.T) {
	dependencies := runtimeDependencies()
	runtime, _ := New(ModePaper, dependencies)
	_ = runtime.Shutdown(context.Background())
	_ = runtime.Shutdown(context.Background())
	if runtime.Health(context.Background()).State != StateStopped {
		t.Fatal("runtime not stopped")
	}
}
