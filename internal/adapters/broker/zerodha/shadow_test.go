package zerodha

import (
	"context"
	"testing"

	"github.com/bibhuyash/tradeedge/internal/adapters/broker/paper"
	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
)

func TestShadowBrokerTranslatesButOnlyDelegatesToPaper(t *testing.T) {
	fixtureAdapter, submission, clock := executionFixture(t, &FakeOrderTransport{}, DisabledMutationGate{})
	observed := paper.NewObserved()
	shadow, err := NewShadowBroker(observed, fixtureAdapter.mapper, clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := shadow.Submit(context.Background(), submission)
	if err != nil || result.Status != executionbroker.SubmissionAccepted || result.BrokerOrderID == "" {
		t.Fatalf("Submit()=%#v,%v", result, err)
	}
	decisions := shadow.Decisions(10)
	if len(decisions) != 1 || decisions[0].Outcome != "translated" || len(decisions[0].RequestFingerprint) != 64 || decisions[0].MappingVersion == "" {
		t.Fatalf("decisions=%#v", decisions)
	}
	places, _ := fixtureAdapter.transport.(*FakeOrderTransport).Counts()
	if places != 0 {
		t.Fatalf("real mutation transport calls=%d", places)
	}
	restored, err := RestoreShadowBroker(observed, fixtureAdapter.mapper, clock, nil, shadow.Checkpoint())
	if err != nil || len(restored.Decisions(10)) != 1 {
		t.Fatalf("RestoreShadowBroker() = %#v, %v", restored, err)
	}
	shadow.Shutdown()
	if _, err := shadow.Submit(context.Background(), submission); err == nil {
		t.Fatal("Submit after shutdown succeeded")
	}
}
