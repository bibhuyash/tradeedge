package domain

import (
	"errors"
	"time"
)

var ErrInvalidOrderRequest = errors.New("invalid order request")

type MarketEvent struct {
	Instrument Instrument
	Price      Price
	ObservedAt time.Time
}

type Signal struct {
	StrategyID StrategyID
	Instrument Instrument
	Side       Side
	ObservedAt time.Time
}

type AllocationRequest struct {
	Signal    Signal
	AccountID AccountID
}

type Allocation struct {
	Request  AllocationRequest
	Quantity Quantity
}

type RiskDecisionStatus string

const (
	RiskApproved RiskDecisionStatus = "APPROVED"
	RiskRejected RiskDecisionStatus = "REJECTED"
)

type RiskDecision struct {
	Status RiskDecisionStatus
	Reason string
}

type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

type OrderState string

const (
	OrderCreated           OrderState = "CREATED"
	OrderRiskApproved      OrderState = "RISK_APPROVED"
	OrderSubmissionPending OrderState = "SUBMISSION_PENDING"
	OrderSubmitted         OrderState = "SUBMITTED"
	OrderAcknowledged      OrderState = "ACKNOWLEDGED"
	OrderPartiallyFilled   OrderState = "PARTIALLY_FILLED"
	OrderFilled            OrderState = "FILLED"
	OrderCancelPending     OrderState = "CANCEL_PENDING"
	OrderCancelled         OrderState = "CANCELLED"
	OrderRejected          OrderState = "REJECTED"
	OrderUnknown           OrderState = "UNKNOWN"
)

type OrderRequest struct {
	ClientRequestID ClientRequestID
	AccountID       AccountID
	StrategyID      StrategyID
	Instrument      Instrument
	Side            Side
	Quantity        Quantity
	LimitPrice      Price
}

func (r OrderRequest) Validate() error {
	if r.ClientRequestID == "" || r.AccountID == "" || r.StrategyID == "" ||
		r.Instrument.IsZero() || !r.Quantity.IsValid() || r.LimitPrice.IsZeroValue() ||
		(r.Side != SideBuy && r.Side != SideSell) {
		return ErrInvalidOrderRequest
	}
	return nil
}

type Order struct {
	ID        OrderID
	Request   OrderRequest
	State     OrderState
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Position struct {
	AccountID  AccountID
	Instrument Instrument
	Quantity   int64
}

type Notification struct {
	Subject       string
	Body          string
	CorrelationID string
}

type ReconciliationReport struct {
	AccountID  AccountID
	Consistent bool
	Details    string
	CheckedAt  time.Time
}
