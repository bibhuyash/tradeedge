package zerodha

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

const validProfile = `{"status":"success","data":{"exchanges":["NFO"],"products":["NRML"],"order_types":["LIMIT"]}}`
const validInstruments = "instrument_token,exchange_token,tradingsymbol,name,last_price,expiry,strike,tick_size,lot_size,instrument_type,segment,exchange\n123,45,NIFTY26AUG25000CE,,0,2026-08-27,25000,0.05,65,CE,NFO-OPT,NFO\n"

func TestParseInstrumentDumpAllowsObservationOnlyIndexMetadata(t *testing.T) {
	dump := "instrument_token,exchange_token,tradingsymbol,name,last_price,expiry,strike,tick_size,lot_size,instrument_type,segment,exchange\n256265,1001,NIFTY 50,NIFTY 50,0,,0,0,0,EQ,INDICES,NSE\n"
	records, err := ParseInstrumentDump([]byte(dump))
	if err != nil || len(records) != 1 || records[0].LotSize != 0 || records[0].TickSize != "0" {
		t.Fatalf("ParseInstrumentDump() = %#v, %v", records, err)
	}
}

func authenticatedSession(now time.Time) (*SessionManager, *fixedClock) {
	clock := &fixedClock{now: now}
	return NewSessionManager(CredentialMaterial{apiKey: "key", apiSecret: "secret", accessToken: "access", expiresAt: now.Add(time.Hour)}, nil, clock, nil), clock
}

func TestDeterministicFakeTransportClientAndMalformedResponses(t *testing.T) {
	now := time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	session, clock := authenticatedSession(now)
	fake := NewFakeTransport([]FakeRead{{Body: []byte(validProfile), Status: 200}, {Body: []byte("{"), Status: 200}}, []FakeRead{{Body: []byte(validInstruments), Status: 200}})
	client, err := NewClient(fake, session, clock, nil)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	capabilities, err := client.Capabilities(context.Background())
	if err != nil || len(capabilities.Exchanges) != 1 || capabilities.Exchanges[0] != "NFO" {
		t.Fatalf("Capabilities() = %#v, %v", capabilities, err)
	}
	instruments, err := client.Instruments(context.Background())
	if err != nil || len(instruments) != 1 || instruments[0].Token != "123" {
		t.Fatalf("Instruments() = %#v, %v", instruments, err)
	}
	if _, err = client.Capabilities(context.Background()); !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("malformed Capabilities() error = %v", err)
	}
	client.Shutdown()
	if _, err = client.Instruments(context.Background()); !errors.Is(err, ErrStopped) {
		t.Fatalf("Instruments() after shutdown error = %v", err)
	}
}

func TestFakeTransportCancellationAndConcurrentReads(t *testing.T) {
	now := time.Date(2026, 8, 2, 3, 0, 0, 0, time.UTC)
	session, clock := authenticatedSession(now)
	reads := make([]FakeRead, 32)
	for index := range reads {
		reads[index] = FakeRead{Body: []byte(validProfile), Status: 200}
	}
	client, _ := NewClient(NewFakeTransport(reads, nil), session, clock, nil)
	var wait sync.WaitGroup
	errorsSeen := make(chan error, len(reads))
	for range reads {
		wait.Add(1)
		go func() { defer wait.Done(); _, err := client.Capabilities(context.Background()); errorsSeen <- err }()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent Capabilities() error = %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Capabilities(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Capabilities() error = %v", err)
	}
}
