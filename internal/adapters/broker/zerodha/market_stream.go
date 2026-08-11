package zerodha

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	brokertelemetry "github.com/bibhuyash/tradeedge/internal/broker/telemetry"
	"github.com/bibhuyash/tradeedge/internal/marketdata"
	"github.com/gorilla/websocket"
)

const defaultWebSocketURL = "wss://ws.kite.trade"

var (
	ErrMarketStreamMalformed = errors.New("malformed zerodha market stream frame")
	ErrMarketStreamStale     = errors.New("zerodha market stream is stale")
	ErrMarketStreamOverflow  = errors.New("zerodha market stream buffer overflow")
	ErrUnexpectedOrderUpdate = errors.New("unexpected zerodha order update on read-only stream")
)

type MarketStreamConfig struct {
	URL              string
	MaxSubscriptions int
	BufferCapacity   int
	MaxReconnects    int
	InitialBackoff   time.Duration
	MaximumBackoff   time.Duration
	LivenessTimeout  time.Duration
}

func DefaultMarketStreamConfig() MarketStreamConfig {
	return MarketStreamConfig{URL: defaultWebSocketURL, MaxSubscriptions: 250, BufferCapacity: 1024, MaxReconnects: 5, InitialBackoff: 250 * time.Millisecond, MaximumBackoff: 5 * time.Second, LivenessTimeout: 10 * time.Second}
}

func (c MarketStreamConfig) Validate() error {
	parsed, err := url.Parse(c.URL)
	if err != nil || parsed.Scheme != "wss" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || c.MaxSubscriptions < 1 || c.MaxSubscriptions > 3000 || c.BufferCapacity < 1 || c.BufferCapacity > 65536 || c.MaxReconnects < 0 || c.MaxReconnects > 20 || c.InitialBackoff <= 0 || c.MaximumBackoff < c.InitialBackoff || c.MaximumBackoff > time.Minute || c.LivenessTimeout <= 0 || c.LivenessTimeout > time.Minute {
		return ErrInvalidConfiguration
	}
	return nil
}

type MarketStreamSnapshot struct {
	State                 StreamState `json:"state"`
	HandshakeEstablished  bool        `json:"handshake_established"`
	SubscribeSent         bool        `json:"subscribe_sent"`
	Subscriptions         int         `json:"subscriptions"`
	ReconnectAttempts     int         `json:"reconnect_attempts"`
	Frames                uint64      `json:"frames"`
	BinaryFrames          uint64      `json:"binary_frames"`
	Heartbeats            uint64      `json:"heartbeats"`
	Packets               uint64      `json:"packets"`
	IndexPackets          uint64      `json:"index_packets"`
	PacketsDecoded        uint64      `json:"packets_decoded"`
	PacketsRejected       uint64      `json:"packets_rejected"`
	TokenMatches          uint64      `json:"token_matches"`
	Observations          uint64      `json:"observations"`
	UnexpectedOrderFrames uint64      `json:"unexpected_order_frames"`
	LastFrameAt           time.Time   `json:"last_frame_at,omitempty"`
	LastObservationAt     time.Time   `json:"last_observation_at,omitempty"`
	LastError             string      `json:"last_error,omitempty"`
}

type MarketFrame struct {
	Binary bool
	Data   []byte
}

type MarketConnection interface {
	Read(context.Context) (MarketFrame, error)
	WriteJSON(context.Context, any) error
	Close() error
}

type MarketDialer interface {
	Dial(context.Context, string) (MarketConnection, error)
}

type websocketMarketDialer struct{ dialer *websocket.Dialer }

func NewWebSocketMarketDialer() MarketDialer {
	return websocketMarketDialer{dialer: &websocket.Dialer{HandshakeTimeout: 5 * time.Second, EnableCompression: false}}
}

func (d websocketMarketDialer) Dial(ctx context.Context, endpoint string) (MarketConnection, error) {
	conn, response, err := d.dialer.DialContext(ctx, endpoint, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	conn.SetReadLimit(1 << 20)
	return &gorillaMarketConnection{conn: conn}, nil
}

type gorillaMarketConnection struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (c *gorillaMarketConnection) Read(ctx context.Context) (MarketFrame, error) {
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetReadDeadline(time.Now()) })
	defer stop()
	kind, data, err := c.conn.ReadMessage()
	if err != nil {
		if ctx.Err() != nil {
			return MarketFrame{}, ctx.Err()
		}
		return MarketFrame{}, ErrUnavailable
	}
	return MarketFrame{Binary: kind == websocket.BinaryMessage, Data: append([]byte(nil), data...)}, nil
}

func (c *gorillaMarketConnection) WriteJSON(ctx context.Context, value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetWriteDeadline(deadline)
	}
	if err := c.conn.WriteJSON(value); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (c *gorillaMarketConnection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
	return c.conn.Close()
}

type MarketStream struct {
	config   MarketStreamConfig
	dialer   MarketDialer
	session  *SessionManager
	clock    Clock
	recorder brokertelemetry.Recorder
	mu       sync.RWMutex
	snapshot MarketStreamSnapshot
	cancel   context.CancelFunc
	running  bool
	closed   bool
	position int64
}

func NewMarketStream(config MarketStreamConfig, dialer MarketDialer, session *SessionManager, clock Clock, recorder brokertelemetry.Recorder) (*MarketStream, error) {
	if config.Validate() != nil || dialer == nil || session == nil {
		return nil, ErrInvalidConfiguration
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &MarketStream{config: config, dialer: dialer, session: session, clock: clock, recorder: brokertelemetry.Safe(recorder), snapshot: MarketStreamSnapshot{State: StreamStopped}}, nil
}

func (s *MarketStream) Stream(ctx context.Context, tokens []string, sink marketdata.ObservationSink) error {
	if sink == nil {
		return ErrInvalidConfiguration
	}
	tokens, numeric, err := normalizeTokens(tokens, s.config.MaxSubscriptions)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.running || s.closed {
		s.mu.Unlock()
		return ErrStopped
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.running, s.cancel = true, cancel
	s.snapshot.Subscriptions = len(tokens)
	s.mu.Unlock()
	expectedTokens := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		expectedTokens[token] = struct{}{}
	}
	defer func() {
		cancel()
		s.mu.Lock()
		s.running, s.cancel, s.snapshot.State = false, nil, StreamStopped
		s.mu.Unlock()
	}()

	backoff := s.config.InitialBackoff
	for attempt := 0; attempt <= s.config.MaxReconnects; attempt++ {
		endpoint, endpointErr := s.endpoint()
		if endpointErr != nil {
			s.setFailure(StreamExpired, attempt, endpointErr)
			return endpointErr
		}
		state := StreamConnecting
		if attempt > 0 {
			state = StreamReconnecting
		}
		s.setState(state, attempt)
		connection, connectErr := s.dialer.Dial(runCtx, endpoint)
		if connectErr == nil {
			s.mu.Lock()
			s.snapshot.HandshakeEstablished = true
			s.mu.Unlock()
		}
		if connectErr == nil {
			connectErr = subscribe(runCtx, connection, numeric)
			if connectErr == nil {
				s.mu.Lock()
				s.snapshot.SubscribeSent = true
				s.mu.Unlock()
			}
		}
		if connectErr == nil {
			s.setState(StreamConnected, attempt)
			connectErr = s.consume(runCtx, connection, expectedTokens, sink)
		}
		if connection != nil {
			_ = connection.Close()
		}
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		if errors.Is(connectErr, ErrSessionExpired) {
			s.session.Expire()
			s.setFailure(StreamExpired, attempt, connectErr)
			return connectErr
		}
		if attempt == s.config.MaxReconnects {
			s.setFailure(StreamExhausted, attempt, connectErr)
			return errors.Join(ErrUnavailable, connectErr)
		}
		s.recorder.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationReconnect, Outcome: brokertelemetry.OutcomeDisconnected})
		if err := sleepContext(runCtx, backoff); err != nil {
			return err
		}
		backoff *= 2
		if backoff > s.config.MaximumBackoff {
			backoff = s.config.MaximumBackoff
		}
	}
	return ErrUnavailable
}

func (s *MarketStream) endpoint() (string, error) {
	s.session.mu.Lock()
	defer s.session.mu.Unlock()
	if s.session.state != SessionAuthenticated || !s.session.expiresAt.After(s.clock.Now()) || s.session.credentials.apiKey == "" || s.session.accessToken == "" {
		return "", ErrSessionExpired
	}
	parsed, _ := url.Parse(s.config.URL)
	query := parsed.Query()
	query.Set("api_key", s.session.credentials.apiKey)
	query.Set("access_token", s.session.accessToken)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func subscribe(ctx context.Context, connection MarketConnection, tokens []uint32) error {
	if err := connection.WriteJSON(ctx, map[string]any{"a": "subscribe", "v": tokens}); err != nil {
		return err
	}
	return connection.WriteJSON(ctx, map[string]any{"a": "mode", "v": []any{"full", tokens}})
}

func (s *MarketStream) consume(ctx context.Context, connection MarketConnection, expectedTokens map[string]struct{}, sink marketdata.ObservationSink) error {
	frames := make(chan MarketFrame, s.config.BufferCapacity)
	errorsCh := make(chan error, 1)
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		for {
			frame, err := readWithLiveness(readCtx, connection, s.config.LivenessTimeout)
			if err != nil {
				errorsCh <- err
				return
			}
			select {
			case frames <- frame:
			case <-readCtx.Done():
				return
			default:
				errorsCh <- ErrMarketStreamOverflow
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errorsCh:
			return err
		case frame := <-frames:
			now := s.clock.Now().UTC()
			s.mu.Lock()
			s.snapshot.Frames++
			s.snapshot.LastFrameAt = now
			s.mu.Unlock()
			if frame.Binary && len(frame.Data) == 1 {
				s.mu.Lock()
				s.snapshot.Heartbeats++
				s.mu.Unlock()
				continue
			}
			if !frame.Binary {
				if err := s.observeText(frame.Data); err != nil {
					return err
				}
				continue
			}
			s.mu.Lock()
			s.snapshot.BinaryFrames++
			s.mu.Unlock()
			observations, stats, err := decodeMarketFrame(frame.Data, now)
			s.mu.Lock()
			s.snapshot.Packets += stats.packets
			s.snapshot.IndexPackets += stats.indexPackets
			s.snapshot.PacketsDecoded += stats.decoded
			s.snapshot.PacketsRejected += stats.rejected
			s.mu.Unlock()
			if err != nil {
				return err
			}
			for _, observation := range observations {
				if _, matched := expectedTokens[observation.ProviderToken]; matched {
					s.mu.Lock()
					s.snapshot.TokenMatches++
					s.mu.Unlock()
				}
				if observation.ExchangeTime.IsZero() {
					continue
				}
				s.mu.Lock()
				s.position++
				observation.SourcePosition = s.position
				s.mu.Unlock()
				if err := sink(ctx, observation); err != nil {
					return err
				}
				s.mu.Lock()
				s.snapshot.Observations++
				s.snapshot.LastObservationAt = now
				s.mu.Unlock()
			}
		}
	}
}

func readWithLiveness(ctx context.Context, connection MarketConnection, timeout time.Duration) (MarketFrame, error) {
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	frame, err := connection.Read(readCtx)
	if errors.Is(err, context.DeadlineExceeded) {
		return MarketFrame{}, ErrMarketStreamStale
	}
	return frame, err
}

func (s *MarketStream) observeText(raw []byte) error {
	var envelope struct {
		Type string `json:"type"`
	}
	if len(raw) > 64<<10 || json.Unmarshal(raw, &envelope) != nil {
		return ErrMarketStreamMalformed
	}
	if envelope.Type == "order" {
		s.mu.Lock()
		s.snapshot.UnexpectedOrderFrames++
		s.mu.Unlock()
		return ErrUnexpectedOrderUpdate
	}
	if envelope.Type == "error" {
		return ErrUnavailable
	}
	if envelope.Type != "message" {
		return ErrMarketStreamMalformed
	}
	return nil
}

type marketFrameDecodeStats struct {
	packets      uint64
	indexPackets uint64
	decoded      uint64
	rejected     uint64
}

func DecodeMarketFrame(raw []byte, ingestedAt time.Time) ([]marketdata.Observation, error) {
	observations, _, err := decodeMarketFrame(raw, ingestedAt)
	return observations, err
}

func decodeMarketFrame(raw []byte, ingestedAt time.Time) ([]marketdata.Observation, marketFrameDecodeStats, error) {
	stats := marketFrameDecodeStats{}
	if len(raw) < 2 || ingestedAt.IsZero() {
		stats.rejected = 1
		return nil, stats, ErrMarketStreamMalformed
	}
	count := int(binary.BigEndian.Uint16(raw[:2]))
	if count < 1 || count > 3000 {
		stats.rejected = 1
		return nil, stats, ErrMarketStreamMalformed
	}
	stats.packets = uint64(count)
	offset := 2
	result := make([]marketdata.Observation, 0, count)
	for index := 0; index < count; index++ {
		if offset+2 > len(raw) {
			stats.rejected++
			return nil, stats, ErrMarketStreamMalformed
		}
		length := int(binary.BigEndian.Uint16(raw[offset : offset+2]))
		offset += 2
		if offset+length > len(raw) {
			stats.rejected++
			return nil, stats, ErrMarketStreamMalformed
		}
		packet := raw[offset : offset+length]
		if length == 28 || length == 32 {
			stats.indexPackets++
		}
		observation, err := decodeMarketPacket(packet, ingestedAt.UTC())
		if err != nil {
			stats.rejected++
			return nil, stats, err
		}
		stats.decoded++
		result = append(result, observation)
		offset += length
	}
	if offset != len(raw) {
		stats.rejected++
		return nil, stats, ErrMarketStreamMalformed
	}
	return result, stats, nil
}

func decodeMarketPacket(packet []byte, ingestedAt time.Time) (marketdata.Observation, error) {
	if len(packet) != 8 && len(packet) != 28 && len(packet) != 32 && len(packet) != 44 && len(packet) != 184 {
		return marketdata.Observation{}, ErrMarketStreamMalformed
	}
	token := binary.BigEndian.Uint32(packet[0:4])
	if token == 0 {
		return marketdata.Observation{}, ErrMarketStreamMalformed
	}
	observation := marketdata.Observation{Kind: marketdata.ObservationQuote, Provider: Provider, ProviderToken: strconv.FormatUint(uint64(token), 10), IngestedAt: ingestedAt, Currency: "INR"}
	switch len(packet) {
	case 28, 32:
		observation.LastMinor = int64(int32(binary.BigEndian.Uint32(packet[4:8])))
		observation.HighMinor = int64(int32(binary.BigEndian.Uint32(packet[8:12])))
		observation.LowMinor = int64(int32(binary.BigEndian.Uint32(packet[12:16])))
		observation.OpenMinor = int64(int32(binary.BigEndian.Uint32(packet[16:20])))
		observation.CloseMinor = int64(int32(binary.BigEndian.Uint32(packet[20:24])))
		if len(packet) == 32 {
			observation.ExchangeTime = unixTime(packet[28:32])
		}
	case 8:
		observation.LastMinor = int64(int32(binary.BigEndian.Uint32(packet[4:8])))
	case 44, 184:
		observation.LastMinor = int64(int32(binary.BigEndian.Uint32(packet[4:8])))
		observation.OpenMinor = int64(int32(binary.BigEndian.Uint32(packet[28:32])))
		observation.HighMinor = int64(int32(binary.BigEndian.Uint32(packet[32:36])))
		observation.LowMinor = int64(int32(binary.BigEndian.Uint32(packet[36:40])))
		observation.CloseMinor = int64(int32(binary.BigEndian.Uint32(packet[40:44])))
		observation.Volume = int64(int32(binary.BigEndian.Uint32(packet[16:20])))
		if len(packet) == 184 {
			oi := int64(int32(binary.BigEndian.Uint32(packet[48:52])))
			observation.OpenInterest = &oi
			observation.ExchangeTime = unixTime(packet[60:64])
			bidPrice := int64(int32(binary.BigEndian.Uint32(packet[68:72])))
			bidQuantity := int64(int32(binary.BigEndian.Uint32(packet[64:68])))
			askPrice := int64(int32(binary.BigEndian.Uint32(packet[128:132])))
			askQuantity := int64(int32(binary.BigEndian.Uint32(packet[124:128])))
			if bidPrice > 0 && bidQuantity > 0 {
				observation.BidMinor, observation.BidQuantity = &bidPrice, bidQuantity
			}
			if askPrice > 0 && askQuantity > 0 {
				observation.AskMinor, observation.AskQuantity = &askPrice, askQuantity
			}
		}
	}
	if observation.LastMinor <= 0 {
		return marketdata.Observation{}, ErrMarketStreamMalformed
	}
	return observation, nil
}

func unixTime(raw []byte) time.Time {
	seconds := int64(binary.BigEndian.Uint32(raw))
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

func normalizeTokens(values []string, maximum int) ([]string, []uint32, error) {
	seen := map[uint32]struct{}{}
	for _, value := range values {
		parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
		if err != nil || parsed == 0 {
			return nil, nil, ErrInvalidConfiguration
		}
		seen[uint32(parsed)] = struct{}{}
	}
	if len(seen) == 0 || len(seen) > maximum {
		return nil, nil, ErrInvalidConfiguration
	}
	numeric := make([]uint32, 0, len(seen))
	for value := range seen {
		numeric = append(numeric, value)
	}
	sort.Slice(numeric, func(i, j int) bool { return numeric[i] < numeric[j] })
	result := make([]string, len(numeric))
	for index, value := range numeric {
		result[index] = strconv.FormatUint(uint64(value), 10)
	}
	return result, numeric, nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *MarketStream) setState(state StreamState, attempt int) {
	s.mu.Lock()
	s.snapshot.State, s.snapshot.ReconnectAttempts, s.snapshot.LastError = state, attempt, ""
	s.mu.Unlock()
}

func (s *MarketStream) setFailure(state StreamState, attempt int, err error) {
	s.mu.Lock()
	s.snapshot.State, s.snapshot.ReconnectAttempts = state, attempt
	if err != nil {
		s.snapshot.LastError = boundedMarketError(err)
	}
	s.mu.Unlock()
}

func boundedMarketError(err error) string {
	switch {
	case errors.Is(err, ErrMarketStreamMalformed):
		return "MALFORMED_FRAME"
	case errors.Is(err, ErrMarketStreamStale):
		return "STALE_CONNECTION"
	case errors.Is(err, ErrMarketStreamOverflow):
		return "BUFFER_OVERFLOW"
	case errors.Is(err, ErrUnexpectedOrderUpdate):
		return "UNEXPECTED_ORDER_UPDATE"
	case errors.Is(err, ErrSessionExpired):
		return "SESSION_EXPIRED"
	default:
		return "UNAVAILABLE"
	}
}

func (s *MarketStream) Snapshot() MarketStreamSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *MarketStream) Shutdown() {
	s.mu.Lock()
	s.closed = true
	cancel := s.cancel
	s.snapshot.State = StreamStopped
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s MarketStreamSnapshot) String() string {
	return fmt.Sprintf("market-stream state=%s subscriptions=%d observations=%d", s.State, s.Subscriptions, s.Observations)
}
