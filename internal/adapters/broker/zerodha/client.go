package zerodha

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"

	brokertelemetry "github.com/bibhuyash/tradeedge/internal/broker/telemetry"
)

type CapabilitySnapshot struct {
	Exchanges  []string `json:"exchanges"`
	Products   []string `json:"products"`
	OrderTypes []string `json:"order_types"`
}

type InstrumentRecord struct {
	Token          string
	ExchangeToken  string
	TradingSymbol  string
	Expiry         string
	Strike         string
	TickSize       string
	LotSize        int64
	InstrumentType string
	Segment        string
	Exchange       string
}

type Client struct {
	transport ReadTransport
	session   *SessionManager
	telemetry brokertelemetry.Recorder
	clock     Clock
	mu        sync.RWMutex
	stopped   bool
}

func NewClient(transport ReadTransport, session *SessionManager, clock Clock, recorder brokertelemetry.Recorder) (*Client, error) {
	if transport == nil || session == nil {
		return nil, ErrInvalidConfiguration
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &Client{transport: transport, session: session, clock: clock, telemetry: brokertelemetry.Safe(recorder)}, nil
}

func (client *Client) Capabilities(ctx context.Context) (CapabilitySnapshot, error) {
	started := client.clock.Now()
	body, err := client.read(ctx, client.transport.Profile)
	outcome := classifyOutcome(err)
	defer client.telemetry.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationProfile, Outcome: outcome, Duration: client.clock.Now().Sub(started)})
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Exchanges  []string `json:"exchanges"`
			Products   []string `json:"products"`
			OrderTypes []string `json:"order_types"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if decoder.Decode(&payload) != nil || payload.Status != "success" || len(payload.Data.Exchanges) == 0 {
		return CapabilitySnapshot{}, ErrMalformedResponse
	}
	return CapabilitySnapshot{Exchanges: boundedStrings(payload.Data.Exchanges), Products: boundedStrings(payload.Data.Products), OrderTypes: boundedStrings(payload.Data.OrderTypes)}, nil
}

func (client *Client) Instruments(ctx context.Context) ([]InstrumentRecord, error) {
	started := client.clock.Now()
	body, err := client.read(ctx, client.transport.Instruments)
	if err != nil {
		client.telemetry.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationInstruments, Outcome: classifyOutcome(err), Duration: client.clock.Now().Sub(started)})
		return nil, err
	}
	values, err := parseInstruments(body)
	client.telemetry.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationInstruments, Outcome: classifyOutcome(err), Duration: client.clock.Now().Sub(started)})
	return values, err
}

func (client *Client) read(ctx context.Context, operation func(context.Context, string) ([]byte, int, error)) ([]byte, error) {
	client.mu.RLock()
	stopped := client.stopped
	client.mu.RUnlock()
	if stopped {
		return nil, ErrStopped
	}
	authorization, err := client.session.Authorization()
	if err != nil {
		return nil, err
	}
	body, _, err := operation(ctx, authorization)
	if errors.Is(err, ErrSessionExpired) {
		client.session.Expire()
	}
	return body, err
}

func (client *Client) Shutdown() {
	client.mu.Lock()
	if client.stopped {
		client.mu.Unlock()
		return
	}
	client.stopped = true
	client.mu.Unlock()
	client.transport.CloseIdleConnections()
	client.session.Shutdown()
	client.telemetry.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationShutdown, Outcome: brokertelemetry.OutcomeStopped})
}

func parseInstruments(body []byte) ([]InstrumentRecord, error) {
	reader := csv.NewReader(strings.NewReader(string(body)))
	records, err := reader.ReadAll()
	if err != nil || len(records) < 2 || len(records[0]) != 12 {
		return nil, ErrMalformedResponse
	}
	expected := []string{"instrument_token", "exchange_token", "tradingsymbol", "name", "last_price", "expiry", "strike", "tick_size", "lot_size", "instrument_type", "segment", "exchange"}
	for index := range expected {
		if strings.TrimSpace(records[0][index]) != expected[index] {
			return nil, ErrMalformedResponse
		}
	}
	if len(records) > 200000 {
		return nil, ErrMalformedResponse
	}
	result := make([]InstrumentRecord, 0, len(records)-1)
	seen := make(map[string]bool, len(records)-1)
	for _, record := range records[1:] {
		if len(record) != len(expected) {
			return nil, ErrMalformedResponse
		}
		lot, parseErr := strconv.ParseInt(strings.TrimSpace(record[8]), 10, 64)
		token := strings.TrimSpace(record[0])
		if parseErr != nil || lot <= 0 || token == "" || seen[token] {
			return nil, ErrMalformedResponse
		}
		seen[token] = true
		result = append(result, InstrumentRecord{Token: token, ExchangeToken: strings.TrimSpace(record[1]), TradingSymbol: strings.TrimSpace(record[2]), Expiry: strings.TrimSpace(record[5]), Strike: strings.TrimSpace(record[6]), TickSize: strings.TrimSpace(record[7]), LotSize: lot, InstrumentType: strings.TrimSpace(record[9]), Segment: strings.TrimSpace(record[10]), Exchange: strings.TrimSpace(record[11])})
	}
	return result, nil
}

// ParseInstrumentDump validates and decodes the read-only daily Zerodha
// instrument CSV. It exists for operator tooling; it performs no network or
// broker mutation operation.
func ParseInstrumentDump(body []byte) ([]InstrumentRecord, error) {
	return parseInstruments(body)
}

func boundedStrings(values []string) []string {
	if len(values) > 32 {
		values = values[:32]
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value != "" && len(value) <= 32 {
			result = append(result, value)
		}
	}
	return result
}

func classifyOutcome(err error) brokertelemetry.Outcome {
	switch {
	case err == nil:
		return brokertelemetry.OutcomeSuccess
	case errors.Is(err, context.DeadlineExceeded):
		return brokertelemetry.OutcomeTimeout
	case errors.Is(err, ErrRateLimited):
		return brokertelemetry.OutcomeRateLimited
	case errors.Is(err, ErrMalformedResponse):
		return brokertelemetry.OutcomeMalformed
	case errors.Is(err, ErrSessionExpired):
		return brokertelemetry.OutcomeExpired
	default:
		return brokertelemetry.OutcomeFailure
	}
}
