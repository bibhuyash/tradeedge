package coordinator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	executionbroker "github.com/bibhuyash/tradeedge/internal/execution/broker"
	executionhealth "github.com/bibhuyash/tradeedge/internal/execution/health"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	executionstorage "github.com/bibhuyash/tradeedge/internal/execution/storage"
	executiontelemetry "github.com/bibhuyash/tradeedge/internal/execution/telemetry"
)

var (
	ErrInvalidCoordinator  = errors.New("invalid execution coordinator")
	ErrDuplicateInProgress = errors.New("execution plan already in progress")
	ErrPlanBusy            = errors.New("execution plan is busy")
	ErrOrderBusy           = errors.New("execution order is busy")
	ErrShutdown            = errors.New("execution coordinator is shut down")
	ErrUnknownOutcome      = errors.New("execution outcome remains unknown")
)

type Outcome string

const (
	OutcomeCompleted           Outcome = "COMPLETED"
	OutcomePending             Outcome = "PENDING"
	OutcomeIncomplete          Outcome = "INCOMPLETE"
	OutcomeUnknown             Outcome = "UNKNOWN"
	OutcomeDuplicateCommitted  Outcome = "DUPLICATE_COMMITTED"
	OutcomeDuplicateInProgress Outcome = "DUPLICATE_IN_PROGRESS"
	OutcomeBusy                Outcome = "BUSY"
	OutcomeShutdown            Outcome = "SHUTDOWN"
)

type PlanReceipt struct {
	PlanID  executionmodel.OrderPlanID
	Outcome Outcome
	Orders  []executionmodel.OrderID
}
type OrderReceipt struct {
	OrderID executionmodel.OrderID
	State   executionmodel.OrderState
}
type Config struct {
	MaxConcurrentPlans  int
	BrokerTimeout       time.Duration
	KnownNotSentRetries int
	EventBatchSize      int
}

func DefaultConfig() Config {
	return Config{MaxConcurrentPlans: 4, BrokerTimeout: time.Second, KnownNotSentRetries: 3, EventBatchSize: 100}
}

type Repository interface {
	executionstorage.OMSRepository
}

type Coordinator struct {
	repository Repository
	broker     executionbroker.Port
	config     Config
	ctx        context.Context
	cancel     context.CancelFunc
	semaphore  chan struct{}
	mu         sync.Mutex
	plans      map[executionmodel.OrderPlanID]string
	orders     map[executionmodel.OrderID]bool
	closed     bool
	wait       sync.WaitGroup
	stopOnce   sync.Once
	stopped    chan struct{}
	eventMu    sync.Mutex
	cursor     executionbroker.EventCursor
	telemetry  executiontelemetry.Recorder
}

func New(repository Repository, broker executionbroker.Port, config Config) (*Coordinator, error) {
	return NewInstrumented(repository, broker, config, executiontelemetry.NopRecorder{})
}

func NewInstrumented(repository Repository, broker executionbroker.Port, config Config, recorder executiontelemetry.Recorder) (*Coordinator, error) {
	if repository == nil || broker == nil || config.MaxConcurrentPlans <= 0 || config.MaxConcurrentPlans > 64 || config.BrokerTimeout <= 0 || config.BrokerTimeout > time.Minute || config.KnownNotSentRetries < 0 || config.KnownNotSentRetries > 10 || config.EventBatchSize <= 0 || config.EventBatchSize > 1000 {
		return nil, ErrInvalidCoordinator
	}
	recorder = executiontelemetry.Safe(recorder)
	ctx, cancel := context.WithCancel(context.Background())
	return &Coordinator{repository: repository, broker: broker, config: config, ctx: ctx, cancel: cancel, semaphore: make(chan struct{}, config.MaxConcurrentPlans), plans: map[executionmodel.OrderPlanID]string{}, orders: map[executionmodel.OrderID]bool{}, stopped: make(chan struct{}), telemetry: recorder}, nil
}

func (runner *Coordinator) ExecutePlan(ctx context.Context, planID executionmodel.OrderPlanID, logicalTime time.Time) (PlanReceipt, error) {
	return runner.execute(ctx, planID, logicalTime)
}
func (runner *Coordinator) ResumePlan(ctx context.Context, planID executionmodel.OrderPlanID, logicalTime time.Time) (PlanReceipt, error) {
	return runner.execute(ctx, planID, logicalTime)
}

func (runner *Coordinator) execute(ctx context.Context, planID executionmodel.OrderPlanID, logicalTime time.Time) (receipt PlanReceipt, err error) {
	started := time.Now()
	defer func() { runner.recordPlan(receipt, err, time.Since(started)) }()
	receipt.PlanID = planID
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if planID.IsZero() || logicalTime.IsZero() {
		return receipt, ErrInvalidCoordinator
	}
	plan, err := runner.repository.Plan(ctx, planID)
	if err != nil {
		return receipt, err
	}
	intent, err := runner.repository.Intent(ctx, plan.IntentID())
	if err != nil {
		return receipt, err
	}
	if intent.ID() != plan.IntentID() || logicalTime.Before(plan.Spec().CreatedAt) || !logicalTime.Before(plan.Spec().ExpiresAt) {
		return receipt, executionmodel.ErrDecisionExpired
	}
	orders, err := runner.repository.OrdersForPlan(ctx, planID)
	if err != nil {
		return receipt, err
	}
	for _, order := range orders {
		receipt.Orders = append(receipt.Orders, order.ID())
	}
	if allFilled(orders) {
		receipt.Outcome = OutcomeDuplicateCommitted
		return receipt, nil
	}
	trigger := planID.String() + "|" + logicalTime.UTC().Format(time.RFC3339Nano)
	if outcome := runner.reservePlan(planID, trigger); outcome != "" {
		receipt.Outcome = outcome
		if outcome == OutcomeDuplicateInProgress {
			return receipt, ErrDuplicateInProgress
		}
		if outcome == OutcomeShutdown {
			return receipt, ErrShutdown
		}
		return receipt, ErrPlanBusy
	}
	defer runner.releasePlan(planID)
	defer runner.wait.Done()
	select {
	case runner.semaphore <- struct{}{}:
		defer func() { <-runner.semaphore }()
	case <-ctx.Done():
		return receipt, ctx.Err()
	case <-runner.ctx.Done():
		receipt.Outcome = OutcomeShutdown
		return receipt, ErrShutdown
	}
	if err := runner.DrainEvents(ctx, logicalTime); err != nil && !errors.Is(err, executionbroker.ErrUnavailable) {
		return receipt, err
	}
	for {
		orders, err = runner.repository.OrdersForPlan(ctx, planID)
		if err != nil {
			return receipt, err
		}
		byLeg := map[executionmodel.OrderLegID]executionmodel.Order{}
		for _, order := range orders {
			byLeg[order.Spec().Leg.ID] = order
		}
		progress := false
		for _, leg := range plan.Legs() {
			order := byLeg[leg.ID]
			if order.Spec().State == executionmodel.OrderSubmissionPending {
				if !runner.reserveOrder(order.ID()) {
					return receipt, ErrOrderBusy
				}
				order, err = runner.publishInternal(ctx, order, executionmodel.ReportUnknown, executionmodel.ReasonSubmissionOutcomeUnknown, logicalTime, "restart-unknown-"+fmt.Sprint(order.Spec().Revision))
				if err != nil {
					runner.releaseOrder(order.ID())
					return receipt, err
				}
				byLeg[leg.ID] = order
				progress = true
				recovered, recoverErr := runner.recoverUnknown(ctx, order, logicalTime)
				runner.releaseOrder(order.ID())
				if recoverErr == nil {
					byLeg[leg.ID] = recovered
					progress = true
				}
				continue
			}
			if order.Spec().State == executionmodel.OrderUnknown {
				if !runner.reserveOrder(order.ID()) {
					return receipt, ErrOrderBusy
				}
				recovered, recoverErr := runner.recoverUnknown(ctx, order, logicalTime)
				runner.releaseOrder(order.ID())
				if recoverErr == nil {
					byLeg[leg.ID] = recovered
					progress = true
				}
				continue
			}
			if order.Spec().State == executionmodel.OrderCreated {
				if !runner.reserveOrder(order.ID()) {
					return receipt, ErrOrderBusy
				}
				if _, err = runner.publishInternal(ctx, order, executionmodel.ReportPlanned, executionmodel.ReasonPlanned, logicalTime, "planned"); err != nil {
					runner.releaseOrder(order.ID())
					return receipt, err
				}
				runner.releaseOrder(order.ID())
				progress = true
				continue
			}
			if order.Spec().State != executionmodel.OrderPlanned || !dependenciesFilled(leg, byLeg) {
				continue
			}
			if leg.Side == domain.SideSell && !dependenciesFilled(leg, byLeg) {
				return receipt, executionmodel.ErrUnsafeLegSequence
			}
			if _, err = runner.submit(ctx, order, logicalTime); err != nil && !errors.Is(err, ErrUnknownOutcome) {
				return receipt, err
			}
			progress = true
			if drainErr := runner.DrainEvents(ctx, logicalTime); drainErr != nil && !errors.Is(drainErr, executionbroker.ErrUnavailable) {
				return receipt, drainErr
			}
		}
		if !progress {
			break
		}
	}
	orders, _ = runner.repository.OrdersForPlan(ctx, planID)
	unknownCount := 0
	for _, order := range orders {
		if order.Spec().State == executionmodel.OrderUnknown {
			unknownCount++
		}
	}
	switch {
	case allFilled(orders):
		receipt.Outcome = OutcomeCompleted
	case anyState(orders, executionmodel.OrderUnknown):
		receipt.Outcome = OutcomeUnknown
		err = ErrUnknownOutcome
	case anyTerminalFailure(orders):
		receipt.Outcome = OutcomeIncomplete
	default:
		receipt.Outcome = OutcomePending
	}
	runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationHealth, Outcome: executiontelemetry.OutcomeCompleted, Occurred: logicalTime.UTC(), UnknownOrders: unknownCount, HasUnknownOrders: true})
	return receipt, err
}

func (runner *Coordinator) recordPlan(receipt PlanReceipt, err error, duration time.Duration) {
	outcome := executiontelemetry.OutcomeFailed
	switch receipt.Outcome {
	case OutcomeCompleted, OutcomeDuplicateCommitted:
		outcome = executiontelemetry.OutcomeCompleted
	case OutcomePending:
		outcome = executiontelemetry.OutcomePending
	case OutcomeUnknown:
		outcome = executiontelemetry.OutcomeUnknown
	case OutcomeIncomplete:
		outcome = executiontelemetry.OutcomeFailed
	case OutcomeDuplicateInProgress:
		outcome = executiontelemetry.OutcomeDuplicate
	case OutcomeShutdown:
		outcome = executiontelemetry.OutcomeShutdown
	}
	if err != nil && errors.Is(err, executionbroker.ErrUnavailable) {
		outcome = executiontelemetry.OutcomeUnavailable
	}
	health := runner.Health()
	runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationPlan, Outcome: outcome, PlanID: receipt.PlanID.String(), Duration: duration, InFlight: health.InFlightPlans})
}

func dependenciesFilled(leg executionmodel.OrderLeg, orders map[executionmodel.OrderLegID]executionmodel.Order) bool {
	for _, id := range leg.DependsOn {
		order, ok := orders[id]
		if !ok || order.Spec().State != executionmodel.OrderFilled {
			return false
		}
	}
	return true
}
func allFilled(values []executionmodel.Order) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if value.Spec().State != executionmodel.OrderFilled {
			return false
		}
	}
	return true
}
func anyState(values []executionmodel.Order, state executionmodel.OrderState) bool {
	for _, value := range values {
		if value.Spec().State == state {
			return true
		}
	}
	return false
}
func anyTerminalFailure(values []executionmodel.Order) bool {
	for _, value := range values {
		switch value.Spec().State {
		case executionmodel.OrderRejected, executionmodel.OrderCancelled, executionmodel.OrderExpired, executionmodel.OrderFailed:
			return true
		}
	}
	return false
}

func (runner *Coordinator) submit(ctx context.Context, order executionmodel.Order, logicalTime time.Time) (OrderReceipt, error) {
	if !runner.reserveOrder(order.ID()) {
		return OrderReceipt{}, ErrOrderBusy
	}
	defer runner.releaseOrder(order.ID())
	current, err := runner.repository.Order(ctx, order.ID())
	if err != nil {
		return OrderReceipt{}, err
	}
	if current.Spec().State != executionmodel.OrderPlanned {
		return OrderReceipt{current.ID(), current.Spec().State}, nil
	}
	attemptID, _ := executionmodel.NewSubmissionAttemptID(order.ID().String(), fmt.Sprint(current.Spec().Revision), logicalTime.UTC().Format(time.RFC3339Nano))
	current, err = runner.publishInternal(ctx, current, executionmodel.ReportSubmissionPending, executionmodel.ReasonSubmissionStarted, logicalTime, attemptID.String()+"-pending")
	if err != nil {
		return OrderReceipt{}, err
	}
	request := executionbroker.Submission{AttemptID: attemptID, OrderID: current.ID(), ClientOrderID: current.ClientOrderID(), InstrumentID: current.Spec().Leg.InstrumentID, Side: current.Spec().Leg.Side, Quantity: current.Spec().Leg.Quantity, LimitPrice: current.Spec().Leg.LimitPrice, SubmittedAt: logicalTime.UTC()}
	var result executionbroker.SubmissionResult
	var submitErr error
	for attempt := 0; attempt <= runner.config.KnownNotSentRetries; attempt++ {
		result, submitErr = runner.invokeSubmit(ctx, request)
		if !errors.Is(submitErr, executionbroker.ErrUnavailable) {
			break
		}
	}
	if submitErr != nil {
		runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationSubmission, Outcome: executiontelemetry.OutcomeUnknown, OrderID: current.ID().String(), Occurred: logicalTime.UTC()})
		unknown, publishErr := runner.publishInternal(ctx, current, executionmodel.ReportUnknown, executionmodel.ReasonSubmissionOutcomeUnknown, logicalTime, attemptID.String()+"-unknown")
		if publishErr != nil {
			return OrderReceipt{}, publishErr
		}
		if recovered, recoverErr := runner.recoverUnknown(ctx, unknown, logicalTime); recoverErr == nil {
			return OrderReceipt{recovered.ID(), recovered.Spec().State}, nil
		}
		return OrderReceipt{unknown.ID(), unknown.Spec().State}, ErrUnknownOutcome
	}
	if result.Status == executionbroker.SubmissionRejected {
		runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationSubmission, Outcome: executiontelemetry.OutcomeRejected, OrderID: current.ID().String(), Occurred: logicalTime.UTC()})
		next, err := runner.publishInternalWithBroker(ctx, current, executionmodel.ReportRejected, executionmodel.ReasonBrokerRejected, logicalTime, attemptID.String()+"-rejected", result.BrokerOrderID)
		return OrderReceipt{next.ID(), next.Spec().State}, err
	}
	runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationSubmission, Outcome: executiontelemetry.OutcomeAccepted, OrderID: current.ID().String(), Occurred: logicalTime.UTC()})
	next, err := runner.publishInternalWithBroker(ctx, current, executionmodel.ReportSubmitted, executionmodel.ReasonBrokerAccepted, logicalTime, attemptID.String()+"-submitted", result.BrokerOrderID)
	return OrderReceipt{next.ID(), next.Spec().State}, err
}

func (runner *Coordinator) invokeSubmit(parent context.Context, request executionbroker.Submission) (result executionbroker.SubmissionResult, err error) {
	callCtx, cancel := context.WithTimeout(parent, runner.config.BrokerTimeout)
	stop := context.AfterFunc(runner.ctx, cancel)
	defer func() {
		stop()
		cancel()
		if recovered := recover(); recovered != nil {
			_ = debug.Stack()
			err = executionbroker.ErrOutcomeUnknown
		}
	}()
	return runner.broker.Submit(callCtx, request)
}

func (runner *Coordinator) invokeLookup(parent context.Context, id executionmodel.ClientOrderID) (snapshot executionbroker.OrderSnapshot, err error) {
	callCtx, cancel := context.WithTimeout(parent, runner.config.BrokerTimeout)
	stop := context.AfterFunc(runner.ctx, cancel)
	defer func() {
		stop()
		cancel()
		if recover() != nil {
			err = executionbroker.ErrUnavailable
		}
	}()
	return runner.broker.LookupByClientOrderID(callCtx, id)
}

func (runner *Coordinator) invokeEvents(parent context.Context) (batch executionbroker.EventBatch, err error) {
	callCtx, cancel := context.WithTimeout(parent, runner.config.BrokerTimeout)
	stop := context.AfterFunc(runner.ctx, cancel)
	defer func() {
		stop()
		cancel()
		if recover() != nil {
			err = executionbroker.ErrUnavailable
		}
	}()
	return runner.broker.EventsAfter(callCtx, runner.cursor, runner.config.EventBatchSize)
}

func (runner *Coordinator) invokeCancel(parent context.Context, request executionbroker.CancellationRequest) (result executionbroker.CancellationResult, err error) {
	callCtx, cancel := context.WithTimeout(parent, runner.config.BrokerTimeout)
	stop := context.AfterFunc(runner.ctx, cancel)
	defer func() {
		stop()
		cancel()
		if recover() != nil {
			err = executionbroker.ErrOutcomeUnknown
		}
	}()
	return runner.broker.Cancel(callCtx, request)
}

func (runner *Coordinator) recoverUnknown(ctx context.Context, order executionmodel.Order, logicalTime time.Time) (executionmodel.Order, error) {
	snapshot, err := runner.invokeLookup(ctx, order.ClientOrderID())
	if err != nil {
		return order, err
	}
	if snapshot.InstrumentID != order.Spec().Leg.InstrumentID || snapshot.Side != order.Spec().Leg.Side || snapshot.Quantity != order.Spec().Leg.Quantity || snapshot.LimitPrice.MinorUnits() != order.Spec().Leg.LimitPrice.MinorUnits() || snapshot.LimitPrice.Currency() != order.Spec().Leg.LimitPrice.Currency() {
		return order, executionbroker.ErrIdentityConflict
	}
	return runner.publishInternalWithBroker(ctx, order, executionmodel.ReportSubmitted, executionmodel.ReasonBrokerAccepted, logicalTime, "recovered-"+snapshot.BrokerOrderID, snapshot.BrokerOrderID)
}

func (runner *Coordinator) publishInternal(ctx context.Context, order executionmodel.Order, kind executionmodel.ReportType, reason executionmodel.TransitionReason, at time.Time, key string) (executionmodel.Order, error) {
	return runner.publishInternalWithBroker(ctx, order, kind, reason, at, key, order.Spec().BrokerOrderID)
}
func (runner *Coordinator) publishInternalWithBroker(ctx context.Context, order executionmodel.Order, kind executionmodel.ReportType, reason executionmodel.TransitionReason, at time.Time, key, brokerID string) (executionmodel.Order, error) {
	report, err := executionmodel.NewExecutionReport(executionmodel.ExecutionReportSpec{SchemaVersion: "execution-report/v1", Source: "COORDINATOR", SourceEventID: order.ID().String() + "-" + key, OrderID: order.ID(), ClientOrderID: order.ClientOrderID(), BrokerOrderID: brokerID, Type: kind, Reason: reason, CumulativeFilled: order.Spec().FilledQuantity, OccurredAt: at.UTC(), ReceivedAt: at.UTC()})
	if err != nil {
		return executionmodel.Order{}, err
	}
	return runner.publish(ctx, order, report, nil)
}

func (runner *Coordinator) publish(ctx context.Context, current executionmodel.Order, report executionmodel.ExecutionReport, fill *executionmodel.Fill) (next executionmodel.Order, err error) {
	started := time.Now()
	defer func() {
		outcome := executiontelemetry.OutcomeCompleted
		if err != nil {
			outcome = executiontelemetry.OutcomeInvalid
		}
		runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationPublication, Outcome: outcome, Detail: string(report.Spec().Type), OrderID: current.ID().String(), Occurred: report.Spec().ReceivedAt, Duration: time.Since(started)})
	}()
	checkpoint, err := runner.repository.CurrentOrderCheckpoint(ctx, current.ID())
	if err != nil {
		return executionmodel.Order{}, err
	}
	next, err = executionmodel.ApplyExecutionReport(checkpoint.Order, report, fill)
	if err != nil {
		return executionmodel.Order{}, err
	}
	nextCheckpointSpec := executionstorage.OrderCheckpoint{Order: next, ParentChecksum: checkpoint.CheckpointChecksum, ReportID: report.ID()}
	if fill != nil {
		nextCheckpointSpec.FillID = fill.ID()
	}
	nextCheckpoint, err := executionstorage.NewOrderCheckpoint(nextCheckpointSpec)
	if err != nil {
		return executionmodel.Order{}, err
	}
	publicationID, _ := executionmodel.NewPublicationID(current.ID().String(), report.ID().String())
	publication, err := executionstorage.NewOrderPublication(executionstorage.OrderPublication{PublicationID: publicationID, ExpectedRevision: checkpoint.Order.Spec().Revision, ExpectedCheckpoint: checkpoint.CheckpointChecksum, Report: report, Fill: fill, NextCheckpoint: nextCheckpoint})
	if err != nil {
		return executionmodel.Order{}, err
	}
	_, err = runner.repository.PublishOrderEvent(ctx, publication)
	if err != nil {
		return executionmodel.Order{}, err
	}
	return next, nil
}

func (runner *Coordinator) DrainEvents(ctx context.Context, receivedAt time.Time) error {
	runner.eventMu.Lock()
	defer runner.eventMu.Unlock()
	batch, err := runner.invokeEvents(ctx)
	if err != nil {
		return err
	}
	for _, event := range batch.Events {
		_, publishErr := runner.PublishBrokerEvent(ctx, event, receivedAt)
		if publishErr != nil && !errors.Is(publishErr, executionstorage.ErrIdentityCollision) {
			if errors.Is(publishErr, executionstorage.ErrNotFound) {
				continue
			}
			return publishErr
		}
	}
	runner.cursor = batch.NextCursor
	return nil
}

func (runner *Coordinator) PublishBrokerEvent(ctx context.Context, event executionbroker.Event, receivedAt time.Time) (OrderReceipt, error) {
	order, err := runner.repository.OrderByClientOrderID(ctx, event.ClientOrderID)
	if err != nil {
		return OrderReceipt{}, err
	}
	if !runner.reserveOrder(order.ID()) {
		return OrderReceipt{}, ErrOrderBusy
	}
	defer runner.releaseOrder(order.ID())
	report, err := executionmodel.NewExecutionReport(executionmodel.ExecutionReportSpec{SchemaVersion: "execution-report/v1", Source: "BROKER", SourceEventID: event.EventID, OrderID: order.ID(), ClientOrderID: order.ClientOrderID(), BrokerOrderID: event.BrokerOrderID, Type: event.Type, Reason: event.Reason, CumulativeFilled: event.CumulativeFilled, OccurredAt: event.OccurredAt, ReceivedAt: receivedAt.UTC()})
	if err != nil {
		return OrderReceipt{}, err
	}
	existingReports, err := runner.repository.Reports(ctx, order.ID())
	if err != nil {
		return OrderReceipt{}, err
	}
	for _, existing := range existingReports {
		if existing.ID() == report.ID() {
			if !bytes.Equal(existing.CanonicalJSON(), report.CanonicalJSON()) {
				runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationOrderEvent, Outcome: executiontelemetry.OutcomeInvalid, Detail: string(event.Type), OrderID: order.ID().String(), Occurred: receivedAt.UTC()})
				return OrderReceipt{}, &executionstorage.IdentityCollisionError{Kind: "report", Identity: report.ID().String()}
			}
			runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationOrderEvent, Outcome: executiontelemetry.OutcomeDuplicate, Detail: string(event.Type), OrderID: order.ID().String(), Occurred: receivedAt.UTC()})
			return OrderReceipt{order.ID(), order.Spec().State}, nil
		}
	}
	if staleBrokerEvent(order, event) {
		runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationOrderEvent, Outcome: executiontelemetry.OutcomeDuplicate, Detail: string(event.Type), OrderID: order.ID().String(), Occurred: receivedAt.UTC()})
		return OrderReceipt{order.ID(), order.Spec().State}, nil
	}
	var fill *executionmodel.Fill
	if event.FillQuantity > 0 {
		quantity, quantityErr := domain.NewQuantity(event.FillQuantity)
		if quantityErr != nil {
			return OrderReceipt{}, quantityErr
		}
		value, fillErr := executionmodel.NewFill(executionmodel.FillSpec{SchemaVersion: "fill/v1", Source: "BROKER", SourceExecutionID: event.FillExecutionID, OrderID: order.ID(), ReportID: report.ID(), Quantity: quantity, Price: event.FillPrice, OccurredAt: event.OccurredAt})
		if fillErr != nil {
			return OrderReceipt{}, fillErr
		}
		fill = &value
	}
	next, err := runner.publish(ctx, order, report, fill)
	if err != nil {
		runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationOrderEvent, Outcome: executiontelemetry.OutcomeInvalid, Detail: string(event.Type), OrderID: order.ID().String(), Occurred: receivedAt.UTC()})
		return OrderReceipt{}, err
	}
	runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationOrderEvent, Outcome: eventOutcome(event.Type), Detail: string(event.Type), OrderID: order.ID().String(), Occurred: receivedAt.UTC()})
	runner.recordUnknownCount(ctx, receivedAt)
	return OrderReceipt{next.ID(), next.Spec().State}, nil
}

func (runner *Coordinator) recordUnknownCount(ctx context.Context, occurred time.Time) {
	orders, err := runner.repository.NonTerminalOrders(ctx, 1000)
	if err != nil {
		return
	}
	count := 0
	for _, order := range orders {
		if order.Spec().State == executionmodel.OrderUnknown {
			count++
		}
	}
	runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationHealth, Outcome: executiontelemetry.OutcomeCompleted, Occurred: occurred.UTC(), UnknownOrders: count, HasUnknownOrders: true})
}

func eventOutcome(kind executionmodel.ReportType) executiontelemetry.Outcome {
	switch kind {
	case executionmodel.ReportAcknowledged:
		return executiontelemetry.OutcomeAcknowledged
	case executionmodel.ReportPartialFill:
		return executiontelemetry.OutcomePartialFill
	case executionmodel.ReportFill:
		return executiontelemetry.OutcomeFilled
	case executionmodel.ReportCancelled:
		return executiontelemetry.OutcomeCancelled
	case executionmodel.ReportRejected:
		return executiontelemetry.OutcomeRejected
	case executionmodel.ReportUnknown:
		return executiontelemetry.OutcomeUnknown
	default:
		return executiontelemetry.OutcomeCompleted
	}
}

func staleBrokerEvent(order executionmodel.Order, event executionbroker.Event) bool {
	if event.CumulativeFilled < order.Spec().FilledQuantity {
		return true
	}
	switch order.Spec().State {
	case executionmodel.OrderFilled:
		return event.Type != executionmodel.ReportFill
	case executionmodel.OrderPartiallyFilled:
		return event.Type == executionmodel.ReportAcknowledged || event.Type == executionmodel.ReportSubmitted
	default:
		return false
	}
}

func (runner *Coordinator) RequestCancel(ctx context.Context, id executionmodel.OrderID, logicalTime time.Time) (OrderReceipt, error) {
	order, err := runner.repository.Order(ctx, id)
	if err != nil {
		return OrderReceipt{}, err
	}
	if !runner.reserveOrder(id) {
		return OrderReceipt{}, ErrOrderBusy
	}
	switch order.Spec().State {
	case executionmodel.OrderSubmitted, executionmodel.OrderAcknowledged, executionmodel.OrderPartiallyFilled:
	default:
		runner.releaseOrder(id)
		return OrderReceipt{id, order.Spec().State}, executionmodel.ErrInvalidTransition
	}
	order, err = runner.publishInternal(ctx, order, executionmodel.ReportCancelPending, executionmodel.ReasonCancellationRequested, logicalTime, "cancel-"+fmt.Sprint(order.Spec().Revision))
	if err != nil {
		runner.releaseOrder(id)
		return OrderReceipt{}, err
	}
	_, err = runner.invokeCancel(ctx, executionbroker.CancellationRequest{OrderID: id, ClientOrderID: order.ClientOrderID(), BrokerOrderID: order.Spec().BrokerOrderID, RequestedAt: logicalTime.UTC()})
	if err != nil {
		order, publishErr := runner.publishInternal(ctx, order, executionmodel.ReportUnknown, executionmodel.ReasonSubmissionOutcomeUnknown, logicalTime, "cancel-unknown-"+fmt.Sprint(order.Spec().Revision))
		runner.releaseOrder(id)
		if publishErr != nil {
			runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationCancellation, Outcome: executiontelemetry.OutcomeInvalid, OrderID: id.String(), Occurred: logicalTime.UTC()})
			return OrderReceipt{}, publishErr
		}
		runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationCancellation, Outcome: executiontelemetry.OutcomeUnknown, OrderID: id.String(), Occurred: logicalTime.UTC()})
		return OrderReceipt{id, order.Spec().State}, ErrUnknownOutcome
	}
	runner.releaseOrder(id)
	if err = runner.DrainEvents(ctx, logicalTime); err != nil {
		return OrderReceipt{}, err
	}
	order, _ = runner.repository.Order(ctx, id)
	runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationCancellation, Outcome: executiontelemetry.OutcomeCancelled, OrderID: id.String(), Occurred: logicalTime.UTC()})
	return OrderReceipt{id, order.Spec().State}, nil
}

func (runner *Coordinator) reservePlan(id executionmodel.OrderPlanID, trigger string) Outcome {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.closed {
		return OutcomeShutdown
	}
	if existing, ok := runner.plans[id]; ok {
		if existing == trigger {
			return OutcomeDuplicateInProgress
		}
		return OutcomeBusy
	}
	runner.plans[id] = trigger
	runner.wait.Add(1)
	return ""
}
func (runner *Coordinator) releasePlan(id executionmodel.OrderPlanID) {
	runner.mu.Lock()
	delete(runner.plans, id)
	runner.mu.Unlock()
}
func (runner *Coordinator) reserveOrder(id executionmodel.OrderID) bool {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.closed || runner.orders[id] {
		return false
	}
	runner.orders[id] = true
	return true
}
func (runner *Coordinator) releaseOrder(id executionmodel.OrderID) {
	runner.mu.Lock()
	delete(runner.orders, id)
	runner.mu.Unlock()
}
func (runner *Coordinator) Cursor() executionbroker.EventCursor {
	runner.eventMu.Lock()
	defer runner.eventMu.Unlock()
	return runner.cursor
}
func (runner *Coordinator) RestoreCursor(value executionbroker.EventCursor) {
	runner.eventMu.Lock()
	runner.cursor = value
	runner.eventMu.Unlock()
}
func (runner *Coordinator) Shutdown(ctx context.Context) error {
	started := time.Now()
	runner.stopOnce.Do(func() {
		runner.mu.Lock()
		runner.closed = true
		runner.cancel()
		runner.mu.Unlock()
		go func() {
			runner.wait.Wait()
			close(runner.stopped)
		}()
	})
	select {
	case <-runner.stopped:
		runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationShutdown, Outcome: executiontelemetry.OutcomeShutdown, Duration: time.Since(started)})
		return nil
	case <-ctx.Done():
		runner.telemetry.Record(executiontelemetry.Event{Operation: executiontelemetry.OperationShutdown, Outcome: executiontelemetry.OutcomeFailed, Duration: time.Since(started)})
		return ctx.Err()
	}
}

func (runner *Coordinator) Health() executionhealth.Coordinator {
	runner.mu.Lock()
	value := executionhealth.Coordinator{Available: true, Closed: runner.closed, InFlightPlans: len(runner.plans), KeyedOrders: len(runner.orders), MaximumPlans: runner.config.MaxConcurrentPlans, BrokerTimeout: runner.config.BrokerTimeout}
	runner.mu.Unlock()
	runner.eventMu.Lock()
	value.Cursor = uint64(runner.cursor)
	runner.eventMu.Unlock()
	return value
}

func SortedOrderIDs(values []executionmodel.Order) []executionmodel.OrderID {
	ids := make([]executionmodel.OrderID, len(values))
	for index, value := range values {
		ids[index] = value.ID()
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}
