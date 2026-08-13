package app

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/config"
	"github.com/bibhuyash/tradeedge/internal/platform/checkpointfile"
	"github.com/bibhuyash/tradeedge/internal/platform/logging"
)

type shadowShutdownRecorder struct {
	operation    shutdownOperation
	publications atomic.Int32
	err          error
	state        *atomic.Int32
	seenState    atomic.Int32
}

func (r *shadowShutdownRecorder) Shutdown(context.Context) error {
	return r.operation.Run(func() error {
		r.publications.Add(1)
		if r.state != nil {
			r.seenState.Store(r.state.Load())
		}
		return r.err
	})
}

func TestProductionShadowNormalShutdownPublishesOnceAndExitsSuccessfully(t *testing.T) {
	recorder := &shadowShutdownRecorder{}
	if err := runShadowApplicationForTest(t, recorder); err != nil {
		t.Fatalf("shutdown error = %v", err)
	}
	if got := recorder.publications.Load(); got != 1 {
		t.Fatalf("checkpoint publications = %d, want 1", got)
	}
}

func TestProductionShadowEODThenShutdownPublishesFinalStateOnce(t *testing.T) {
	var eodState atomic.Int32
	eodState.Store(1) // EOD COMPLETED before process cancellation.
	recorder := &shadowShutdownRecorder{state: &eodState}
	if err := runShadowApplicationForTest(t, recorder); err != nil {
		t.Fatalf("shutdown error = %v", err)
	}
	if got := recorder.publications.Load(); got != 1 {
		t.Fatalf("checkpoint publications = %d, want 1", got)
	}
	if got := recorder.seenState.Load(); got != 1 {
		t.Fatalf("published EOD state = %d, want COMPLETED", got)
	}
}

func TestShadowShutdownRepeatedRequestsConverge(t *testing.T) {
	recorder := &shadowShutdownRecorder{}
	for range 5 {
		if err := recorder.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := recorder.publications.Load(); got != 1 {
		t.Fatalf("checkpoint publications = %d, want 1", got)
	}
}

func TestShadowShutdownConcurrentRequestsConverge(t *testing.T) {
	recorder := &shadowShutdownRecorder{}
	const callers = 32
	start := make(chan struct{})
	errorsByCaller := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsByCaller <- recorder.Shutdown(context.Background())
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByCaller)
	for err := range errorsByCaller {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := recorder.publications.Load(); got != 1 {
		t.Fatalf("checkpoint publications = %d, want 1", got)
	}
}

func TestShadowShutdownCheckpointFailuresRemainFailClosed(t *testing.T) {
	persistenceFailure := errors.New("checkpoint persistence failure")
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "revision conflict", err: checkpointfile.ErrConflict},
		{name: "persistence failure", err: persistenceFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &shadowShutdownRecorder{err: test.err}
			err := runShadowApplicationForTest(t, recorder)
			if !errors.Is(err, test.err) {
				t.Fatalf("shutdown error = %v, want %v", err, test.err)
			}
			if got := recorder.publications.Load(); got != 1 {
				t.Fatalf("checkpoint publications = %d, want 1", got)
			}
			if err = recorder.Shutdown(context.Background()); !errors.Is(err, test.err) {
				t.Fatalf("repeated shutdown error = %v, want original failure", err)
			}
		})
	}
}

func runShadowApplicationForTest(t *testing.T, runtime interface{ Shutdown(context.Context) error }) error {
	t.Helper()
	logger, err := logging.New("error", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Environment: "test", HTTPAddress: "127.0.0.1:0", LogLevel: "error",
		ShutdownTimeout: time.Second, TradingMode: config.ModeShadow,
		StrategyMaxConcurrency: 4, StrategyTimeout: 100 * time.Millisecond,
		RiskMaxConcurrency: 4, RiskTimeout: 100 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- runProductionShadowApplication(ctx, cfg, logger, Options{TradingRuntime: runtime})
	}()
	time.Sleep(25 * time.Millisecond)
	cancel()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("application shutdown timed out")
		return nil
	}
}
