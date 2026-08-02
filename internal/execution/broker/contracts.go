package broker

import (
	"context"
	"errors"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
)

var (
	ErrUnavailable      = errors.New("broker temporarily unavailable")
	ErrOutcomeUnknown   = errors.New("broker operation outcome unknown")
	ErrNotFound         = errors.New("broker order not found")
	ErrIdentityConflict = errors.New("broker client identity conflict")
	ErrInvalidRequest   = errors.New("invalid broker request")
)

type Submission struct {
	AttemptID     executionmodel.SubmissionAttemptID
	OrderID       executionmodel.OrderID
	ClientOrderID executionmodel.ClientOrderID
	InstrumentID  domain.InstrumentID
	Side          domain.Side
	Quantity      domain.Quantity
	LimitPrice    domain.Price
	SubmittedAt   time.Time
}

type SubmissionStatus string

const (
	SubmissionAccepted SubmissionStatus = "ACCEPTED"
	SubmissionRejected SubmissionStatus = "REJECTED"
)

type SubmissionResult struct {
	Status        SubmissionStatus
	BrokerOrderID string
}

type CancellationRequest struct {
	OrderID       executionmodel.OrderID
	ClientOrderID executionmodel.ClientOrderID
	BrokerOrderID string
	RequestedAt   time.Time
}
type CancellationResult struct{ Accepted bool }

type EventCursor uint64
type Event struct {
	EventID          string
	FillExecutionID  string
	ClientOrderID    executionmodel.ClientOrderID
	BrokerOrderID    string
	Type             executionmodel.ReportType
	Reason           executionmodel.TransitionReason
	CumulativeFilled int64
	FillQuantity     int64
	FillPrice        domain.Price
	OccurredAt       time.Time
}
type EventBatch struct {
	Events     []Event
	NextCursor EventCursor
	Complete   bool
}

type OrderSnapshot struct {
	ClientOrderID    executionmodel.ClientOrderID
	BrokerOrderID    string
	InstrumentID     domain.InstrumentID
	Side             domain.Side
	Quantity         domain.Quantity
	LimitPrice       domain.Price
	State            executionmodel.OrderState
	CumulativeFilled int64
	Fills            []FillSnapshot
	UpdatedAt        time.Time
}
type FillSnapshot struct {
	ExecutionID      string
	Quantity         int64
	CumulativeFilled int64
	Price            domain.Price
	OccurredAt       time.Time
}
type Snapshot struct {
	Orders     []OrderSnapshot
	Complete   bool
	Cursor     EventCursor
	ObservedAt time.Time
}

type Port interface {
	Submit(context.Context, Submission) (SubmissionResult, error)
	LookupByClientOrderID(context.Context, executionmodel.ClientOrderID) (OrderSnapshot, error)
	Cancel(context.Context, CancellationRequest) (CancellationResult, error)
	EventsAfter(context.Context, EventCursor, int) (EventBatch, error)
	Snapshot(context.Context, int) (Snapshot, error)
}
