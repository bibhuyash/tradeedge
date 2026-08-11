package zerodha

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/bibhuyash/tradeedge/internal/marketdata"
)

type fakeMarketConnection struct {
	mu                     sync.Mutex
	frames                 []MarketFrame
	readError              error
	writes                 []any
	closed                 bool
	activeReaders          int
	maxReaders             int
	readBeforeSubscription bool
}

func (c *fakeMarketConnection) Read(ctx context.Context) (MarketFrame, error) {
	c.mu.Lock()
	c.activeReaders++
	if len(c.writes) < 2 {
		c.readBeforeSubscription = true
	}
	if c.activeReaders > c.maxReaders {
		c.maxReaders = c.activeReaders
	}
	if len(c.frames) > 0 {
		value := c.frames[0]
		c.frames = c.frames[1:]
		c.activeReaders--
		c.mu.Unlock()
		return value, nil
	}
	err := c.readError
	if err != nil {
		c.activeReaders--
		c.mu.Unlock()
		return MarketFrame{}, err
	}
	c.mu.Unlock()
	<-ctx.Done()
	c.mu.Lock()
	c.activeReaders--
	c.mu.Unlock()
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

func TestDecodeMarketFrameSupportsOfficialPacketLengths(t *testing.T) {
	ingested := time.Date(2026, 8, 10, 4, 0, 1, 0, time.UTC)
	exchange := ingested.Add(-time.Second)
	for name, testCase := range map[string]struct {
		packet       []byte
		index        uint64
		exchangeTime time.Time
		open         int64
		high         int64
		low          int64
		close        int64
	}{
		"8 byte ltp":    {packet: ltpPacket(256265, 2450125)},
		"28 byte index": {packet: indexPacket(256265, 2450125, 2460000, 2440000, 2445000, 2455000, time.Time{}), index: 1, open: 2445000, high: 2460000, low: 2440000, close: 2455000},
		"32 byte index": {packet: indexPacket(256265, 2450125, 2460000, 2440000, 2445000, 2455000, exchange), index: 1, exchangeTime: exchange, open: 2445000, high: 2460000, low: 2440000, close: 2455000},
		"44 byte quote": {packet: quotePacket(408065, 150025, 149000, 151000, 148500, 149500), open: 149000, high: 151000, low: 148500, close: 149500},
	} {
		t.Run(name, func(t *testing.T) {
			observations, stats, err := decodeMarketFrame(wrapPackets(testCase.packet), ingested)
			if err != nil || len(observations) != 1 {
				t.Fatalf("decode = %#v, stats=%#v, err=%v", observations, stats, err)
			}
			value := observations[0]
			if value.LastMinor != 2450125 && name != "44 byte quote" {
				t.Fatalf("last minor = %d", value.LastMinor)
			}
			if name == "44 byte quote" && value.LastMinor != 150025 {
				t.Fatalf("last minor = %d", value.LastMinor)
			}
			if value.OpenMinor != testCase.open || value.HighMinor != testCase.high || value.LowMinor != testCase.low || value.CloseMinor != testCase.close || !value.ExchangeTime.Equal(testCase.exchangeTime) {
				t.Fatalf("observation = %#v", value)
			}
			if stats.packets != 1 || stats.indexPackets != testCase.index || stats.decoded != 1 || stats.rejected != 0 {
				t.Fatalf("stats = %#v", stats)
			}
		})
	}
}

func TestDecodeMarketFrameSupportsMultiPacketEnvelope(t *testing.T) {
	ingested := time.Date(2026, 8, 10, 4, 0, 1, 0, time.UTC)
	raw := wrapPackets(
		ltpPacket(408065, 150025),
		indexPacket(256265, 2450125, 2460000, 2440000, 2445000, 2455000, time.Time{}),
		indexPacket(260105, 5500125, 5510000, 5490000, 5495000, 5505000, ingested.Add(-time.Second)),
	)
	observations, stats, err := decodeMarketFrame(raw, ingested)
	if err != nil || len(observations) != 3 {
		t.Fatalf("decode=%#v stats=%#v err=%v", observations, stats, err)
	}
	if stats.packets != 3 || stats.indexPackets != 2 || stats.decoded != 3 || stats.rejected != 0 {
		t.Fatalf("stats=%#v", stats)
	}
	if observations[0].ProviderToken != "408065" || observations[1].ProviderToken != "256265" || observations[2].ProviderToken != "260105" {
		t.Fatalf("tokens=%#v", observations)
	}
}

func TestMaximumMessageSizeAllowsLargestKiteEnvelope(t *testing.T) {
	const maximumKiteEnvelope = 2 + 3000*(2+184)
	if maximumKiteEnvelope >= maxWebSocketMessageBytes {
		t.Fatalf("Kite envelope %d exceeds read limit %d", maximumKiteEnvelope, maxWebSocketMessageBytes)
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
	wantSubscribe := map[string]any{"a": "subscribe", "v": []uint32{256265}}
	wantMode := map[string]any{"a": "mode", "v": []any{"full", []uint32{256265}}}
	if !reflect.DeepEqual(second.writes[0], wantSubscribe) || !reflect.DeepEqual(second.writes[1], wantMode) {
		t.Fatalf("subscription messages = %#v", second.writes)
	}
	snapshot := stream.Snapshot()
	if !snapshot.HandshakeEstablished || !snapshot.SubscribeSent || snapshot.Heartbeats != 1 || snapshot.BinaryFrames != 3 || snapshot.Packets != 1 || snapshot.IndexPackets != 1 || snapshot.PacketsDecoded != 1 || snapshot.PacketsRejected != 1 || snapshot.TokenMatches != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if first.maxReaders != 1 || second.maxReaders != 1 || first.readBeforeSubscription || second.readBeforeSubscription {
		t.Fatalf("read sequencing readers=%d/%d before-subscribe=%t/%t", first.maxReaders, second.maxReaders, first.readBeforeSubscription, second.readBeforeSubscription)
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

func TestMarketStreamRejectsUnknownTextWithoutCallingBinaryDecoder(t *testing.T) {
	stream := &MarketStream{}
	if err := stream.observeText([]byte(`{"unexpected":true}`)); !errors.Is(err, ErrUnexpectedTextMessage) {
		t.Fatalf("error=%v", err)
	}
}

func TestMarketStreamClassifiesControlAndCloseFrames(t *testing.T) {
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
	connection := &fakeMarketConnection{frames: []MarketFrame{
		{MessageType: MarketMessagePing},
		{MessageType: MarketMessagePong},
		{MessageType: MarketMessageClose, CloseCode: 1000},
	}}
	dialer := &fakeMarketDialer{connections: []*fakeMarketConnection{connection}}
	cfg := DefaultMarketStreamConfig()
	cfg.MaxReconnects = 0
	stream, err := NewMarketStream(cfg, dialer, session, &clock, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = stream.Stream(context.Background(), []string{"256265"}, func(context.Context, marketdata.Observation) error { return nil })
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stream error=%v", err)
	}
	snapshot := stream.Snapshot()
	if len(snapshot.FrameDiagnostics) != 3 {
		t.Fatalf("diagnostics=%#v", snapshot.FrameDiagnostics)
	}
	for index, messageType := range []MarketMessageType{MarketMessagePing, MarketMessagePong, MarketMessageClose} {
		diagnostic := snapshot.FrameDiagnostics[index]
		if diagnostic.Sequence != uint64(index+1) || diagnostic.MessageType != messageType || diagnostic.Classification != FrameControl {
			t.Fatalf("diagnostic[%d]=%#v", index, diagnostic)
		}
	}
	if snapshot.FrameDiagnostics[2].CloseCode != 1000 || connection.maxReaders != 1 {
		t.Fatalf("close/read diagnostics=%#v readers=%d", snapshot.FrameDiagnostics[2], connection.maxReaders)
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

func ltpPacket(token uint32, last int32) []byte {
	packet := make([]byte, 8)
	binary.BigEndian.PutUint32(packet[0:4], token)
	binary.BigEndian.PutUint32(packet[4:8], uint32(last))
	return packet
}

func indexPacket(token uint32, last, high, low, open, close int32, exchange time.Time) []byte {
	length := 28
	if !exchange.IsZero() {
		length = 32
	}
	packet := make([]byte, length)
	binary.BigEndian.PutUint32(packet[0:4], token)
	binary.BigEndian.PutUint32(packet[4:8], uint32(last))
	binary.BigEndian.PutUint32(packet[8:12], uint32(high))
	binary.BigEndian.PutUint32(packet[12:16], uint32(low))
	binary.BigEndian.PutUint32(packet[16:20], uint32(open))
	binary.BigEndian.PutUint32(packet[20:24], uint32(close))
	if length == 32 {
		binary.BigEndian.PutUint32(packet[28:32], uint32(exchange.Unix()))
	}
	return packet
}

func quotePacket(token uint32, last, open, high, low, close int32) []byte {
	packet := make([]byte, 44)
	binary.BigEndian.PutUint32(packet[0:4], token)
	binary.BigEndian.PutUint32(packet[4:8], uint32(last))
	binary.BigEndian.PutUint32(packet[28:32], uint32(open))
	binary.BigEndian.PutUint32(packet[32:36], uint32(high))
	binary.BigEndian.PutUint32(packet[36:40], uint32(low))
	binary.BigEndian.PutUint32(packet[40:44], uint32(close))
	return packet
}
