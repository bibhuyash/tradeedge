package zerodha

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	brokertelemetry "github.com/bibhuyash/tradeedge/internal/broker/telemetry"
	marketmodel "github.com/bibhuyash/tradeedge/internal/marketdata/model"
)

type StreamState string

const (
	StreamStopped      StreamState = "STOPPED"
	StreamConnecting   StreamState = "CONNECTING"
	StreamConnected    StreamState = "CONNECTED"
	StreamReconnecting StreamState = "RECONNECTING"
	StreamExpired      StreamState = "EXPIRED"
	StreamExhausted    StreamState = "RECONNECT_EXHAUSTED"
)

type StreamSnapshot struct {
	State             StreamState `json:"state"`
	ReconnectAttempts int         `json:"reconnect_attempts"`
	Subscriptions     int         `json:"subscriptions"`
	Messages          uint64      `json:"messages"`
	LastMessageAt     time.Time   `json:"last_message_at,omitempty"`
	LastConnectedAt   time.Time   `json:"last_connected_at,omitempty"`
}

type StreamEnvelope struct {
	Quote  *marketmodel.QuoteEvent
	Order  *BrokerOrder
	Trades []BrokerTrade
}

type StreamConnection interface {
	Receive(context.Context) (StreamEnvelope, error)
	Close() error
}
type StreamConnector interface {
	Connect(context.Context, []string) (StreamConnection, error)
}
type StreamSink interface {
	ObserveQuote(context.Context, marketmodel.QuoteEvent) error
	ObserveOrder(context.Context, BrokerOrder, []BrokerTrade) error
}
type SleepFunc func(context.Context, time.Duration) error

type StreamConfig struct {
	MaxSubscriptions int
	MaxReconnects    int
	InitialBackoff   time.Duration
	MaximumBackoff   time.Duration
}

type StreamSupervisor struct {
	config    StreamConfig
	connector StreamConnector
	sink      StreamSink
	session   *SessionManager
	sleep     SleepFunc
	clock     Clock
	recorder  brokertelemetry.Recorder
	mu        sync.RWMutex
	snapshot  StreamSnapshot
	closed    bool
	running   bool
	cancel    context.CancelFunc
}

func NewStreamSupervisor(config StreamConfig, connector StreamConnector, sink StreamSink, session *SessionManager, sleep SleepFunc, clock Clock, recorder brokertelemetry.Recorder) (*StreamSupervisor, error) {
	if connector == nil || sink == nil || session == nil || config.MaxSubscriptions <= 0 || config.MaxSubscriptions > 3000 || config.MaxReconnects < 0 || config.MaxReconnects > 20 || config.InitialBackoff <= 0 || config.MaximumBackoff < config.InitialBackoff || config.MaximumBackoff > time.Minute {
		return nil, ErrInvalidConfiguration
	}
	if sleep == nil {
		sleep = func(ctx context.Context, duration time.Duration) error {
			timer := time.NewTimer(duration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &StreamSupervisor{config: config, connector: connector, sink: sink, session: session, sleep: sleep, clock: clock, recorder: brokertelemetry.Safe(recorder), snapshot: StreamSnapshot{State: StreamStopped}}, nil
}

func (supervisor *StreamSupervisor) Run(ctx context.Context, tokens []string) error {
	tokens = uniqueSorted(tokens)
	if len(tokens) == 0 || len(tokens) > supervisor.config.MaxSubscriptions {
		return ErrInvalidConfiguration
	}
	supervisor.mu.Lock()
	if supervisor.closed || supervisor.running {
		supervisor.mu.Unlock()
		return ErrStopped
	}
	runCtx, cancel := context.WithCancel(ctx)
	supervisor.running, supervisor.cancel = true, cancel
	supervisor.snapshot.Subscriptions = len(tokens)
	supervisor.mu.Unlock()
	defer func() {
		supervisor.mu.Lock()
		supervisor.running, supervisor.cancel = false, nil
		supervisor.mu.Unlock()
		cancel()
	}()
	backoff := supervisor.config.InitialBackoff
	for attempt := 0; attempt <= supervisor.config.MaxReconnects; attempt++ {
		if _, err := supervisor.session.Authorization(); err != nil {
			supervisor.setState(StreamExpired, attempt)
			return ErrSessionExpired
		}
		state := StreamConnecting
		if attempt > 0 {
			state = StreamReconnecting
		}
		supervisor.setState(state, attempt)
		connection, err := supervisor.connector.Connect(runCtx, tokens)
		if err == nil {
			supervisor.mu.Lock()
			supervisor.snapshot.State, supervisor.snapshot.LastConnectedAt = StreamConnected, supervisor.clock.Now()
			supervisor.snapshot.ReconnectAttempts = attempt
			supervisor.mu.Unlock()
			err = supervisor.consume(runCtx, connection)
			_ = connection.Close()
		}
		if runCtx.Err() != nil {
			supervisor.setState(StreamStopped, attempt)
			return runCtx.Err()
		}
		if errors.Is(err, ErrSessionExpired) {
			supervisor.session.Expire()
			supervisor.setState(StreamExpired, attempt)
			return err
		}
		if attempt == supervisor.config.MaxReconnects {
			supervisor.setState(StreamExhausted, attempt)
			supervisor.recorder.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationReconnect, Outcome: brokertelemetry.OutcomeFailure})
			return ErrUnavailable
		}
		supervisor.recorder.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationReconnect, Outcome: brokertelemetry.OutcomeDisconnected})
		if err := supervisor.sleep(runCtx, backoff); err != nil {
			supervisor.setState(StreamStopped, attempt)
			return err
		}
		backoff *= 2
		if backoff > supervisor.config.MaximumBackoff {
			backoff = supervisor.config.MaximumBackoff
		}
	}
	return ErrUnavailable
}

func (supervisor *StreamSupervisor) consume(ctx context.Context, connection StreamConnection) error {
	for {
		envelope, err := connection.Receive(ctx)
		if err != nil {
			return err
		}
		if envelope.Quote == nil && envelope.Order == nil {
			return ErrMalformedResponse
		}
		if envelope.Quote != nil {
			if err := supervisor.sink.ObserveQuote(ctx, *envelope.Quote); err != nil {
				return err
			}
		}
		if envelope.Order != nil {
			if err := supervisor.sink.ObserveOrder(ctx, *envelope.Order, envelope.Trades); err != nil {
				return err
			}
		}
		supervisor.mu.Lock()
		supervisor.snapshot.Messages++
		supervisor.snapshot.LastMessageAt = supervisor.clock.Now()
		supervisor.mu.Unlock()
		supervisor.recorder.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationStream, Outcome: brokertelemetry.OutcomeSuccess})
	}
}

func (supervisor *StreamSupervisor) setState(state StreamState, attempt int) {
	supervisor.mu.Lock()
	supervisor.snapshot.State, supervisor.snapshot.ReconnectAttempts = state, attempt
	supervisor.mu.Unlock()
}
func (supervisor *StreamSupervisor) Snapshot() StreamSnapshot {
	supervisor.mu.RLock()
	defer supervisor.mu.RUnlock()
	return supervisor.snapshot
}
func (supervisor *StreamSupervisor) Shutdown() {
	supervisor.mu.Lock()
	supervisor.closed = true
	supervisor.snapshot.State = StreamStopped
	cancel := supervisor.cancel
	supervisor.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
func uniqueSorted(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
