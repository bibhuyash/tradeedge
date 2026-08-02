package reconciliation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	executionstorage "github.com/bibhuyash/tradeedge/internal/execution/storage"
)

var (
	ErrInvalidReconciler        = errors.New("invalid execution reconciler")
	ErrReconciliationInProgress = errors.New("execution reconciliation already in progress")
)

type IssueKind string

const (
	IssueMissingBrokerOrder IssueKind = "MISSING_BROKER_ORDER"
	IssueUnknownBrokerOrder IssueKind = "UNKNOWN_BROKER_ORDER"
	IssueTermsMismatch      IssueKind = "TERMS_MISMATCH"
	IssueStatusMismatch     IssueKind = "STATUS_MISMATCH"
	IssueFillMismatch       IssueKind = "FILL_MISMATCH"
	IssueBrokerBehind       IssueKind = "BROKER_FILL_BEHIND_LOCAL"
	IssueIncompleteSnapshot IssueKind = "INCOMPLETE_BROKER_SNAPSHOT"
)

type Severity string

const (
	SeverityWarning  Severity = "WARNING"
	SeverityCritical Severity = "CRITICAL"
)

type Issue struct {
	Kind          IssueKind
	Severity      Severity
	OrderID       executionmodel.OrderID
	ClientOrderID executionmodel.ClientOrderID
	BrokerOrderID string
}
type Receipt struct {
	ObservedAt time.Time
	Repaired   int
	Issues     []Issue
	Blocked    bool
	Cursor     executionbroker.EventCursor
}

type Repository interface {
	NonTerminalOrders(context.Context, int) ([]executionmodel.Order, error)
	OrderByClientOrderID(context.Context, executionmodel.ClientOrderID) (executionmodel.Order, error)
}

// PublisherFunc adapts the coordinator without giving reconciliation broker-submission capability.
type PublisherFunc func(context.Context, executionbroker.Event, time.Time) error

type Runner struct {
	repository Repository
	broker     executionbroker.Port
	publish    PublisherFunc
	mu         sync.Mutex
	running    bool
}

func New(repository Repository, broker executionbroker.Port, publish PublisherFunc) (*Runner, error) {
	if repository == nil || broker == nil || publish == nil {
		return nil, ErrInvalidReconciler
	}
	return &Runner{repository: repository, broker: broker, publish: publish}, nil
}

func (runner *Runner) Run(ctx context.Context, logicalTime time.Time) (Receipt, error) {
	receipt := Receipt{ObservedAt: logicalTime.UTC()}
	if logicalTime.IsZero() {
		return receipt, ErrInvalidReconciler
	}
	runner.mu.Lock()
	if runner.running {
		runner.mu.Unlock()
		return receipt, ErrReconciliationInProgress
	}
	runner.running = true
	runner.mu.Unlock()
	defer func() { runner.mu.Lock(); runner.running = false; runner.mu.Unlock() }()
	snapshot, err := runner.snapshot(ctx)
	if err != nil {
		return receipt, err
	}
	receipt.Cursor = snapshot.Cursor
	if !snapshot.Complete {
		receipt.Issues = append(receipt.Issues, Issue{Kind: IssueIncompleteSnapshot, Severity: SeverityCritical})
		receipt.Blocked = true
	}
	local, err := runner.repository.NonTerminalOrders(ctx, 1000)
	if err != nil {
		return receipt, err
	}
	brokerByClient := map[executionmodel.ClientOrderID]executionbroker.OrderSnapshot{}
	for _, remote := range snapshot.Orders {
		brokerByClient[remote.ClientOrderID] = remote
		order, lookupErr := runner.repository.OrderByClientOrderID(ctx, remote.ClientOrderID)
		if errors.Is(lookupErr, executionstorage.ErrNotFound) {
			receipt.Issues = append(receipt.Issues, Issue{Kind: IssueUnknownBrokerOrder, Severity: SeverityCritical, ClientOrderID: remote.ClientOrderID, BrokerOrderID: remote.BrokerOrderID})
			receipt.Blocked = true
			continue
		}
		if lookupErr != nil {
			return receipt, lookupErr
		}
		// Terminal cancellations remain reconcilable because a broker may report a
		// legitimate late fill after the local cancellation was published.
		if order.Spec().State == executionmodel.OrderCancelled && remote.CumulativeFilled > order.Spec().FilledQuantity {
			if termsMismatch(order, remote) {
				receipt.Issues = append(receipt.Issues, Issue{Kind: IssueTermsMismatch, Severity: SeverityCritical, OrderID: order.ID(), ClientOrderID: order.ClientOrderID(), BrokerOrderID: remote.BrokerOrderID})
				receipt.Blocked = true
				continue
			}
			events, needed, valid := repairEvents(order, remote, logicalTime)
			if !valid {
				receipt.Issues = append(receipt.Issues, Issue{Kind: IssueFillMismatch, Severity: SeverityCritical, OrderID: order.ID(), ClientOrderID: order.ClientOrderID(), BrokerOrderID: remote.BrokerOrderID})
				receipt.Blocked = true
				continue
			}
			if needed {
				if publishErr := publishEvents(ctx, runner.publish, events, logicalTime); publishErr != nil {
					receipt.Issues = append(receipt.Issues, Issue{Kind: IssueStatusMismatch, Severity: SeverityCritical, OrderID: order.ID(), ClientOrderID: order.ClientOrderID(), BrokerOrderID: remote.BrokerOrderID})
					receipt.Blocked = true
				} else {
					receipt.Repaired += len(events)
				}
			}
		}
	}
	for _, order := range local {
		remote, found := brokerByClient[order.ClientOrderID()]
		if !found {
			if order.Spec().State != executionmodel.OrderCreated && order.Spec().State != executionmodel.OrderPlanned {
				receipt.Issues = append(receipt.Issues, Issue{Kind: IssueMissingBrokerOrder, Severity: SeverityCritical, OrderID: order.ID(), ClientOrderID: order.ClientOrderID()})
				receipt.Blocked = true
			}
			continue
		}
		if termsMismatch(order, remote) {
			receipt.Issues = append(receipt.Issues, Issue{Kind: IssueTermsMismatch, Severity: SeverityCritical, OrderID: order.ID(), ClientOrderID: order.ClientOrderID(), BrokerOrderID: remote.BrokerOrderID})
			receipt.Blocked = true
			continue
		}
		if remote.CumulativeFilled < order.Spec().FilledQuantity {
			receipt.Issues = append(receipt.Issues, Issue{Kind: IssueBrokerBehind, Severity: SeverityCritical, OrderID: order.ID(), ClientOrderID: order.ClientOrderID(), BrokerOrderID: remote.BrokerOrderID})
			receipt.Blocked = true
			continue
		}
		events, needed, valid := repairEvents(order, remote, logicalTime)
		if !valid {
			receipt.Issues = append(receipt.Issues, Issue{Kind: IssueFillMismatch, Severity: SeverityCritical, OrderID: order.ID(), ClientOrderID: order.ClientOrderID(), BrokerOrderID: remote.BrokerOrderID})
			receipt.Blocked = true
			continue
		}
		if !needed {
			continue
		}
		if publishErr := publishEvents(ctx, runner.publish, events, logicalTime); publishErr != nil {
			receipt.Issues = append(receipt.Issues, Issue{Kind: IssueStatusMismatch, Severity: SeverityCritical, OrderID: order.ID(), ClientOrderID: order.ClientOrderID(), BrokerOrderID: remote.BrokerOrderID})
			receipt.Blocked = true
			continue
		}
		receipt.Repaired += len(events)
	}
	sort.Slice(receipt.Issues, func(i, j int) bool {
		left, right := receipt.Issues[i], receipt.Issues[j]
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		return left.ClientOrderID.String() < right.ClientOrderID.String()
	})
	return receipt, nil
}

func (runner *Runner) snapshot(parent context.Context) (snapshot executionbroker.Snapshot, err error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer func() {
		cancel()
		if recover() != nil {
			err = executionbroker.ErrUnavailable
		}
	}()
	return runner.broker.Snapshot(ctx, 1000)
}

func termsMismatch(local executionmodel.Order, remote executionbroker.OrderSnapshot) bool {
	return local.Spec().Leg.InstrumentID != remote.InstrumentID || local.Spec().Leg.Side != remote.Side || local.Spec().Leg.Quantity != remote.Quantity || local.Spec().Leg.LimitPrice.MinorUnits() != remote.LimitPrice.MinorUnits() || local.Spec().Leg.LimitPrice.Currency() != remote.LimitPrice.Currency()
}
func repairEvents(local executionmodel.Order, remote executionbroker.OrderSnapshot, at time.Time) ([]executionbroker.Event, bool, bool) {
	if remote.CumulativeFilled > local.Spec().FilledQuantity {
		fills := append([]executionbroker.FillSnapshot(nil), remote.Fills...)
		sort.Slice(fills, func(i, j int) bool {
			if !fills[i].OccurredAt.Equal(fills[j].OccurredAt) {
				return fills[i].OccurredAt.Before(fills[j].OccurredAt)
			}
			return fills[i].ExecutionID < fills[j].ExecutionID
		})
		cumulative := local.Spec().FilledQuantity
		events := make([]executionbroker.Event, 0, len(fills))
		for _, fill := range fills {
			if fill.CumulativeFilled <= cumulative {
				continue
			}
			if fill.ExecutionID == "" || fill.Quantity <= 0 || fill.CumulativeFilled != cumulative+fill.Quantity || fill.CumulativeFilled > remote.Quantity.Int64() || fill.Price.IsZeroValue() || fill.OccurredAt.IsZero() {
				return nil, false, false
			}
			kind := executionmodel.ReportPartialFill
			if fill.CumulativeFilled == remote.Quantity.Int64() {
				kind = executionmodel.ReportFill
			}
			events = append(events, executionbroker.Event{EventID: "reconcile-" + remote.BrokerOrderID + "-" + fill.ExecutionID, FillExecutionID: fill.ExecutionID, ClientOrderID: local.ClientOrderID(), BrokerOrderID: remote.BrokerOrderID, Type: kind, Reason: executionmodel.ReasonBrokerFill, CumulativeFilled: fill.CumulativeFilled, FillQuantity: fill.Quantity, FillPrice: fill.Price, OccurredAt: fill.OccurredAt})
			cumulative = fill.CumulativeFilled
		}
		if cumulative != remote.CumulativeFilled {
			return nil, false, false
		}
		return events, len(events) > 0, true
	}
	kind := executionmodel.ReportType("")
	reason := executionmodel.TransitionReason("")
	switch {
	case remote.State == executionmodel.OrderAcknowledged && local.Spec().State != executionmodel.OrderAcknowledged:
		kind = executionmodel.ReportAcknowledged
		reason = executionmodel.ReasonBrokerAcknowledged
	case remote.State == executionmodel.OrderSubmitted && local.Spec().State == executionmodel.OrderUnknown:
		kind = executionmodel.ReportSubmitted
		reason = executionmodel.ReasonBrokerAccepted
	case remote.State == executionmodel.OrderCancelled && local.Spec().State != executionmodel.OrderCancelled:
		kind = executionmodel.ReportCancelled
		reason = executionmodel.ReasonBrokerCancelled
	case remote.State == executionmodel.OrderRejected && local.Spec().State != executionmodel.OrderRejected:
		kind = executionmodel.ReportRejected
		reason = executionmodel.ReasonBrokerRejected
	default:
		return nil, false, true
	}
	eventID := fmt.Sprintf("reconcile-%s-%s-%d", remote.BrokerOrderID, kind, remote.CumulativeFilled)
	return []executionbroker.Event{{EventID: eventID, ClientOrderID: local.ClientOrderID(), BrokerOrderID: remote.BrokerOrderID, Type: kind, Reason: reason, CumulativeFilled: remote.CumulativeFilled, OccurredAt: remote.UpdatedAt}}, true, true
}

func publishEvents(ctx context.Context, publish PublisherFunc, events []executionbroker.Event, at time.Time) error {
	for _, event := range events {
		if err := publish(ctx, event, at); err != nil {
			return err
		}
	}
	return nil
}
