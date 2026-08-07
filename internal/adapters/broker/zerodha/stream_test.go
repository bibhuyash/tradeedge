package zerodha

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

type streamConnectionFake struct {
	mu     sync.Mutex
	values []StreamEnvelope
	err    error
	at     int
}

func (fake *streamConnectionFake) Receive(ctx context.Context) (StreamEnvelope, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.at < len(fake.values) {
		value := fake.values[fake.at]
		fake.at++
		return value, nil
	}
	if fake.err != nil {
		return StreamEnvelope{}, fake.err
	}
	<-ctx.Done()
	return StreamEnvelope{}, ctx.Err()
}
func (*streamConnectionFake) Close() error { return nil }

type streamConnectorFake struct {
	mu          sync.Mutex
	connections []StreamConnection
	calls       [][]string
}

func (fake *streamConnectorFake) Connect(_ context.Context, tokens []string) (StreamConnection, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.calls = append(fake.calls, append([]string(nil), tokens...))
	if len(fake.connections) == 0 {
		return nil, ErrUnavailable
	}
	value := fake.connections[0]
	fake.connections = fake.connections[1:]
	return value, nil
}

type streamSinkFake struct {
	mu     sync.Mutex
	quotes int
	orders int
}

func (sink *streamSinkFake) ObserveQuote(context.Context, marketmodel.QuoteEvent) error {
	sink.mu.Lock()
	sink.quotes++
	sink.mu.Unlock()
	return nil
}
func (sink *streamSinkFake) ObserveOrder(context.Context, BrokerOrder, []BrokerTrade) error {
	sink.mu.Lock()
	sink.orders++
	sink.mu.Unlock()
	return nil
}

func TestStreamSupervisorReconnectsAndResubscribesBoundedly(t *testing.T) {
	now := time.Date(2026, 8, 7, 4, 0, 0, 0, time.UTC)
	session, _ := authenticatedSession(now)
	connector := &streamConnectorFake{connections: []StreamConnection{&streamConnectionFake{values: []StreamEnvelope{{Order: &BrokerOrder{BrokerOrderID: "ignored"}}}, err: errors.New("disconnect")}, &streamConnectionFake{err: context.Canceled}}}
	sink := &streamSinkFake{}
	sleeps := 0
	supervisor, err := NewStreamSupervisor(StreamConfig{MaxSubscriptions: 10, MaxReconnects: 1, InitialBackoff: time.Millisecond, MaximumBackoff: time.Millisecond}, connector, sink, session, func(context.Context, time.Duration) error { sleeps++; return nil }, &fixedClock{now: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = supervisor.Run(context.Background(), []string{"2", "1", "2"})
	if !errors.Is(err, ErrUnavailable) && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error=%v", err)
	}
	if len(connector.calls) != 2 || len(connector.calls[0]) != 2 || connector.calls[0][0] != "1" || sleeps != 1 {
		t.Fatalf("calls=%#v sleeps=%d", connector.calls, sleeps)
	}
	if supervisor.Snapshot().ReconnectAttempts != 1 {
		t.Fatalf("snapshot=%#v", supervisor.Snapshot())
	}
}

func TestStreamSupervisorSessionExpiryStopsReconnect(t *testing.T) {
	now := time.Date(2026, 8, 7, 4, 0, 0, 0, time.UTC)
	session, _ := authenticatedSession(now)
	session.Expire()
	supervisor, _ := NewStreamSupervisor(StreamConfig{MaxSubscriptions: 1, MaxReconnects: 2, InitialBackoff: time.Millisecond, MaximumBackoff: time.Millisecond}, &streamConnectorFake{}, &streamSinkFake{}, session, nil, &fixedClock{now: now}, nil)
	if err := supervisor.Run(context.Background(), []string{"1"}); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("Run() error=%v", err)
	}
	if supervisor.Snapshot().State != StreamExpired {
		t.Fatalf("snapshot=%#v", supervisor.Snapshot())
	}
}

func TestStreamSupervisorShutdownCancelsActiveConnection(t *testing.T) {
	now := time.Date(2026, 8, 7, 4, 0, 0, 0, time.UTC)
	session, _ := authenticatedSession(now)
	connector := &streamConnectorFake{connections: []StreamConnection{&streamConnectionFake{}}}
	supervisor, _ := NewStreamSupervisor(StreamConfig{MaxSubscriptions: 1, MaxReconnects: 1, InitialBackoff: time.Millisecond, MaximumBackoff: time.Millisecond}, connector, &streamSinkFake{}, session, nil, &fixedClock{now: now}, nil)
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(context.Background(), []string{"1"}) }()
	deadline := time.After(time.Second)
	for supervisor.Snapshot().State != StreamConnected {
		select {
		case <-deadline:
			t.Fatal("stream did not connect")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	supervisor.Shutdown()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
}
