package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
)

var (
	ErrNotFound           = errors.New("OMS record not found")
	ErrIdentityCollision  = errors.New("OMS identity collision")
	ErrCapacityExhausted  = errors.New("OMS capacity exhausted")
	ErrStaleOrderRevision = errors.New("stale order revision")
	ErrInvalidPublication = errors.New("invalid OMS publication")
	ErrCorruptCheckpoint  = errors.New("corrupt OMS checkpoint")
	ErrInternal           = errors.New("OMS internal failure")
)

type IdentityCollisionError struct{ Kind, Identity string }

func (value *IdentityCollisionError) Error() string {
	return fmt.Sprintf("%v: %s %s", ErrIdentityCollision, value.Kind, value.Identity)
}
func (value *IdentityCollisionError) Unwrap() error { return ErrIdentityCollision }

type RevisionConflictError struct {
	OrderID          executionmodel.OrderID
	Expected, Actual executionmodel.OrderRevision
}

func (value *RevisionConflictError) Error() string {
	return fmt.Sprintf("%v for %s: expected %d, actual %d", ErrStaleOrderRevision, value.OrderID, value.Expected, value.Actual)
}
func (value *RevisionConflictError) Unwrap() error { return ErrStaleOrderRevision }

type RegistrationStatus string

const (
	RegistrationCommitted  RegistrationStatus = "COMMITTED"
	RegistrationIdempotent RegistrationStatus = "IDEMPOTENT_REPLAY"
)

type RegistrationOutcome struct{ Status RegistrationStatus }

type OrderCheckpoint struct {
	Order              executionmodel.Order
	OrderChecksum      executionmodel.StateChecksum
	ParentChecksum     executionmodel.StateChecksum
	ReportID           executionmodel.ExecutionReportID
	FillID             executionmodel.FillID
	CheckpointChecksum executionmodel.StateChecksum
	canonical          []byte
}

func NewOrderCheckpoint(value OrderCheckpoint) (OrderCheckpoint, error) {
	if value.Order.IsZero() {
		return OrderCheckpoint{}, ErrCorruptCheckpoint
	}
	orderChecksum, _ := executionmodel.NewStateChecksum(value.Order.CanonicalJSON())
	if !value.OrderChecksum.IsZero() && value.OrderChecksum != orderChecksum {
		return OrderCheckpoint{}, ErrCorruptCheckpoint
	}
	value.OrderChecksum = orderChecksum
	genesis := value.Order.Spec().Revision == 1
	if genesis {
		if !value.ParentChecksum.IsZero() || !value.ReportID.IsZero() || !value.FillID.IsZero() {
			return OrderCheckpoint{}, ErrCorruptCheckpoint
		}
	} else if value.ParentChecksum.IsZero() || value.ReportID.IsZero() {
		return OrderCheckpoint{}, ErrCorruptCheckpoint
	}
	raw, err := json.Marshal(struct {
		Order                                           json.RawMessage
		OrderChecksum, ParentChecksum, ReportID, FillID string
	}{
		value.Order.CanonicalJSON(), value.OrderChecksum.String(), value.ParentChecksum.String(), value.ReportID.String(), value.FillID.String()})
	if err != nil {
		return OrderCheckpoint{}, ErrCorruptCheckpoint
	}
	checksum, _ := executionmodel.NewStateChecksum(raw)
	if !value.CheckpointChecksum.IsZero() && value.CheckpointChecksum != checksum {
		return OrderCheckpoint{}, ErrCorruptCheckpoint
	}
	value.CheckpointChecksum, value.canonical = checksum, raw
	return value, nil
}
func (value OrderCheckpoint) CanonicalJSON() []byte { return append([]byte(nil), value.canonical...) }

type OrderPublication struct {
	PublicationID       executionmodel.PublicationID
	ExpectedRevision    executionmodel.OrderRevision
	ExpectedCheckpoint  executionmodel.StateChecksum
	Report              executionmodel.ExecutionReport
	Fill                *executionmodel.Fill
	NextCheckpoint      OrderCheckpoint
	PublicationChecksum executionmodel.StateChecksum
	canonical           []byte
}

func NewOrderPublication(value OrderPublication) (OrderPublication, error) {
	if value.PublicationID.IsZero() || value.ExpectedRevision.Validate() != nil || value.ExpectedCheckpoint.IsZero() ||
		value.Report.ID().IsZero() || value.NextCheckpoint.Order.IsZero() ||
		value.NextCheckpoint.Order.ID() != value.Report.Spec().OrderID ||
		value.NextCheckpoint.Order.Spec().Revision != value.ExpectedRevision+1 ||
		value.NextCheckpoint.ParentChecksum != value.ExpectedCheckpoint ||
		value.NextCheckpoint.ReportID != value.Report.ID() {
		return OrderPublication{}, ErrInvalidPublication
	}
	fill := json.RawMessage("null")
	if value.Fill != nil {
		if value.Fill.Spec().OrderID != value.Report.Spec().OrderID || value.Fill.Spec().ReportID != value.Report.ID() || value.NextCheckpoint.FillID != value.Fill.ID() {
			return OrderPublication{}, ErrInvalidPublication
		}
		fill = value.Fill.CanonicalJSON()
	} else if !value.NextCheckpoint.FillID.IsZero() {
		return OrderPublication{}, ErrInvalidPublication
	}
	raw, err := json.Marshal(struct {
		PublicationID, ExpectedCheckpoint string
		ExpectedRevision                  uint64
		Report, Fill, NextCheckpoint      json.RawMessage
	}{
		value.PublicationID.String(), value.ExpectedCheckpoint.String(), uint64(value.ExpectedRevision), value.Report.CanonicalJSON(), fill, value.NextCheckpoint.CanonicalJSON()})
	if err != nil {
		return OrderPublication{}, ErrInvalidPublication
	}
	checksum, _ := executionmodel.NewStateChecksum(raw)
	if !value.PublicationChecksum.IsZero() && value.PublicationChecksum != checksum {
		return OrderPublication{}, ErrInvalidPublication
	}
	value.PublicationChecksum, value.canonical = checksum, raw
	return value, nil
}
func (value OrderPublication) CanonicalJSON() []byte { return append([]byte(nil), value.canonical...) }

type PublicationReceipt struct {
	Status              RegistrationStatus
	PublicationID       executionmodel.PublicationID
	OrderID             executionmodel.OrderID
	Revision            executionmodel.OrderRevision
	ReportID            executionmodel.ExecutionReportID
	FillID              executionmodel.FillID
	CheckpointChecksum  executionmodel.StateChecksum
	PublicationChecksum executionmodel.StateChecksum
}

type OMSRepository interface {
	RegisterPlan(context.Context, executionmodel.ExecutionIntent, executionmodel.OrderPlan, []executionmodel.Order) (RegistrationOutcome, error)
	RestorePlan(context.Context, executionmodel.ExecutionIntent, executionmodel.OrderPlan, []OrderCheckpoint, []executionmodel.ExecutionReport, []executionmodel.Fill) (RegistrationOutcome, error)
	RestoreOrder(context.Context, OrderCheckpoint, []executionmodel.ExecutionReport, []executionmodel.Fill) (RegistrationOutcome, error)
	CurrentOrderCheckpoint(context.Context, executionmodel.OrderID) (OrderCheckpoint, error)
	OrderCheckpoint(context.Context, executionmodel.OrderID, executionmodel.OrderRevision) (OrderCheckpoint, error)
	CommittedPublication(context.Context, executionmodel.PublicationID) (PublicationReceipt, error)
	PublishOrderEvent(context.Context, OrderPublication) (PublicationReceipt, error)
	Intent(context.Context, executionmodel.ExecutionIntentID) (executionmodel.ExecutionIntent, error)
	Plan(context.Context, executionmodel.OrderPlanID) (executionmodel.OrderPlan, error)
	Order(context.Context, executionmodel.OrderID) (executionmodel.Order, error)
	OrderByClientOrderID(context.Context, executionmodel.ClientOrderID) (executionmodel.Order, error)
	OrdersForPlan(context.Context, executionmodel.OrderPlanID) ([]executionmodel.Order, error)
	NonTerminalOrders(context.Context, int) ([]executionmodel.Order, error)
	Reports(context.Context, executionmodel.OrderID) ([]executionmodel.ExecutionReport, error)
	Fills(context.Context, executionmodel.OrderID) ([]executionmodel.Fill, error)
}
