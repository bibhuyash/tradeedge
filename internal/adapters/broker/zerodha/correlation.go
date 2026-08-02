package zerodha

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
)

type CorrelationState string

const (
	CorrelationPrepared  CorrelationState = "PREPARED"
	CorrelationNotSent   CorrelationState = "NOT_SENT"
	CorrelationUncertain CorrelationState = "SEND_UNCERTAIN"
	CorrelationAccepted  CorrelationState = "ACCEPTED"
	CorrelationRejected  CorrelationState = "REJECTED"
)

type CorrelationRecord struct {
	Submission     executionbroker.Submission
	Request        OrderRequest
	RequestHash    [sha256.Size]byte
	MappingVersion string
	State          CorrelationState
	BrokerOrderID  string
	UpdatedAt      time.Time
}

type ExecutionCheckpoint struct {
	Version uint32
	Records []CorrelationRecord
	Events  []executionbroker.Event
	Gap     bool
}

type executionState struct {
	mu      sync.Mutex
	records map[executionmodel.ClientOrderID]CorrelationRecord
	tags    map[string]executionmodel.ClientOrderID
	events  []executionbroker.Event
	seen    map[string]executionbroker.Event
	gap     bool
	closed  bool
}

func newExecutionState() *executionState {
	return &executionState{records: map[executionmodel.ClientOrderID]CorrelationRecord{}, tags: map[string]executionmodel.ClientOrderID{}, seen: map[string]executionbroker.Event{}}
}

func restoreExecutionState(checkpoint ExecutionCheckpoint) (*executionState, error) {
	state := newExecutionState()
	if checkpoint.Version != 0 && checkpoint.Version != 1 {
		return nil, executionbroker.ErrIdentityConflict
	}
	for _, record := range checkpoint.Records {
		if record.Submission.ClientOrderID.IsZero() || record.Request.Tag == "" || record.RequestHash != hashOrderRequest(record.Request) {
			return nil, executionbroker.ErrIdentityConflict
		}
		if existing, ok := state.tags[record.Request.Tag]; ok && existing != record.Submission.ClientOrderID {
			return nil, executionbroker.ErrIdentityConflict
		}
		if _, duplicate := state.records[record.Submission.ClientOrderID]; duplicate {
			return nil, executionbroker.ErrIdentityConflict
		}
		state.records[record.Submission.ClientOrderID] = record
		state.tags[record.Request.Tag] = record.Submission.ClientOrderID
	}
	for _, event := range checkpoint.Events {
		if event.EventID == "" {
			return nil, executionbroker.ErrIdentityConflict
		}
		if existing, ok := state.seen[event.EventID]; ok && !sameEvent(existing, event) {
			return nil, executionbroker.ErrIdentityConflict
		}
		state.seen[event.EventID] = event
		state.events = append(state.events, event)
	}
	state.gap = checkpoint.Gap
	return state, nil
}

func (state *executionState) checkpoint() ExecutionCheckpoint {
	state.mu.Lock()
	defer state.mu.Unlock()
	records := make([]CorrelationRecord, 0, len(state.records))
	for _, record := range state.records {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Submission.ClientOrderID.String() < records[j].Submission.ClientOrderID.String()
	})
	return ExecutionCheckpoint{Version: 1, Records: records, Events: append([]executionbroker.Event(nil), state.events...), Gap: state.gap}
}

func compactTag(id executionmodel.ClientOrderID) string {
	digest := sha256.Sum256([]byte("tradeedge-zerodha-tag/v1|" + id.String()))
	return "TE" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:])[:18]
}

func hashOrderRequest(value OrderRequest) [sha256.Size]byte {
	return sha256.Sum256([]byte(value.Tag + "|" + value.TradingSymbol + "|" + value.Exchange + "|" + value.TransactionType + "|" + value.OrderType + "|" + value.Product + "|" + value.Validity + "|" + value.Variety + "|" + value.Price + "|" + strconv.FormatInt(value.Quantity, 10)))
}

func sameEvent(left, right executionbroker.Event) bool {
	return left.EventID == right.EventID && left.FillExecutionID == right.FillExecutionID && left.ClientOrderID == right.ClientOrderID && left.BrokerOrderID == right.BrokerOrderID && left.Type == right.Type && left.Reason == right.Reason && left.CumulativeFilled == right.CumulativeFilled && left.FillQuantity == right.FillQuantity && left.FillPrice.MinorUnits() == right.FillPrice.MinorUnits() && left.FillPrice.Currency() == right.FillPrice.Currency() && left.OccurredAt.Equal(right.OccurredAt)
}

func (state *executionState) appendEvent(event executionbroker.Event) error {
	if existing, found := state.seen[event.EventID]; found {
		if sameEvent(existing, event) {
			return nil
		}
		return executionbroker.ErrIdentityConflict
	}
	state.seen[event.EventID] = event
	state.events = append(state.events, event)
	return nil
}

func deliveryError(responseDelivery DeliveryState, err error) error {
	if responseDelivery == DeliveryNotSent {
		return executionbroker.ErrUnavailable
	}
	if responseDelivery == DeliveryPossiblySent || errors.Is(err, ErrDeliveryUnknown) {
		return executionbroker.ErrOutcomeUnknown
	}
	return err
}
