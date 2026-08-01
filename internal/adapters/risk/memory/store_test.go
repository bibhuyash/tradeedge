package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
	riskmodel "github.com/bibhuyash/tradeedge/internal/risk/model"
	riskstorage "github.com/bibhuyash/tradeedge/internal/risk/storage"
	"github.com/bibhuyash/tradeedge/internal/risk/testfixture"
)

func TestDecisionRepositoryIdempotencyOrderingAndCancellation(t *testing.T) {
	store := NewStoreWithLimits(Limits{Policies: 1, Decisions: 1, Controls: 2})
	decision, err := testfixture.ApprovedDecision()
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := store.AppendDecision(context.Background(), decision)
	if err != nil || outcome.Status != riskstorage.RegistrationCommitted {
		t.Fatalf("append = %#v, %v", outcome, err)
	}
	outcome, err = store.AppendDecision(context.Background(), decision)
	if err != nil || outcome.Status != riskstorage.RegistrationIdempotent {
		t.Fatalf("idempotent = %#v, %v", outcome, err)
	}
	modified, err := testfixture.ModifiedDecision()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendDecision(context.Background(), modified); !errors.Is(err, riskstorage.ErrIdentityCollision) {
		t.Fatalf("mismatched proposal/revision error = %v", err)
	} else {
		var collision *riskstorage.IdentityCollisionError
		if !errors.As(err, &collision) {
			t.Fatalf("collision does not support errors.As: %v", err)
		}
	}
	stored, err := store.DecisionByProposal(context.Background(), decision.ProposalID(),
		decision.Spec().ExpectedPortfolioRevision)
	if err != nil || stored.ID() != decision.ID() {
		t.Fatalf("stored decision = %#v, %v", stored, err)
	}
	values, err := store.Decisions(context.Background(), decision.Spec().PortfolioID)
	if err != nil || len(values) != 1 || values[0].ID() != decision.ID() {
		t.Fatalf("decisions = %#v, %v", values, err)
	}
	if _, err := store.Decision(context.Background(),
		riskmodel.PortfolioRiskDecisionID{}); !errors.Is(err, riskstorage.ErrNotFound) {
		t.Fatalf("not found error = %v", err)
	}
	raw := stored.CanonicalJSON()
	raw[0] = 'x'
	again, _ := store.Decision(context.Background(), stored.ID())
	if again.CanonicalJSON()[0] == 'x' {
		t.Fatal("returned decision mutated repository state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.AppendDecision(ctx, decision); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestControlRepositoryRevisionAndTypedNotFound(t *testing.T) {
	store := NewStore()
	if _, err := store.KillSwitchState(context.Background(),
		portfoliomodel.KillSwitchID{}, 1); !errors.Is(err, riskstorage.ErrNotFound) {
		t.Fatalf("kill switch error = %v", err)
	}

	id, _ := portfoliomodel.NewKillSwitchID("portfolio", "primary")
	configurationID, _ := portfoliomodel.NewPortfolioConfigurationID("configuration", "one")
	configurationHash, _ := portfoliomodel.NewConfigurationHash([]byte(`{"version":1}`))
	evidence, _ := portfoliomodel.NewStateChecksum([]byte(`{"triggered":true}`))
	activatedAt := time.Date(2026, time.July, 18, 9, 30, 0, 0, time.UTC)
	first, err := portfoliomodel.NewKillSwitch(portfoliomodel.KillSwitchSpec{
		ID: id, Scope: portfoliomodel.ScopePortfolio, ScopeSubject: "primary",
		State: portfoliomodel.KillSwitchActive, ReasonCode: "LOSS_LIMIT_REACHED",
		ActivationEvidence: evidence, ActivatedAt: activatedAt,
		ConfigurationID: configurationID, ConfigurationHash: configurationHash,
		StateRevision: 1, SchemaVersion: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := store.RegisterKillSwitchState(context.Background(), first)
	if err != nil || outcome.Status != riskstorage.RegistrationCommitted {
		t.Fatalf("register first state = %#v, %v", outcome, err)
	}
	outcome, err = store.RegisterKillSwitchState(context.Background(), first)
	if err != nil || outcome.Status != riskstorage.RegistrationIdempotent {
		t.Fatalf("replay first state = %#v, %v", outcome, err)
	}
	secondSpec := first.Spec()
	secondSpec.State = portfoliomodel.KillSwitchRecoveryPending
	secondSpec.StateRevision = 2
	second, err := portfoliomodel.NewKillSwitch(secondSpec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterKillSwitchState(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	states, err := store.KillSwitchStates(context.Background(), id)
	if err != nil || len(states) != 2 ||
		states[0].Spec().StateRevision != 1 || states[1].Spec().StateRevision != 2 {
		t.Fatalf("states = %#v, %v", states, err)
	}
	storedFirst, err := store.KillSwitchState(context.Background(), id, 1)
	if err != nil || storedFirst.Spec().State != portfoliomodel.KillSwitchActive {
		t.Fatalf("first revision was overwritten: %#v, %v", storedFirst, err)
	}
}
