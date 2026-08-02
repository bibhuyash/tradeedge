package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"sort"
	"sync"

	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	executionstorage "github.com/bibhuyash/tradeedge/internal/execution/storage"
)

type Limits struct{ Plans, Orders, Publications, Reports, Fills int }

func DefaultLimits() Limits { return Limits{256, 2048, 20000, 50000, 50000} }

type storedPlan struct {
	intent    executionmodel.ExecutionIntent
	plan      executionmodel.OrderPlan
	canonical []byte
}
type storedPublication struct {
	value     executionstorage.OrderPublication
	receipt   executionstorage.PublicationReceipt
	canonical []byte
}

type Store struct {
	mu               sync.RWMutex
	limits           Limits
	plans            map[executionmodel.OrderPlanID]storedPlan
	intents          map[executionmodel.ExecutionIntentID]executionmodel.ExecutionIntent
	current          map[executionmodel.OrderID]executionmodel.OrderRevision
	checkpoints      map[executionmodel.OrderID]map[executionmodel.OrderRevision]executionstorage.OrderCheckpoint
	publications     map[executionmodel.PublicationID]storedPublication
	reports          map[executionmodel.ExecutionReportID]executionmodel.ExecutionReport
	reportReceipts   map[executionmodel.ExecutionReportID]executionstorage.PublicationReceipt
	fills            map[executionmodel.FillID]executionmodel.Fill
	failBeforeCommit bool
}

func NewStore() *Store { return NewStoreWithLimits(DefaultLimits()) }
func NewStoreWithLimits(limits Limits) *Store {
	if limits.Plans <= 0 || limits.Orders <= 0 || limits.Publications <= 0 || limits.Reports <= 0 || limits.Fills <= 0 {
		limits = DefaultLimits()
	}
	return &Store{limits: limits, plans: map[executionmodel.OrderPlanID]storedPlan{}, intents: map[executionmodel.ExecutionIntentID]executionmodel.ExecutionIntent{},
		current: map[executionmodel.OrderID]executionmodel.OrderRevision{}, checkpoints: map[executionmodel.OrderID]map[executionmodel.OrderRevision]executionstorage.OrderCheckpoint{},
		publications: map[executionmodel.PublicationID]storedPublication{}, reports: map[executionmodel.ExecutionReportID]executionmodel.ExecutionReport{},
		reportReceipts: map[executionmodel.ExecutionReportID]executionstorage.PublicationReceipt{}, fills: map[executionmodel.FillID]executionmodel.Fill{}}
}

func (store *Store) SetFailBeforeCommitForTest(value bool) {
	store.mu.Lock()
	store.failBeforeCommit = value
	store.mu.Unlock()
}

func (store *Store) RegisterPlan(ctx context.Context, intent executionmodel.ExecutionIntent, plan executionmodel.OrderPlan, orders []executionmodel.Order) (executionstorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return executionstorage.RegistrationOutcome{}, err
	}
	if intent.IsZero() || plan.IsZero() || plan.IntentID() != intent.ID() || len(orders) == 0 {
		return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
	}
	legOrders := make(map[executionmodel.OrderLegID]struct{}, len(orders))
	checkpoints := make([]executionstorage.OrderCheckpoint, len(orders))
	for i, order := range orders {
		if order.IsZero() || order.Spec().PlanID != plan.ID() || order.Spec().Revision != 1 {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
		}
		if _, duplicate := legOrders[order.Spec().Leg.ID]; duplicate {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
		}
		legOrders[order.Spec().Leg.ID] = struct{}{}
		checkpoint, err := executionstorage.NewOrderCheckpoint(executionstorage.OrderCheckpoint{Order: order})
		if err != nil {
			return executionstorage.RegistrationOutcome{}, err
		}
		checkpoints[i] = checkpoint
	}
	if len(legOrders) != len(plan.Legs()) {
		return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
	}
	canonical := canonicalPlanBundle(intent, plan, checkpoints, nil, nil)
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return executionstorage.RegistrationOutcome{}, err
	}
	if existing, found := store.plans[plan.ID()]; found {
		if bytes.Equal(existing.canonical, canonical) {
			return executionstorage.RegistrationOutcome{Status: executionstorage.RegistrationIdempotent}, nil
		}
		return executionstorage.RegistrationOutcome{}, &executionstorage.IdentityCollisionError{Kind: "plan", Identity: plan.ID().String()}
	}
	if len(store.plans) >= store.limits.Plans || len(store.current)+len(orders) > store.limits.Orders {
		return executionstorage.RegistrationOutcome{}, executionstorage.ErrCapacityExhausted
	}
	for _, checkpoint := range checkpoints {
		if _, exists := store.current[checkpoint.Order.ID()]; exists {
			return executionstorage.RegistrationOutcome{}, &executionstorage.IdentityCollisionError{Kind: "order", Identity: checkpoint.Order.ID().String()}
		}
	}
	store.plans[plan.ID()] = storedPlan{intent, plan, append([]byte(nil), canonical...)}
	store.intents[intent.ID()] = intent
	for _, checkpoint := range checkpoints {
		store.current[checkpoint.Order.ID()] = 1
		store.checkpoints[checkpoint.Order.ID()] = map[executionmodel.OrderRevision]executionstorage.OrderCheckpoint{1: checkpoint}
	}
	return executionstorage.RegistrationOutcome{Status: executionstorage.RegistrationCommitted}, nil
}

func (store *Store) RestoreOrder(ctx context.Context, checkpoint executionstorage.OrderCheckpoint, reports []executionmodel.ExecutionReport, fills []executionmodel.Fill) (executionstorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return executionstorage.RegistrationOutcome{}, err
	}
	validated, err := executionstorage.NewOrderCheckpoint(checkpoint)
	if err != nil {
		return executionstorage.RegistrationOutcome{}, err
	}
	fillTotal := int64(0)
	reportMap := map[executionmodel.ExecutionReportID]executionmodel.ExecutionReport{}
	for _, report := range reports {
		if report.Spec().OrderID != validated.Order.ID() {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
		}
		if existing, ok := reportMap[report.ID()]; ok && !bytes.Equal(existing.CanonicalJSON(), report.CanonicalJSON()) {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrIdentityCollision
		}
		reportMap[report.ID()] = report
	}
	fillMap := map[executionmodel.FillID]executionmodel.Fill{}
	for _, fill := range fills {
		if fill.Spec().OrderID != validated.Order.ID() {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
		}
		if _, ok := reportMap[fill.Spec().ReportID]; !ok {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
		}
		if existing, ok := fillMap[fill.ID()]; ok && !bytes.Equal(existing.CanonicalJSON(), fill.CanonicalJSON()) {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrIdentityCollision
		}
		fillMap[fill.ID()] = fill
		if fill.Spec().Quantity.Int64() > math.MaxInt64-fillTotal {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
		}
		fillTotal += fill.Spec().Quantity.Int64()
	}
	if fillTotal != validated.Order.Spec().FilledQuantity {
		return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	stored, ok := store.plans[validated.Order.Spec().PlanID]
	if !ok {
		return executionstorage.RegistrationOutcome{}, executionstorage.ErrNotFound
	}
	legFound := false
	for _, leg := range stored.plan.Legs() {
		if leg.ID == validated.Order.Spec().Leg.ID {
			legFound = true
			break
		}
	}
	if !legFound {
		return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
	}
	if _, ok := store.current[validated.Order.ID()]; ok {
		return executionstorage.RegistrationOutcome{}, &executionstorage.IdentityCollisionError{Kind: "order_restore", Identity: validated.Order.ID().String()}
	}
	if len(store.current) >= store.limits.Orders || len(store.reports)+len(reportMap) > store.limits.Reports || len(store.fills)+len(fillMap) > store.limits.Fills {
		return executionstorage.RegistrationOutcome{}, executionstorage.ErrCapacityExhausted
	}
	store.current[validated.Order.ID()] = validated.Order.Spec().Revision
	store.checkpoints[validated.Order.ID()] = map[executionmodel.OrderRevision]executionstorage.OrderCheckpoint{validated.Order.Spec().Revision: validated}
	for id, value := range reportMap {
		store.reports[id] = value
	}
	for id, value := range fillMap {
		store.fills[id] = value
	}
	return executionstorage.RegistrationOutcome{Status: executionstorage.RegistrationCommitted}, nil
}

func (store *Store) RestorePlan(ctx context.Context, intent executionmodel.ExecutionIntent, plan executionmodel.OrderPlan,
	checkpoints []executionstorage.OrderCheckpoint, reports []executionmodel.ExecutionReport, fills []executionmodel.Fill) (executionstorage.RegistrationOutcome, error) {
	if err := ctx.Err(); err != nil {
		return executionstorage.RegistrationOutcome{}, err
	}
	if intent.IsZero() || plan.IsZero() || plan.IntentID() != intent.ID() || len(checkpoints) != len(plan.Legs()) {
		return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
	}
	validated := make([]executionstorage.OrderCheckpoint, len(checkpoints))
	orderIDs := map[executionmodel.OrderID]struct{}{}
	legIDs := map[executionmodel.OrderLegID]struct{}{}
	for index, checkpoint := range checkpoints {
		value, err := executionstorage.NewOrderCheckpoint(checkpoint)
		if err != nil || value.Order.Spec().PlanID != plan.ID() {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
		}
		if _, duplicate := orderIDs[value.Order.ID()]; duplicate {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
		}
		orderIDs[value.Order.ID()] = struct{}{}
		legIDs[value.Order.Spec().Leg.ID] = struct{}{}
		validated[index] = value
	}
	if len(legIDs) != len(plan.Legs()) {
		return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
	}
	reportMap := map[executionmodel.ExecutionReportID]executionmodel.ExecutionReport{}
	fillTotals := map[executionmodel.OrderID]int64{}
	for _, report := range reports {
		if _, ok := orderIDs[report.Spec().OrderID]; !ok {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
		}
		if existing, ok := reportMap[report.ID()]; ok && !bytes.Equal(existing.CanonicalJSON(), report.CanonicalJSON()) {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrIdentityCollision
		}
		reportMap[report.ID()] = report
	}
	fillMap := map[executionmodel.FillID]executionmodel.Fill{}
	for _, fill := range fills {
		if _, ok := orderIDs[fill.Spec().OrderID]; !ok {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
		}
		if _, ok := reportMap[fill.Spec().ReportID]; !ok {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
		}
		if existing, ok := fillMap[fill.ID()]; ok && !bytes.Equal(existing.CanonicalJSON(), fill.CanonicalJSON()) {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrIdentityCollision
		}
		fillMap[fill.ID()] = fill
		currentTotal := fillTotals[fill.Spec().OrderID]
		if fill.Spec().Quantity.Int64() > math.MaxInt64-currentTotal {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
		}
		fillTotals[fill.Spec().OrderID] = currentTotal + fill.Spec().Quantity.Int64()
	}
	for _, checkpoint := range validated {
		if fillTotals[checkpoint.Order.ID()] != checkpoint.Order.Spec().FilledQuantity {
			return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
		}
	}
	for _, checkpoint := range validated {
		if checkpoint.Order.Spec().Revision > 1 {
			if _, ok := reportMap[checkpoint.ReportID]; !ok {
				return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
			}
			if !checkpoint.FillID.IsZero() {
				if _, ok := fillMap[checkpoint.FillID]; !ok {
					return executionstorage.RegistrationOutcome{}, executionstorage.ErrCorruptCheckpoint
				}
			}
		}
	}
	canonical := canonicalPlanBundle(intent, plan, validated, reports, fills)
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return executionstorage.RegistrationOutcome{}, err
	}
	if existing, ok := store.plans[plan.ID()]; ok {
		if bytes.Equal(existing.canonical, canonical) {
			return executionstorage.RegistrationOutcome{Status: executionstorage.RegistrationIdempotent}, nil
		}
		return executionstorage.RegistrationOutcome{}, &executionstorage.IdentityCollisionError{Kind: "plan_restore", Identity: plan.ID().String()}
	}
	if len(store.plans) >= store.limits.Plans || len(store.current)+len(validated) > store.limits.Orders || len(store.reports)+len(reportMap) > store.limits.Reports || len(store.fills)+len(fillMap) > store.limits.Fills {
		return executionstorage.RegistrationOutcome{}, executionstorage.ErrCapacityExhausted
	}
	store.plans[plan.ID()] = storedPlan{intent, plan, append([]byte(nil), canonical...)}
	store.intents[intent.ID()] = intent
	for _, checkpoint := range validated {
		store.current[checkpoint.Order.ID()] = checkpoint.Order.Spec().Revision
		store.checkpoints[checkpoint.Order.ID()] = map[executionmodel.OrderRevision]executionstorage.OrderCheckpoint{checkpoint.Order.Spec().Revision: checkpoint}
	}
	for id, report := range reportMap {
		store.reports[id] = report
	}
	for id, fill := range fillMap {
		store.fills[id] = fill
	}
	return executionstorage.RegistrationOutcome{Status: executionstorage.RegistrationCommitted}, nil
}

func (store *Store) PublishOrderEvent(ctx context.Context, publication executionstorage.OrderPublication) (executionstorage.PublicationReceipt, error) {
	if err := ctx.Err(); err != nil {
		return executionstorage.PublicationReceipt{}, err
	}
	validated, err := executionstorage.NewOrderPublication(publication)
	if err != nil {
		return executionstorage.PublicationReceipt{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return executionstorage.PublicationReceipt{}, err
	}
	if existing, ok := store.publications[validated.PublicationID]; ok {
		if bytes.Equal(existing.canonical, validated.CanonicalJSON()) {
			receipt := existing.receipt
			receipt.Status = executionstorage.RegistrationIdempotent
			return receipt, nil
		}
		return executionstorage.PublicationReceipt{}, &executionstorage.IdentityCollisionError{Kind: "publication", Identity: validated.PublicationID.String()}
	}
	if existing, ok := store.reports[validated.Report.ID()]; ok {
		if !bytes.Equal(existing.CanonicalJSON(), validated.Report.CanonicalJSON()) {
			return executionstorage.PublicationReceipt{}, &executionstorage.IdentityCollisionError{Kind: "report", Identity: validated.Report.ID().String()}
		}
		receipt := store.reportReceipts[validated.Report.ID()]
		if validated.Fill == nil {
			if !receipt.FillID.IsZero() {
				return executionstorage.PublicationReceipt{}, &executionstorage.IdentityCollisionError{Kind: "report_fill", Identity: validated.Report.ID().String()}
			}
		} else {
			storedFill, found := store.fills[validated.Fill.ID()]
			if receipt.FillID != validated.Fill.ID() || !found || !bytes.Equal(storedFill.CanonicalJSON(), validated.Fill.CanonicalJSON()) {
				return executionstorage.PublicationReceipt{}, &executionstorage.IdentityCollisionError{Kind: "fill", Identity: validated.Fill.ID().String()}
			}
		}
		receipt.Status = executionstorage.RegistrationIdempotent
		return receipt, nil
	}
	orderID := validated.Report.Spec().OrderID
	actual, ok := store.current[orderID]
	if !ok {
		return executionstorage.PublicationReceipt{}, executionstorage.ErrNotFound
	}
	current := store.checkpoints[orderID][actual]
	if actual != validated.ExpectedRevision || current.CheckpointChecksum != validated.ExpectedCheckpoint {
		return executionstorage.PublicationReceipt{}, &executionstorage.RevisionConflictError{OrderID: orderID, Expected: validated.ExpectedRevision, Actual: actual}
	}
	next, err := executionmodel.ApplyExecutionReport(current.Order, validated.Report, validated.Fill)
	if err != nil {
		return executionstorage.PublicationReceipt{}, err
	}
	if !bytes.Equal(next.CanonicalJSON(), validated.NextCheckpoint.Order.CanonicalJSON()) || validated.NextCheckpoint.ParentChecksum != current.CheckpointChecksum {
		return executionstorage.PublicationReceipt{}, executionstorage.ErrInvalidPublication
	}
	if validated.Fill != nil {
		if existing, ok := store.fills[validated.Fill.ID()]; ok {
			if !bytes.Equal(existing.CanonicalJSON(), validated.Fill.CanonicalJSON()) {
				return executionstorage.PublicationReceipt{}, &executionstorage.IdentityCollisionError{Kind: "fill", Identity: validated.Fill.ID().String()}
			}
			return executionstorage.PublicationReceipt{}, executionstorage.ErrInvalidPublication
		}
	}
	if len(store.publications) >= store.limits.Publications || len(store.reports) >= store.limits.Reports || (validated.Fill != nil && len(store.fills) >= store.limits.Fills) {
		return executionstorage.PublicationReceipt{}, executionstorage.ErrCapacityExhausted
	}
	if store.failBeforeCommit {
		return executionstorage.PublicationReceipt{}, executionstorage.ErrInternal
	}
	receipt := executionstorage.PublicationReceipt{Status: executionstorage.RegistrationCommitted, PublicationID: validated.PublicationID, OrderID: orderID, Revision: next.Spec().Revision, ReportID: validated.Report.ID(), CheckpointChecksum: validated.NextCheckpoint.CheckpointChecksum, PublicationChecksum: validated.PublicationChecksum}
	if validated.Fill != nil {
		receipt.FillID = validated.Fill.ID()
		store.fills[validated.Fill.ID()] = *validated.Fill
	}
	store.current[orderID] = receipt.Revision
	store.checkpoints[orderID][receipt.Revision] = validated.NextCheckpoint
	store.reports[validated.Report.ID()] = validated.Report
	store.reportReceipts[validated.Report.ID()] = receipt
	store.publications[validated.PublicationID] = storedPublication{validated, receipt, validated.CanonicalJSON()}
	return receipt, nil
}

func (store *Store) CurrentOrderCheckpoint(ctx context.Context, id executionmodel.OrderID) (executionstorage.OrderCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return executionstorage.OrderCheckpoint{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	revision, ok := store.current[id]
	if !ok {
		return executionstorage.OrderCheckpoint{}, executionstorage.ErrNotFound
	}
	return store.checkpoints[id][revision], nil
}
func (store *Store) OrderCheckpoint(ctx context.Context, id executionmodel.OrderID, revision executionmodel.OrderRevision) (executionstorage.OrderCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return executionstorage.OrderCheckpoint{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	values, ok := store.checkpoints[id]
	if !ok {
		return executionstorage.OrderCheckpoint{}, executionstorage.ErrNotFound
	}
	value, ok := values[revision]
	if !ok {
		return executionstorage.OrderCheckpoint{}, executionstorage.ErrNotFound
	}
	return value, nil
}
func (store *Store) CommittedPublication(ctx context.Context, id executionmodel.PublicationID) (executionstorage.PublicationReceipt, error) {
	if err := ctx.Err(); err != nil {
		return executionstorage.PublicationReceipt{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.publications[id]
	if !ok {
		return executionstorage.PublicationReceipt{}, executionstorage.ErrNotFound
	}
	return value.receipt, nil
}
func (store *Store) Intent(ctx context.Context, id executionmodel.ExecutionIntentID) (executionmodel.ExecutionIntent, error) {
	if err := ctx.Err(); err != nil {
		return executionmodel.ExecutionIntent{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.intents[id]
	if !ok {
		return executionmodel.ExecutionIntent{}, executionstorage.ErrNotFound
	}
	return value, nil
}
func (store *Store) Plan(ctx context.Context, id executionmodel.OrderPlanID) (executionmodel.OrderPlan, error) {
	if err := ctx.Err(); err != nil {
		return executionmodel.OrderPlan{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, ok := store.plans[id]
	if !ok {
		return executionmodel.OrderPlan{}, executionstorage.ErrNotFound
	}
	return value.plan, nil
}
func (store *Store) Order(ctx context.Context, id executionmodel.OrderID) (executionmodel.Order, error) {
	checkpoint, err := store.CurrentOrderCheckpoint(ctx, id)
	if err != nil {
		return executionmodel.Order{}, err
	}
	return checkpoint.Order, nil
}
func (store *Store) OrderByClientOrderID(ctx context.Context, id executionmodel.ClientOrderID) (executionmodel.Order, error) {
	if err := ctx.Err(); err != nil {
		return executionmodel.Order{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	for orderID, revision := range store.current {
		value := store.checkpoints[orderID][revision].Order
		if value.ClientOrderID() == id {
			return value, nil
		}
	}
	return executionmodel.Order{}, executionstorage.ErrNotFound
}
func (store *Store) OrdersForPlan(ctx context.Context, id executionmodel.OrderPlanID) ([]executionmodel.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := []executionmodel.Order{}
	for orderID, revision := range store.current {
		value := store.checkpoints[orderID][revision].Order
		if value.Spec().PlanID == id {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID().String() < result[j].ID().String() })
	if len(result) == 0 {
		return nil, executionstorage.ErrNotFound
	}
	return result, nil
}
func (store *Store) NonTerminalOrders(ctx context.Context, limit int) ([]executionmodel.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		return nil, executionstorage.ErrInternal
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := []executionmodel.Order{}
	for orderID, revision := range store.current {
		value := store.checkpoints[orderID][revision].Order
		if !value.Spec().State.Terminal() {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID().String() < result[j].ID().String() })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
func (store *Store) Reports(ctx context.Context, id executionmodel.OrderID) ([]executionmodel.ExecutionReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := []executionmodel.ExecutionReport{}
	for _, value := range store.reports {
		if value.Spec().OrderID == id {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i].Spec(), result[j].Spec()
		if left.ReceivedAt.Equal(right.ReceivedAt) {
			return result[i].ID().String() < result[j].ID().String()
		}
		return left.ReceivedAt.Before(right.ReceivedAt)
	})
	return result, nil
}
func (store *Store) Fills(ctx context.Context, id executionmodel.OrderID) ([]executionmodel.Fill, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := []executionmodel.Fill{}
	for _, value := range store.fills {
		if value.Spec().OrderID == id {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i].Spec(), result[j].Spec()
		if left.OccurredAt.Equal(right.OccurredAt) {
			return result[i].ID().String() < result[j].ID().String()
		}
		return left.OccurredAt.Before(right.OccurredAt)
	})
	return result, nil
}

var _ executionstorage.OMSRepository = (*Store)(nil)

func canonicalPlanBundle(intent executionmodel.ExecutionIntent, plan executionmodel.OrderPlan,
	checkpoints []executionstorage.OrderCheckpoint, reports []executionmodel.ExecutionReport,
	fills []executionmodel.Fill) []byte {
	values := append([]executionstorage.OrderCheckpoint(nil), checkpoints...)
	sort.Slice(values, func(i, j int) bool { return values[i].Order.ID().String() < values[j].Order.ID().String() })
	wire := make([]json.RawMessage, len(values))
	for index, checkpoint := range values {
		wire[index] = checkpoint.CanonicalJSON()
	}
	reportValues := append([]executionmodel.ExecutionReport(nil), reports...)
	sort.Slice(reportValues, func(i, j int) bool { return reportValues[i].ID().String() < reportValues[j].ID().String() })
	reportWire := make([]json.RawMessage, len(reportValues))
	for index, report := range reportValues {
		reportWire[index] = report.CanonicalJSON()
	}
	fillValues := append([]executionmodel.Fill(nil), fills...)
	sort.Slice(fillValues, func(i, j int) bool { return fillValues[i].ID().String() < fillValues[j].ID().String() })
	fillWire := make([]json.RawMessage, len(fillValues))
	for index, fill := range fillValues {
		fillWire[index] = fill.CanonicalJSON()
	}
	raw, _ := json.Marshal(struct {
		Intent, Plan json.RawMessage
		Orders       []json.RawMessage
		Reports      []json.RawMessage
		Fills        []json.RawMessage
	}{intent.CanonicalJSON(), plan.CanonicalJSON(), wire, reportWire, fillWire})
	return raw
}
