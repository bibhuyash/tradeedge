// Package opshttp exposes bounded, read-only execution diagnostics.
package opshttp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	executionhealth "github.com/bibhuyash/tradeedge/internal/execution/health"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	executiontelemetry "github.com/bibhuyash/tradeedge/internal/execution/telemetry"
)

type Repository interface {
	RecentPlans(context.Context, int) ([]executionmodel.OrderPlan, error)
	RecentOrders(context.Context, executionmodel.OrderState, int) ([]executionmodel.Order, error)
	RecentReports(context.Context, int) ([]executionmodel.ExecutionReport, error)
	RecentFills(context.Context, int) ([]executionmodel.Fill, error)
}
type CoordinatorHealth interface {
	Health() executionhealth.Coordinator
}
type OMSHealth interface{ Health() executionhealth.OMS }
type PaperHealth interface {
	Health() executionhealth.PaperBroker
}
type ReconciliationHealth interface {
	Health() executionhealth.Reconciliation
}
type AuditSource interface {
	Snapshot(int) executiontelemetry.Snapshot
}

type Dependencies struct {
	Repository     Repository
	Coordinator    CoordinatorHealth
	OMS            OMSHealth
	PaperBroker    PaperHealth
	Reconciliation ReconciliationHealth
	Audit          AuditSource
	Timeout        time.Duration
}

type Handler struct{ dependencies Dependencies }

func New(dependencies Dependencies) http.Handler {
	if dependencies.Timeout <= 0 || dependencies.Timeout > 10*time.Second {
		dependencies.Timeout = 2 * time.Second
	}
	return Handler{dependencies}
}

func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		write(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	limit, ok := parseLimit(request)
	if !ok {
		write(writer, http.StatusBadRequest, map[string]string{"error": "invalid_limit"})
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), handler.dependencies.Timeout)
	defer cancel()
	path := strings.TrimSuffix(request.URL.Path, "/")
	switch path {
	case "/api/v1/execution/health":
		handler.health(writer)
	case "/api/v1/execution/plans":
		handler.plans(ctx, writer, limit)
	case "/api/v1/execution/orders", "/api/v1/execution/unknown":
		handler.orders(ctx, writer, request, limit, path)
	case "/api/v1/execution/reports":
		handler.reports(ctx, writer, request, limit)
	case "/api/v1/execution/fills":
		handler.fills(ctx, writer, request, limit)
	case "/api/v1/execution/reconciliation":
		handler.reconciliation(writer)
	case "/api/v1/execution/paper-broker":
		handler.paper(writer)
	case "/api/v1/execution/coordinator":
		handler.coordinator(writer)
	case "/api/v1/execution/audit":
		handler.audit(writer, limit)
	default:
		write(writer, http.StatusNotFound, map[string]string{"error": "not_found"})
	}
}

func (handler Handler) health(writer http.ResponseWriter) {
	if handler.dependencies.Coordinator == nil || handler.dependencies.OMS == nil || handler.dependencies.PaperBroker == nil || handler.dependencies.Reconciliation == nil {
		write(writer, http.StatusServiceUnavailable, map[string]string{"error": "health_source_unavailable"})
		return
	}
	oms := handler.dependencies.OMS.Health()
	snapshot := executionhealth.Aggregate(handler.dependencies.Coordinator.Health(), oms, handler.dependencies.PaperBroker.Health(), handler.dependencies.Reconciliation.Health(), oms.UnknownOrders)
	status := http.StatusOK
	if snapshot.State == executionhealth.StateBlocked || snapshot.State == executionhealth.StateUnavailable {
		status = http.StatusServiceUnavailable
	}
	write(writer, status, snapshot)
}

func (handler Handler) plans(ctx context.Context, writer http.ResponseWriter, limit int) {
	if handler.dependencies.Repository == nil {
		write(writer, http.StatusServiceUnavailable, map[string]string{"error": "repository_unavailable"})
		return
	}
	values, err := handler.dependencies.Repository.RecentPlans(ctx, limit)
	if err != nil {
		repositoryError(writer)
		return
	}
	items := make([]map[string]any, len(values))
	for index, value := range values {
		spec := value.Spec()
		items[index] = map[string]any{"plan_id": value.ID().String(), "intent_id": value.IntentID().String(), "leg_count": len(value.Legs()), "created_at": spec.CreatedAt, "expires_at": spec.ExpiresAt}
	}
	write(writer, http.StatusOK, map[string]any{"items": items, "limit": limit})
}

func (handler Handler) orders(ctx context.Context, writer http.ResponseWriter, request *http.Request, limit int, path string) {
	state := executionmodel.OrderState(request.URL.Query().Get("state"))
	if path == "/api/v1/execution/unknown" {
		state = executionmodel.OrderUnknown
	}
	if state != "" && state.Validate() != nil {
		write(writer, http.StatusBadRequest, map[string]string{"error": "invalid_state"})
		return
	}
	if handler.dependencies.Repository == nil {
		repositoryError(writer)
		return
	}
	planFilter := request.URL.Query().Get("plan_id")
	if planFilter != "" && !validDigest(planFilter) {
		write(writer, http.StatusBadRequest, map[string]string{"error": "invalid_plan_id"})
		return
	}
	readLimit := limit
	if planFilter != "" {
		readLimit = 100
	}
	values, err := handler.dependencies.Repository.RecentOrders(ctx, state, readLimit)
	if err != nil {
		repositoryError(writer)
		return
	}
	items := make([]map[string]any, 0, len(values))
	for _, value := range values {
		spec := value.Spec()
		if planFilter != "" && spec.PlanID.String() != planFilter {
			continue
		}
		items = append(items, map[string]any{"order_id": value.ID().String(), "client_order_id": value.ClientOrderID().String(), "plan_id": spec.PlanID.String(), "broker_order_id": spec.BrokerOrderID, "state": spec.State, "revision": spec.Revision, "side": spec.Leg.Side, "instrument_id": spec.Leg.InstrumentID.String(), "quantity": spec.Leg.Quantity.Int64(), "filled_quantity": spec.FilledQuantity, "limit_price_minor": spec.Leg.LimitPrice.MinorUnits(), "currency": spec.Leg.LimitPrice.Currency(), "created_at": spec.CreatedAt, "updated_at": spec.UpdatedAt})
		if len(items) == limit {
			break
		}
	}
	write(writer, http.StatusOK, map[string]any{"items": items, "limit": limit})
}

func (handler Handler) reports(ctx context.Context, writer http.ResponseWriter, request *http.Request, limit int) {
	if handler.dependencies.Repository == nil {
		repositoryError(writer)
		return
	}
	filter := request.URL.Query().Get("order_id")
	if filter != "" && !validDigest(filter) {
		write(writer, http.StatusBadRequest, map[string]string{"error": "invalid_order_id"})
		return
	}
	readLimit := limit
	if filter != "" {
		readLimit = 100
	}
	values, err := handler.dependencies.Repository.RecentReports(ctx, readLimit)
	if err != nil {
		repositoryError(writer)
		return
	}
	items := make([]map[string]any, 0, len(values))
	for _, value := range values {
		spec := value.Spec()
		if filter != "" && spec.OrderID.String() != filter {
			continue
		}
		items = append(items, map[string]any{"report_id": value.ID().String(), "order_id": spec.OrderID.String(), "type": spec.Type, "reason": spec.Reason, "source": spec.Source, "broker_order_id": spec.BrokerOrderID, "cumulative_filled": spec.CumulativeFilled, "occurred_at": spec.OccurredAt, "received_at": spec.ReceivedAt})
		if len(items) == limit {
			break
		}
	}
	write(writer, http.StatusOK, map[string]any{"items": items, "limit": limit})
}

func (handler Handler) fills(ctx context.Context, writer http.ResponseWriter, request *http.Request, limit int) {
	if handler.dependencies.Repository == nil {
		repositoryError(writer)
		return
	}
	filter := request.URL.Query().Get("order_id")
	if filter != "" && !validDigest(filter) {
		write(writer, http.StatusBadRequest, map[string]string{"error": "invalid_order_id"})
		return
	}
	readLimit := limit
	if filter != "" {
		readLimit = 100
	}
	values, err := handler.dependencies.Repository.RecentFills(ctx, readLimit)
	if err != nil {
		repositoryError(writer)
		return
	}
	items := make([]map[string]any, 0, len(values))
	for _, value := range values {
		spec := value.Spec()
		if filter != "" && spec.OrderID.String() != filter {
			continue
		}
		items = append(items, map[string]any{"fill_id": value.ID().String(), "order_id": spec.OrderID.String(), "report_id": spec.ReportID.String(), "quantity": spec.Quantity.Int64(), "price_minor": spec.Price.MinorUnits(), "currency": spec.Price.Currency(), "occurred_at": spec.OccurredAt})
		if len(items) == limit {
			break
		}
	}
	write(writer, http.StatusOK, map[string]any{"items": items, "limit": limit})
}

func (handler Handler) reconciliation(writer http.ResponseWriter) {
	if handler.dependencies.Reconciliation == nil {
		write(writer, http.StatusServiceUnavailable, map[string]string{"error": "reconciliation_unavailable"})
		return
	}
	write(writer, http.StatusOK, handler.dependencies.Reconciliation.Health())
}
func (handler Handler) paper(writer http.ResponseWriter) {
	if handler.dependencies.PaperBroker == nil {
		write(writer, http.StatusServiceUnavailable, map[string]string{"error": "paper_broker_unavailable"})
		return
	}
	write(writer, http.StatusOK, handler.dependencies.PaperBroker.Health())
}
func (handler Handler) coordinator(writer http.ResponseWriter) {
	if handler.dependencies.Coordinator == nil {
		write(writer, http.StatusServiceUnavailable, map[string]string{"error": "coordinator_unavailable"})
		return
	}
	write(writer, http.StatusOK, handler.dependencies.Coordinator.Health())
}
func (handler Handler) audit(writer http.ResponseWriter, limit int) {
	if handler.dependencies.Audit == nil {
		write(writer, http.StatusServiceUnavailable, map[string]string{"error": "audit_unavailable"})
		return
	}
	write(writer, http.StatusOK, handler.dependencies.Audit.Snapshot(limit))
}

func parseLimit(request *http.Request) (int, bool) {
	if request.URL.Query().Get("limit") == "" {
		return 50, true
	}
	value, err := strconv.Atoi(request.URL.Query().Get("limit"))
	return value, err == nil && value > 0 && value <= 100
}
func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func repositoryError(writer http.ResponseWriter) {
	write(writer, http.StatusServiceUnavailable, map[string]string{"error": "repository_unavailable"})
}
func write(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
