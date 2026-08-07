package zerodha

import (
	"context"
	"encoding/hex"
	"sync"
	"time"

	brokertelemetry "github.com/bibhuyash/tradeedge/internal/broker/telemetry"
	"github.com/bibhuyash/tradeedge/internal/domain"
	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	executionhealth "github.com/bibhuyash/tradeedge/internal/execution/health"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
)

const maximumShadowDecisions = 1000
const ShadowCheckpointVersion uint32 = 1

type ShadowCheckpoint struct {
	Version   uint32
	Decisions []ShadowDecision
}

type ShadowDecision struct {
	ClientOrderID      executionmodel.ClientOrderID `json:"client_order_id"`
	OrderID            executionmodel.OrderID       `json:"order_id"`
	InstrumentID       domain.InstrumentID          `json:"instrument_id"`
	Side               domain.Side                  `json:"side"`
	Quantity           int64                        `json:"quantity"`
	LimitPriceMinor    int64                        `json:"limit_price_minor"`
	Currency           domain.Currency              `json:"currency"`
	MappingVersion     string                       `json:"mapping_version,omitempty"`
	RequestFingerprint string                       `json:"request_fingerprint,omitempty"`
	Outcome            string                       `json:"outcome"`
	ObservedAt         time.Time                    `json:"observed_at"`
}

// ShadowBroker translates every approved submission exactly as the Zerodha
// adapter would, records only bounded/redacted metadata, and delegates all OMS
// behavior to a paper broker. It has no mutation transport.
type ShadowBroker struct {
	delegate executionbroker.Port
	mapper   *Mapper
	clock    Clock
	recorder brokertelemetry.Recorder
	mu       sync.Mutex
	values   []ShadowDecision
	closed   bool
}

func NewShadowBroker(delegate executionbroker.Port, mapper *Mapper, clock Clock, recorder brokertelemetry.Recorder) (*ShadowBroker, error) {
	if delegate == nil || mapper == nil {
		return nil, ErrInvalidConfiguration
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &ShadowBroker{delegate: delegate, mapper: mapper, clock: clock, recorder: brokertelemetry.Safe(recorder)}, nil
}

func RestoreShadowBroker(delegate executionbroker.Port, mapper *Mapper, clock Clock, recorder brokertelemetry.Recorder, checkpoint ShadowCheckpoint) (*ShadowBroker, error) {
	if checkpoint.Version != ShadowCheckpointVersion || len(checkpoint.Decisions) > maximumShadowDecisions {
		return nil, ErrInvalidConfiguration
	}
	value, err := NewShadowBroker(delegate, mapper, clock, recorder)
	if err != nil {
		return nil, err
	}
	value.values = append([]ShadowDecision(nil), checkpoint.Decisions...)
	return value, nil
}

func (broker *ShadowBroker) Submit(ctx context.Context, value executionbroker.Submission) (executionbroker.SubmissionResult, error) {
	request, version, err := translateSubmission(broker.mapper, value)
	decision := ShadowDecision{ClientOrderID: value.ClientOrderID, OrderID: value.OrderID, InstrumentID: value.InstrumentID, Side: value.Side, Quantity: value.Quantity.Int64(), LimitPriceMinor: value.LimitPrice.MinorUnits(), Currency: value.LimitPrice.Currency(), MappingVersion: version, Outcome: "blocked", ObservedAt: broker.clock.Now()}
	if err == nil {
		hash := hashOrderRequest(request)
		decision.RequestFingerprint, decision.Outcome = hex.EncodeToString(hash[:]), "translated"
	}
	broker.mu.Lock()
	if broker.closed {
		broker.mu.Unlock()
		return executionbroker.SubmissionResult{}, executionbroker.ErrUnavailable
	}
	broker.values = append(broker.values, decision)
	if len(broker.values) > maximumShadowDecisions {
		broker.values = append([]ShadowDecision(nil), broker.values[len(broker.values)-maximumShadowDecisions:]...)
	}
	broker.mu.Unlock()
	if err != nil {
		broker.recorder.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationShadow, Outcome: brokertelemetry.OutcomeRejected})
		return executionbroker.SubmissionResult{Status: executionbroker.SubmissionRejected}, nil
	}
	broker.recorder.Record(brokertelemetry.Event{Operation: brokertelemetry.OperationShadow, Outcome: brokertelemetry.OutcomeSuccess})
	return broker.delegate.Submit(ctx, value)
}

func (broker *ShadowBroker) Cancel(ctx context.Context, value executionbroker.CancellationRequest) (executionbroker.CancellationResult, error) {
	return broker.delegate.Cancel(ctx, value)
}
func (broker *ShadowBroker) LookupByClientOrderID(ctx context.Context, id executionmodel.ClientOrderID) (executionbroker.OrderSnapshot, error) {
	return broker.delegate.LookupByClientOrderID(ctx, id)
}
func (broker *ShadowBroker) EventsAfter(ctx context.Context, cursor executionbroker.EventCursor, limit int) (executionbroker.EventBatch, error) {
	return broker.delegate.EventsAfter(ctx, cursor, limit)
}
func (broker *ShadowBroker) Snapshot(ctx context.Context, limit int) (executionbroker.Snapshot, error) {
	return broker.delegate.Snapshot(ctx, limit)
}
func (broker *ShadowBroker) Decisions(limit int) []ShadowDecision {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	start := len(broker.values) - limit
	if start < 0 {
		start = 0
	}
	return append([]ShadowDecision(nil), broker.values[start:]...)
}
func (broker *ShadowBroker) Checkpoint() ShadowCheckpoint {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	return ShadowCheckpoint{Version: ShadowCheckpointVersion, Decisions: append([]ShadowDecision(nil), broker.values...)}
}
func (broker *ShadowBroker) Shutdown() {
	broker.mu.Lock()
	if broker.closed {
		broker.mu.Unlock()
		return
	}
	broker.closed = true
	broker.mu.Unlock()
	if value, ok := broker.delegate.(interface{ Shutdown() }); ok {
		value.Shutdown()
	}
}
func (broker *ShadowBroker) Health() executionhealth.PaperBroker {
	if value, ok := broker.delegate.(interface {
		Health() executionhealth.PaperBroker
	}); ok {
		return value.Health()
	}
	return executionhealth.PaperBroker{Available: true}
}

var _ executionbroker.Port = (*ShadowBroker)(nil)
