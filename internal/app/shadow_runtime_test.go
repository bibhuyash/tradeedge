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
	"github.com/bibhuyash/tradeedge/internal/shadowruntime"
)

type shadowShutdownRecorder struct {
	operation    shutdownOperation
	publications atomic.Int32
	err          error
	state        *atomic.Int32
	seenState    atomic.Int32
}

type shadowCheckpointStoreRecorder struct {
	mu       sync.Mutex
	actual   uint64
	expected []uint64
	conflict bool
	entered  chan struct{}
	release  chan struct{}
}

func (s *shadowCheckpointStoreRecorder) Publish(_ context.Context, _ shadowruntime.Snapshot, expected uint64, _ string, _ string, _ time.Time, _ bool) (checkpointfile.Generation, error) {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
	}
	if s.release != nil {
		<-s.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expected = append(s.expected, expected)
	if s.conflict || expected != s.actual {
		return checkpointfile.Generation{}, checkpointfile.ErrConflict
	}
	s.actual++
	return checkpointfile.Generation{Sequence: s.actual}, nil
}

func checkpointSnapshotForTest() shadowruntime.Snapshot {
	return shadowruntime.Snapshot{SchemaVersion: shadowruntime.SchemaVersion, Revision: 1, Checksum: "test"}
}

func TestShadowCheckpointPublisherRestartsAtLoadedSequence(t *testing.T) {
	store := &shadowCheckpointStoreRecorder{actual: 41}
	publisher := newShadowCheckpointPublisher(store, checkpointSnapshotForTest, 41, "calendar", "configuration")
	publisher.Dirty()
	if err := publisher.Publish(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if publisher.sequence != 42 || len(store.expected) != 1 || store.expected[0] != 41 {
		t.Fatalf("sequence=%d expected=%v, want sequence 42 from expected 41", publisher.sequence, store.expected)
	}
}

func TestShadowCheckpointPublisherSerializesConcurrentRequests(t *testing.T) {
	store := &shadowCheckpointStoreRecorder{}
	publisher := newShadowCheckpointPublisher(store, checkpointSnapshotForTest, 0, "calendar", "configuration")
	const callers = 16
	start := make(chan struct{})
	errorsByCaller := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsByCaller <- publisher.Publish(context.Background(), true)
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
	for index, expected := range store.expected {
		if expected != uint64(index) {
			t.Fatalf("publication %d used expected=%d", index, expected)
		}
	}
}

func TestShadowCheckpointPublisherCoalescesMarketTicks(t *testing.T) {
	store := &shadowCheckpointStoreRecorder{}
	publisher := newShadowCheckpointPublisher(store, checkpointSnapshotForTest, 0, "calendar", "configuration")
	for range 10_000 {
		publisher.Dirty()
	}
	if err := publisher.Publish(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(store.expected) != 1 || publisher.sequence != 1 {
		t.Fatalf("publications=%d sequence=%d, want one publication at sequence 1", len(store.expected), publisher.sequence)
	}
}

func TestShadowCheckpointPublisherExternalConflictFailsClosed(t *testing.T) {
	store := &shadowCheckpointStoreRecorder{actual: 2, conflict: true}
	publisher := newShadowCheckpointPublisher(store, checkpointSnapshotForTest, 2, "calendar", "configuration")
	publisher.Dirty()
	if err := publisher.Publish(context.Background(), false); !errors.Is(err, checkpointfile.ErrConflict) {
		t.Fatalf("error=%v, want checkpoint conflict", err)
	}
	if publisher.sequence != 2 {
		t.Fatalf("sequence advanced to %d after conflict", publisher.sequence)
	}
}

func TestShadowCheckpointPersistenceDoesNotBlockMarketDirtyPath(t *testing.T) {
	store := &shadowCheckpointStoreRecorder{entered: make(chan struct{}, 1), release: make(chan struct{})}
	publisher := newShadowCheckpointPublisher(store, checkpointSnapshotForTest, 0, "calendar", "configuration")
	publisher.Dirty()
	done := make(chan error, 1)
	go func() { done <- publisher.Publish(context.Background(), false) }()
	<-store.entered
	marked := make(chan struct{})
	go func() {
		publisher.Dirty()
		close(marked)
	}()
	select {
	case <-marked:
	case <-time.After(time.Second):
		t.Fatal("market dirty path blocked on checkpoint persistence")
	}
	close(store.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if publisher.mutations.Load() == publisher.published {
		t.Fatal("mutation arriving during publication was lost")
	}
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
