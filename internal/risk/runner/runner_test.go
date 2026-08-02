package runner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	runtimememory "github.com/bibhuyash/tradeedge/internal/adapters/riskruntime/memory"
	portfolioallocation "github.com/bibhuyash/tradeedge/internal/portfolio/allocation"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	riskstorage "github.com/bibhuyash/tradeedge/internal/risk/storage"
)

func TestCommittedDecisionOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		status      riskmodel.RuleResultStatus
		outcome     Outcome
		reservation bool
	}{
		{"approved", riskmodel.RulePass, OutcomeApproved, true},
		{"modified", riskmodel.RuleModificationRequired, OutcomeModified, true},
		{"rejected", riskmodel.RuleViolation, OutcomeRejected, false},
		{"deferred", riskmodel.RuleError, OutcomeDeferred, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerFixture(t, test.status, nil)
			receipt, err := fixture.runner.EvaluateProposal(context.Background(), fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.Outcome != test.outcome || receipt.CommittedRevision != 2 || receipt.DecisionID.IsZero() {
				t.Fatalf("receipt = %#v", receipt)
			}
			if receipt.ReservationID.IsZero() == test.reservation {
				t.Fatalf("reservation mismatch: %#v", receipt)
			}
			checkpoint, err := fixture.runtime.CurrentPortfolioCheckpoint(context.Background(), fixture.request.PortfolioID)
			if err != nil || checkpoint.Snapshot.Revision() != 2 {
				t.Fatalf("checkpoint = %#v, %v", checkpoint, err)
			}
		})
	}
}

func TestCommittedDuplicateIsIdempotent(t *testing.T) {
	fixture := newRunnerFixture(t, riskmodel.RulePass, nil)
	first, err := fixture.runner.EvaluateProposal(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.runner.EvaluateProposal(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcome != OutcomeDuplicateCommitted || second.DecisionID != first.DecisionID || second.SnapshotID != first.SnapshotID {
		t.Fatalf("duplicate = %#v, first = %#v", second, first)
	}
	checkpoint, _ := fixture.runtime.CurrentPortfolioCheckpoint(context.Background(), fixture.request.PortfolioID)
	if checkpoint.Snapshot.Revision() != 2 {
		t.Fatalf("duplicate advanced revision to %d", checkpoint.Snapshot.Revision())
	}
}

func TestInProgressDuplicateAndPortfolioBusy(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	fixture := newRunnerFixture(t, riskmodel.RulePass, func(rule *testRule) { rule.started, rule.release = started, release })
	result := make(chan error, 1)
	go func() {
		_, err := fixture.runner.EvaluateProposal(context.Background(), fixture.request)
		result <- err
	}()
	<-started
	receipt, err := fixture.runner.EvaluateProposal(context.Background(), fixture.request)
	if !errors.Is(err, ErrDuplicateInProgress) || receipt.Outcome != OutcomeDuplicateInProgress {
		t.Fatalf("duplicate = %#v, %v", receipt, err)
	}
	other := fixture.request
	other.LogicalTime = other.LogicalTime.Add(time.Nanosecond)
	receipt, err = fixture.runner.EvaluateProposal(context.Background(), other)
	if !errors.Is(err, ErrPortfolioBusy) || receipt.Outcome != OutcomePortfolioBusy {
		t.Fatalf("busy = %#v, %v", receipt, err)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestStaleRevisionDoesNotReevaluate(t *testing.T) {
	fixture := newRunnerFixture(t, riskmodel.RulePass, nil)
	if _, err := fixture.runner.EvaluateProposal(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	stale := fixture.request
	stale.LogicalTime = stale.LogicalTime.Add(time.Nanosecond)
	receipt, err := fixture.runner.EvaluateProposal(context.Background(), stale)
	if !errors.Is(err, riskstorage.ErrStaleRevision) || receipt.Outcome != OutcomeRevisionConflict {
		t.Fatalf("stale = %#v, %v", receipt, err)
	}
}

type failingAllocator struct{ panicValue any }

func (value failingAllocator) Evaluate(portfolioallocation.Input) (portfoliomodel.AllocationCandidate, error) {
	if value.panicValue != nil {
		panic(value.panicValue)
	}
	return portfoliomodel.AllocationCandidate{}, portfolioallocation.ErrAllocationFailed
}

func TestAllocationFailureAndPanicPublishNothing(t *testing.T) {
	for _, test := range []struct {
		name      string
		allocator Allocator
		outcome   Outcome
	}{
		{"failure", failingAllocator{}, OutcomeAllocationFailure}, {"panic", failingAllocator{panicValue: "boom"}, OutcomePanic},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerFixture(t, riskmodel.RulePass, nil)
			fixture.runner.allocator = test.allocator
			receipt, err := fixture.runner.EvaluateProposal(context.Background(), fixture.request)
			if err == nil || receipt.Outcome != test.outcome {
				t.Fatalf("receipt = %#v, err=%v", receipt, err)
			}
			checkpoint, _ := fixture.runtime.CurrentPortfolioCheckpoint(context.Background(), fixture.request.PortfolioID)
			if checkpoint.Snapshot.Revision() != 1 {
				t.Fatal("failed allocation published state")
			}
		})
	}
}

func TestInvalidRulePanicTimeoutAndCancellationPublishNothing(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testRule)
		prepare func(*runnerFixture) context.Context
		outcome Outcome
	}{
		{"invalid", func(rule *testRule) { rule.invalid = true }, nil, OutcomeInvalidOutput},
		{"panic", func(rule *testRule) { rule.panicValue = "rule boom" }, nil, OutcomePanic},
		{"timeout", func(rule *testRule) { rule.release = make(chan struct{}) }, func(f *runnerFixture) context.Context {
			f.runner.config.Timeout = time.Millisecond
			return context.Background()
		}, OutcomeTimedOut},
		{"cancel", func(rule *testRule) { rule.release = make(chan struct{}) }, func(*runnerFixture) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, OutcomeCancelled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunnerFixture(t, riskmodel.RulePass, test.mutate)
			ctx := context.Background()
			if test.prepare != nil {
				ctx = test.prepare(&fixture)
			}
			receipt, err := fixture.runner.EvaluateProposal(ctx, fixture.request)
			if err == nil || receipt.Outcome != test.outcome {
				t.Fatalf("receipt=%#v err=%v", receipt, err)
			}
			checkpoint, _ := fixture.runtime.CurrentPortfolioCheckpoint(context.Background(), fixture.request.PortfolioID)
			if checkpoint.Snapshot.Revision() != 1 {
				t.Fatal("failed rule published state")
			}
		})
	}
}

func TestAtomicRollbackAndRetry(t *testing.T) {
	fixture := newRunnerFixture(t, riskmodel.RulePass, nil)
	fixture.runtime.SetFailBeforeCommitForTest(true)
	receipt, err := fixture.runner.EvaluateProposal(context.Background(), fixture.request)
	if err == nil || receipt.Outcome != OutcomePublicationFailure {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
	if _, err := fixture.runtime.CommittedPublication(context.Background(), receipt.TriggerID); !errors.Is(err, riskstorage.ErrNotFound) {
		t.Fatal("partial publication visible")
	}
	checkpoint, _ := fixture.runtime.CurrentPortfolioCheckpoint(context.Background(), fixture.request.PortfolioID)
	if checkpoint.Snapshot.Revision() != 1 {
		t.Fatal("rollback failed")
	}
	fixture.runtime.SetFailBeforeCommitForTest(false)
	if receipt, err = fixture.runner.EvaluateProposal(context.Background(), fixture.request); err != nil || receipt.Outcome != OutcomeApproved {
		t.Fatalf("retry=%#v %v", receipt, err)
	}
}

func TestConcurrentWritersHaveOneCommit(t *testing.T) {
	fixture := newRunnerFixture(t, riskmodel.RulePass, nil)
	second, err := New(fixture.runner.deps, fixture.runner.allocator, fixture.runner.rules, fixture.runner.config)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	receipts := make(chan Receipt, 2)
	failures := make(chan error, 2)
	for _, value := range []*Runner{fixture.runner, second} {
		go func(r *Runner) {
			<-start
			receipt, err := r.EvaluateProposal(context.Background(), fixture.request)
			receipts <- receipt
			failures <- err
		}(value)
	}
	close(start)
	first, secondReceipt := <-receipts, <-receipts
	firstErr, secondErr := <-failures, <-failures
	if firstErr != nil || secondErr != nil {
		t.Fatalf("errors = %v, %v", firstErr, secondErr)
	}
	committed := 0
	duplicates := 0
	for _, value := range []Receipt{first, secondReceipt} {
		if value.Outcome == OutcomeApproved {
			committed++
		}
		if value.Outcome == OutcomeDuplicateCommitted {
			duplicates++
		}
	}
	if committed != 1 || duplicates != 1 {
		t.Fatalf("receipts = %#v, %#v", first, secondReceipt)
	}
}

func TestRuleExecutionOrder(t *testing.T) {
	fixture := newRunnerFixture(t, riskmodel.RulePass, nil)
	var order []string
	fixture.rule.record = func(value string) { order = append(order, value) }
	secondDescriptor := riskmodel.RiskRuleDescriptor{ID: "SECOND_RULE", Version: 1,
		Name: "second", Description: "second test rule", SchemaVersion: "risk-rule/v1"}
	secondHash, _ := riskmodel.NewRiskConfigurationHash([]byte(`{"limit_minor":2}`))
	secondConfiguration, err := riskmodel.NewRiskRuleConfiguration(riskmodel.RiskRuleConfiguration{
		Descriptor: secondDescriptor, Order: 2, Severity: riskmodel.SeverityBlocking,
		Effect: riskmodel.EffectNone, ConfigurationHash: secondHash,
		CanonicalJSON: []byte(`{"limit_minor":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	firstPolicy := fixture.runner.deps.Policies.(policySource).value
	policySpec := firstPolicy.Spec()
	policySpec.Rules = append(policySpec.Rules, secondConfiguration)
	policySpec.ID, _ = riskmodel.NewRiskPolicyID("ordered-policy")
	policySpec.ConfigurationHash, _ = riskmodel.NewRiskConfigurationHash([]byte(`{"ordered":true}`))
	orderedPolicy, err := riskmodel.NewRiskPolicy(policySpec)
	if err != nil {
		t.Fatal(err)
	}
	fixture.runner.deps.Policies = policySource{orderedPolicy}
	fixture.request.RiskPolicyID = orderedPolicy.ID()
	secondRule := &testRule{descriptor: secondDescriptor, status: riskmodel.RulePass,
		record: func(value string) { order = append(order, value) }}
	if err := fixture.runner.rules.Register(secondRule); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.runner.EvaluateProposal(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "TEST_RULE" || order[1] != "SECOND_RULE" {
		t.Fatalf("order = %#v", order)
	}
	receipt, _ := fixture.runtime.CommittedPublication(context.Background(), mustTrigger(t, fixture))
	decision, _ := fixture.runtime.Decision(context.Background(), receipt.DecisionID)
	if len(decision.Spec().RiskEvaluation.RuleResults()) != 2 ||
		decision.Spec().RiskEvaluation.RuleResults()[0].RuleID() != fixture.rule.descriptor.ID {
		t.Fatal("rule order/identity changed")
	}
}

func mustTrigger(t *testing.T, fixture runnerFixture) riskmodel.DecisionTriggerID {
	t.Helper()
	current, _ := fixture.runtime.PortfolioCheckpoint(context.Background(), fixture.request.PortfolioID, 2)
	return current.TriggerID
}

func TestShutdownAndResourceCleanup(t *testing.T) {
	fixture := newRunnerFixture(t, riskmodel.RulePass, nil)
	if _, err := fixture.runner.EvaluateProposal(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := fixture.runner.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	receipt, err := fixture.runner.EvaluateProposal(context.Background(), fixture.request)
	if err != nil || receipt.Outcome != OutcomeDuplicateCommitted {
		// Already committed duplicate lookup is intentionally safe after shutdown.
		t.Fatalf("post-shutdown duplicate = %#v, %v", receipt, err)
	}
}

func TestBoundedCrossPortfolioConcurrencyPrimitive(t *testing.T) {
	var inFlight, maximum atomic.Int32
	started, release := make(chan struct{}, 4), make(chan struct{})
	fixture := newRunnerFixture(t, riskmodel.RulePass, func(rule *testRule) {
		rule.started, rule.release, rule.inFlight, rule.maximum = started, release, &inFlight, &maximum
	})
	fixture.runner.semaphore = make(chan struct{}, 1)
	fixture.runner.config.MaxConcurrency = 1
	secondID, _ := portfoliomodel.NewPortfolioID("secondary")
	secondSpec := fixture.checkpoint.Snapshot.Spec()
	secondSpec.PortfolioID, secondSpec.Revision = secondID, 1
	secondSpec.SourceStateChecksum, _ = portfoliomodel.NewStateChecksum([]byte("secondary-genesis"))
	secondSnapshot, err := portfoliomodel.NewPortfolioSnapshot(secondSpec)
	if err != nil {
		t.Fatal(err)
	}
	secondCheckpoint, err := riskstorage.NewPortfolioCheckpoint(riskstorage.PortfolioCheckpoint{Snapshot: secondSnapshot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.runtime.InitializePortfolio(context.Background(), secondCheckpoint); err != nil {
		t.Fatal(err)
	}
	secondRequest := fixture.request
	secondRequest.PortfolioID = secondID
	done := make(chan error, 2)
	go func() { _, err := fixture.runner.EvaluateProposal(context.Background(), fixture.request); done <- err }()
	go func() { _, err := fixture.runner.EvaluateProposal(context.Background(), secondRequest); done <- err }()
	<-started
	time.Sleep(5 * time.Millisecond)
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrency = %d", maximum.Load())
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if maximum.Load() != 1 {
		t.Fatalf("bounded maximum = %d", maximum.Load())
	}
}

func TestCheckpointContinuationEquivalence(t *testing.T) {
	uninterrupted := newRunnerFixture(t, riskmodel.RulePass, nil)
	if _, err := uninterrupted.runner.EvaluateProposal(context.Background(), uninterrupted.request); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := uninterrupted.runtime.CurrentPortfolioCheckpoint(context.Background(), uninterrupted.request.PortfolioID)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := uninterrupted.request
	secondRequest.ExpectedRevision = 2
	secondRequest.LogicalTime = secondRequest.LogicalTime.Add(time.Nanosecond)
	uninterruptedReceipt, err := uninterrupted.runner.EvaluateProposal(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	uninterruptedFinal, _ := uninterrupted.runtime.CurrentPortfolioCheckpoint(context.Background(), uninterrupted.request.PortfolioID)

	restoredStore := runtimememory.NewStore()
	if _, err := restoredStore.RestorePortfolioCheckpoint(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	deps := uninterrupted.runner.deps
	deps.Runtime = restoredStore
	restoredRunner, err := New(deps, uninterrupted.runner.allocator, uninterrupted.runner.rules, uninterrupted.runner.config)
	if err != nil {
		t.Fatal(err)
	}
	restoredReceipt, err := restoredRunner.EvaluateProposal(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	restoredFinal, _ := restoredStore.CurrentPortfolioCheckpoint(context.Background(), uninterrupted.request.PortfolioID)
	if restoredReceipt.DecisionID != uninterruptedReceipt.DecisionID ||
		restoredReceipt.PublicationChecksum != uninterruptedReceipt.PublicationChecksum ||
		string(restoredFinal.Snapshot.CanonicalJSON()) != string(uninterruptedFinal.Snapshot.CanonicalJSON()) ||
		string(restoredFinal.CanonicalJSON()) != string(uninterruptedFinal.CanonicalJSON()) {
		t.Fatal("restored continuation diverged from uninterrupted evaluation")
	}
}

func TestFreshReplayIsByteIdentical(t *testing.T) {
	first := newRunnerFixture(t, riskmodel.RulePass, nil)
	second := newRunnerFixture(t, riskmodel.RulePass, nil)
	firstReceipt, err := first.runner.EvaluateProposal(context.Background(), first.request)
	if err != nil {
		t.Fatal(err)
	}
	secondReceipt, err := second.runner.EvaluateProposal(context.Background(), second.request)
	if err != nil {
		t.Fatal(err)
	}
	firstCheckpoint, _ := first.runtime.CurrentPortfolioCheckpoint(context.Background(), first.request.PortfolioID)
	secondCheckpoint, _ := second.runtime.CurrentPortfolioCheckpoint(context.Background(), second.request.PortfolioID)
	firstDecision, _ := first.runtime.Decision(context.Background(), firstReceipt.DecisionID)
	secondDecision, _ := second.runtime.Decision(context.Background(), secondReceipt.DecisionID)
	if firstReceipt != secondReceipt ||
		string(firstDecision.CanonicalJSON()) != string(secondDecision.CanonicalJSON()) ||
		string(firstCheckpoint.Snapshot.CanonicalJSON()) != string(secondCheckpoint.Snapshot.CanonicalJSON()) ||
		string(firstCheckpoint.CanonicalJSON()) != string(secondCheckpoint.CanonicalJSON()) {
		t.Fatal("fresh replay was not byte-identical")
	}
}

func TestRepositoryReturnsDefensiveCanonicalCopies(t *testing.T) {
	fixture := newRunnerFixture(t, riskmodel.RulePass, nil)
	receipt, err := fixture.runner.EvaluateProposal(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := fixture.runtime.CurrentPortfolioCheckpoint(context.Background(), fixture.request.PortfolioID)
	raw := checkpoint.CanonicalJSON()
	raw[0] = 'x'
	repeated, _ := fixture.runtime.CurrentPortfolioCheckpoint(context.Background(), fixture.request.PortfolioID)
	if repeated.CanonicalJSON()[0] == 'x' {
		t.Fatal("checkpoint canonical bytes are mutable")
	}
	decision, _ := fixture.runtime.Decision(context.Background(), receipt.DecisionID)
	decisionRaw := decision.CanonicalJSON()
	decisionRaw[0] = 'x'
	repeatedDecision, _ := fixture.runtime.Decision(context.Background(), receipt.DecisionID)
	if repeatedDecision.CanonicalJSON()[0] == 'x' {
		t.Fatal("decision canonical bytes are mutable")
	}
}

func TestControlActivationCommitsInsideAtomicPublication(t *testing.T) {
	fixture := newRunnerFixture(t, riskmodel.RuleViolation, func(rule *testRule) {
		rule.effect = riskmodel.EffectActivateKillSwitch
	})
	receipt, err := fixture.runner.EvaluateProposal(context.Background(), fixture.request)
	if err != nil || receipt.Outcome != OutcomeRejected {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	checkpoint, err := fixture.runtime.CurrentPortfolioCheckpoint(context.Background(), fixture.request.PortfolioID)
	if err != nil {
		t.Fatal(err)
	}
	controls := checkpoint.Snapshot.Spec().KillSwitches
	if len(controls) != 1 || controls[0].Spec().State != portfoliomodel.KillSwitchActive ||
		controls[0].Spec().StateRevision != 2 || controls[0].Spec().ActivationEvidence.IsZero() {
		t.Fatalf("kill-switch state was not atomically activated: %+v", controls)
	}
}
