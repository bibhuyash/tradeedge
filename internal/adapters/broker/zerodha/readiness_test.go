package zerodha

import (
	"context"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	"github.com/bibhuyash/tradeedge/internal/instrumentmaster"
)

func TestConnectivityReadinessAndShutdownAreReadOnly(t *testing.T) {
	now := time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	instrument := optionInstrument(t, 2026, time.August, 27)
	master, _ := instrumentmaster.New(now, []domain.Instrument{instrument}, []domain.ProviderInstrumentRef{{Provider: Provider, Token: "123", TradingSymbol: "NIFTY", InstrumentID: instrument.ID(), ValidFrom: now, ValidUntil: now.Add(24 * time.Hour)}})
	mapper, _ := NewMapper(master, 12*time.Hour, &fixedClock{now: now}, nil)
	session, clock := authenticatedSession(now)
	client, _ := NewClient(NewFakeTransport([]FakeRead{{Body: []byte(validProfile), Status: 200}}, nil), session, clock, nil)
	connectivity, err := NewConnectivity(true, client, session, mapper, clock, nil)
	if err != nil {
		t.Fatalf("NewConnectivity() error = %v", err)
	}
	snapshot := connectivity.Check(context.Background())
	if snapshot.State != ReadinessReady || !snapshot.ReadOnly || snapshot.OrderMutation {
		t.Fatalf("Check() = %#v", snapshot)
	}
	connectivity.Shutdown()
	if connectivity.Snapshot().State != ReadinessStopped || connectivity.Snapshot().OrderMutation {
		t.Fatalf("Snapshot() after shutdown = %#v", connectivity.Snapshot())
	}
	disabled, _ := NewConnectivity(false, nil, nil, nil, clock, nil)
	if disabled.Check(context.Background()).State != ReadinessDisabled {
		t.Fatalf("disabled state = %s", disabled.Snapshot().State)
	}
}
