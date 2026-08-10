package zerodha

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/marketdata"
)

type fakeMarketConnection struct {
	mu     sync.Mutex
	frames []MarketFrame
	writes []any
	closed bool
}

func (c *fakeMarketConnection) Read(ctx context.Context) (MarketFrame, error) {
	c.mu.Lock()
	if len(c.frames) > 0 {
		value := c.frames[0]
		c.frames = c.frames[1:]
		c.mu.Unlock()
		return value, nil
	}
	c.mu.Unlock()
	<-ctx.Done()
	return MarketFrame{}, ctx.Err()
}
func (c *fakeMarketConnection) WriteJSON(_ context.Context, value any) error {
	c.mu.Lock()
	c.writes = append(c.writes, value)
	c.mu.Unlock()
	return nil
}
func (c *fakeMarketConnection) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

type fakeMarketDialer struct {
	mu          sync.Mutex
	connections []*fakeMarketConnection
	calls       int
}

func (d *fakeMarketDialer) Dial(context.Context, string) (MarketConnection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.calls >= len(d.connections) {
		d.calls++
		return nil, ErrUnavailable
	}
	value := d.connections[d.calls]
	d.calls++
	return value, nil
}

func TestDecodeMarketFrameFullQuoteUsesIntegerMinorUnits(t *testing.T) {
	packet := make([]byte, 184)
	binary.BigEndian.PutUint32(packet[0:4], 256265)
	binary.BigEndian.PutUint32(packet[4:8], 2450125)
	binary.BigEndian.PutUint32(packet[16:20], 100)
	binary.BigEndian.PutUint32(packet[48:52], 5)
	binary.BigEndian.PutUint32(packet[60:64], uint32(time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC).Unix()))
	binary.BigEndian.PutUint32(packet[64:68], 10)
	binary.BigEndian.PutUint32(packet[68:72], 2450100)
	binary.BigEndian.PutUint32(packet[124:128], 12)
	binary.BigEndian.PutUint32(packet[128:132], 2450150)
	raw := wrapPackets(packet)
	values, err := DecodeMarketFrame(raw, time.Date(2026, 8, 10, 4, 0, 1, 0, time.UTC))
	if err != nil || len(values) != 1 {
		t.Fatalf("DecodeMarketFrame() = %#v, %v", values, err)
	}
	value := values[0]
	if value.ProviderToken != "256265" || value.LastMinor != 2450125 || value.BidMinor == nil || *value.BidMinor != 2450100 || value.AskMinor == nil || *value.AskMinor != 2450150 || value.Currency != "INR" {
		t.Fatalf("observation = %#v", value)
	}
}

func TestDecodeMarketFrameRejectsTruncationAndTrailingBytes(t *testing.T) {
	if _, err := DecodeMarketFrame([]byte{0, 1, 0, 32, 1}, time.Now()); !errors.Is(err, ErrMarketStreamMalformed) {
		t.Fatalf("truncated error = %v", err)
	}
	packet := make([]byte, 32)
	binary.BigEndian.PutUint32(packet[0:4], 1)
	binary.BigEndian.PutUint32(packet[4:8], 100)
	binary.BigEndian.PutUint32(packet[28:32], uint32(time.Now().Unix()))
	raw := append(wrapPackets(packet), 1)
	if _, err := DecodeMarketFrame(raw, time.Now()); !errors.Is(err, ErrMarketStreamMalformed) {
		t.Fatalf("trailing error = %v", err)
	}
}

func TestMarketStreamReconnectsAndResubscribes(t *testing.T) {
	now := time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC)
	credentials, err := (EnvCredentialSource{Lookup: func(key string) (string, bool) {
		values := map[string]string{"TRADEEDGE_ZERODHA_API_KEY": "key", "TRADEEDGE_ZERODHA_API_SECRET": "secret", "TRADEEDGE_ZERODHA_ACCESS_TOKEN": "access", "TRADEEDGE_ZERODHA_ACCESS_TOKEN_EXPIRES_AT": now.Add(time.Hour).Format(time.RFC3339)}
		value, ok := values[key]
		return value, ok
	}}).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clock := fixedClock{now}
	session := NewSessionManager(credentials, nil, &clock, nil)
	packet := make([]byte, 32)
	binary.BigEndian.PutUint32(packet[0:4], 256265)
	binary.BigEndian.PutUint32(packet[4:8], 2450125)
	binary.BigEndian.PutUint32(packet[28:32], uint32(now.Unix()))
	first := &fakeMarketConnection{frames: []MarketFrame{{Binary: true, Data: []byte{0}}, {Binary: true, Data: []byte{0, 0}}}}
	second := &fakeMarketConnection{frames: []MarketFrame{{Binary: true, Data: wrapPackets(packet)}}}
	dialer := &fakeMarketDialer{connections: []*fakeMarketConnection{first, second}}
	cfg := DefaultMarketStreamConfig()
	cfg.MaxReconnects, cfg.InitialBackoff, cfg.MaximumBackoff, cfg.LivenessTimeout = 2, time.Millisecond, time.Millisecond, 100*time.Millisecond
	stream, err := NewMarketStream(cfg, dialer, session, &clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	observed := make(chan marketdata.Observation, 1)
	done := make(chan error, 1)
	go func() {
		done <- stream.Stream(ctx, []string{"256265"}, func(_ context.Context, value marketdata.Observation) error {
			observed <- value
			cancel()
			return nil
		})
	}()
	select {
	case value := <-observed:
		if value.ProviderToken != "256265" {
			t.Fatalf("token = %s", value.ProviderToken)
		}
	case <-time.After(time.Second):
		t.Fatal("observation not delivered")
	}
	<-done
	if dialer.calls != 2 || len(first.writes) != 2 || len(second.writes) != 2 {
		t.Fatalf("calls=%d writes=%d/%d", dialer.calls, len(first.writes), len(second.writes))
	}
}

func TestMarketStreamBlocksUnexpectedOrderFrame(t *testing.T) {
	stream := &MarketStream{}
	if err := stream.observeText([]byte(`{"type":"order","data":{}}`)); !errors.Is(err, ErrUnexpectedOrderUpdate) {
		t.Fatalf("error = %v", err)
	}
	if stream.Snapshot().UnexpectedOrderFrames != 1 {
		t.Fatal("unexpected order frame not recorded")
	}
}

func wrapPackets(packets ...[]byte) []byte {
	length := 2
	for _, packet := range packets {
		length += 2 + len(packet)
	}
	raw := make([]byte, length)
	binary.BigEndian.PutUint16(raw[:2], uint16(len(packets)))
	offset := 2
	for _, packet := range packets {
		binary.BigEndian.PutUint16(raw[offset:offset+2], uint16(len(packet)))
		offset += 2
		copy(raw[offset:], packet)
		offset += len(packet)
	}
	return raw
}
