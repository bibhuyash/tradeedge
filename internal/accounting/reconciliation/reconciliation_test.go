package reconciliation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	accountingcoordinator "github.com/bibhuyash/tradeedge/internal/accounting/coordinator"
	accountingmodel "github.com/bibhuyash/tradeedge/internal/accounting/model"
	"github.com/bibhuyash/tradeedge/internal/accounting/reconciliation"
	accountingfixture "github.com/bibhuyash/tradeedge/internal/accounting/testfixture"
	memory "github.com/bibhuyash/tradeedge/internal/adapters/accounting/memory"
	"github.com/bibhuyash/tradeedge/internal/domain"
)

func setup(t *testing.T, localQuantity int64) (*memory.Store, accountingmodel.AccountBinding, domain.InstrumentID, time.Time) {
	t.Helper()
	store := memory.NewDefault()
	at := accountingfixture.BaseTime
	if localQuantity != 0 {
		fill, _ := accountingfixture.Fill(1, domain.SideBuy, localQuantity, 100, at, at)
		runner, _ := accountingcoordinator.New(store, accountingcoordinator.DefaultConfig())
		if _, err := runner.ApplyFill(context.Background(), fill); err != nil {
			t.Fatal(err)
		}
	}
	probe, _ := accountingfixture.Fill(99, domain.SideBuy, 1, 100, at, at)
	account, _ := domain.NewAccountID("paper-account")
	binding, _ := accountingmodel.NewAccountBinding(probe.Spec().PortfolioID, account, "binding-v1", at.Add(-time.Hour))
	instrument, _ := domain.InstrumentIDFromCanonicalKey("accounting-instrument")
	return store, binding, instrument, at
}

func observation(t *testing.T, binding accountingmodel.AccountBinding, instrument domain.InstrumentID, quantity int64, at time.Time, available, complete bool, scope reconciliation.ObservationScope) reconciliation.BrokerPositionObservation {
	t.Helper()
	source, _ := accountingmodel.NewStateChecksum("broker-source/v1", []byte(time.Duration(quantity).String()+at.String()+string(scope)))
	value, err := reconciliation.NewBrokerPositionObservation(reconciliation.ObservationSpec{Provider: "paper", Binding: binding, InstrumentID: instrument, NetQuantity: quantity, BrokerObservedAt: at, IngestedAt: at.Add(time.Millisecond), SnapshotID: "snapshot-" + time.Duration(quantity).String(), SnapshotVersion: "v1", Complete: complete, Available: available, Scope: scope, SourceChecksum: source})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func reconcile(t *testing.T, local, broker int64, at time.Time) (reconciliation.Evidence, error) {
	t.Helper()
	store, binding, instrument, base := setup(t, local)
	repo, _ := memory.NewReconciliationStore(100)
	runner, _ := reconciliation.New(store, repo, reconciliation.StaticAccountBindings{binding.PortfolioID: binding}, reconciliation.DefaultConfig())
	obs := observation(t, binding, instrument, broker, base, true, true, reconciliation.ScopePaper)
	return runner.Reconcile(context.Background(), reconciliation.Request{Observation: obs, Mode: reconciliation.ModePaper, EvaluatedAt: at})
}

func TestClassifications(t *testing.T) {
	base := accountingfixture.BaseTime.Add(time.Second)
	cases := []struct {
		name          string
		local, broker int64
		want          reconciliation.Classification
		severity      reconciliation.Severity
	}{{"match", 5, 5, reconciliation.Match, reconciliation.SeverityHealthy}, {"quantity", 5, 3, reconciliation.QuantityMismatch, reconciliation.SeverityHigh}, {"direction", 5, -5, reconciliation.DirectionMismatch, reconciliation.SeverityHigh}, {"local", 5, 0, reconciliation.LocalOnly, reconciliation.SeverityHigh}, {"broker", 0, 5, reconciliation.BrokerOnly, reconciliation.SeverityCritical}}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got, err := reconcile(t, item.local, item.broker, base)
			if err != nil || got.Classification != item.want || got.Severity != item.severity {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

func TestStaleUnknownModesAndDeterminism(t *testing.T) {
	store, binding, instrument, at := setup(t, 2)
	repo, _ := memory.NewReconciliationStore(100)
	runner, _ := reconciliation.New(store, repo, reconciliation.StaticAccountBindings{binding.PortfolioID: binding}, reconciliation.DefaultConfig())
	stale := observation(t, binding, instrument, 2, at, true, true, reconciliation.ScopePaper)
	first, err := runner.Reconcile(context.Background(), reconciliation.Request{Observation: stale, Mode: reconciliation.ModePaper, EvaluatedAt: at.Add(time.Minute)})
	if err != nil || first.Classification != reconciliation.StaleBrokerObservation {
		t.Fatalf("stale=%+v err=%v", first, err)
	}
	again, err := runner.Reconcile(context.Background(), reconciliation.Request{Observation: stale, Mode: reconciliation.ModePaper, EvaluatedAt: at.Add(time.Minute)})
	if err != nil || string(first.CanonicalJSON()) != string(again.CanonicalJSON()) {
		t.Fatalf("duplicate changed err=%v", err)
	}
	checkpoint, checkpointErr := repo.Checkpoint(context.Background(), stale.ID)
	if checkpointErr != nil || checkpoint.EvidenceID != first.ID {
		t.Fatalf("checkpoint=%+v err=%v", checkpoint, checkpointErr)
	}
	restarted, _ := reconciliation.New(store, repo, reconciliation.StaticAccountBindings{binding.PortfolioID: binding}, reconciliation.DefaultConfig())
	replayed, replayErr := restarted.Reconcile(context.Background(), reconciliation.Request{Observation: stale, Mode: reconciliation.ModePaper, EvaluatedAt: at.Add(2 * time.Minute)})
	if replayErr != nil || string(replayed.CanonicalJSON()) != string(first.CanonicalJSON()) {
		t.Fatalf("restart changed evidence err=%v", replayErr)
	}
	repo2, _ := memory.NewReconciliationStore(100)
	runner2, _ := reconciliation.New(store, repo2, reconciliation.StaticAccountBindings{binding.PortfolioID: binding}, reconciliation.DefaultConfig())
	real := observation(t, binding, instrument, 2, at, true, true, reconciliation.ScopeReal)
	shadow, err := runner2.Reconcile(context.Background(), reconciliation.Request{Observation: real, Mode: reconciliation.ModeShadow, EvaluatedAt: at.Add(time.Second)})
	if err != nil || shadow.Classification != reconciliation.Unknown {
		t.Fatalf("shadow=%+v err=%v", shadow, err)
	}
}

func TestChangedObservationBytesAreRejected(t *testing.T) {
	store, binding, instrument, at := setup(t, 2)
	repo, _ := memory.NewReconciliationStore(10)
	runner, _ := reconciliation.New(store, repo, reconciliation.StaticAccountBindings{binding.PortfolioID: binding}, reconciliation.DefaultConfig())
	obs := observation(t, binding, instrument, 2, at, true, true, reconciliation.ScopePaper)
	obs.NetQuantity = 3
	if _, err := runner.Reconcile(context.Background(), reconciliation.Request{Observation: obs, Mode: reconciliation.ModePaper, EvaluatedAt: at.Add(time.Second)}); !errors.Is(err, reconciliation.ErrInvalidRequest) {
		t.Fatalf("changed observation err=%v", err)
	}
}

func TestUnavailableObservationFailsClosed(t *testing.T) {
	store, binding, instrument, at := setup(t, 2)
	repo, _ := memory.NewReconciliationStore(10)
	runner, _ := reconciliation.New(store, repo, reconciliation.StaticAccountBindings{binding.PortfolioID: binding}, reconciliation.DefaultConfig())
	obs := observation(t, binding, instrument, 0, at, false, false, reconciliation.ScopePaper)
	evidence, err := runner.Reconcile(context.Background(), reconciliation.Request{Observation: obs, Mode: reconciliation.ModePaper, EvaluatedAt: at.Add(time.Second)})
	if err != nil || evidence.Classification != reconciliation.Unknown || !evidence.Blocked {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fresh := observation(t, binding, instrument, 0, at.Add(time.Second), false, false, reconciliation.ScopePaper)
	if _, err = runner.Reconcile(ctx, reconciliation.Request{Observation: fresh, Mode: reconciliation.ModePaper, EvaluatedAt: at.Add(time.Second)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
}
