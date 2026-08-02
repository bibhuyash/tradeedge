package zerodha

import (
	"context"
	"errors"
	"time"
)

var (
	ErrMutationDisabled  = errors.New("zerodha mutation disabled")
	ErrDeliveryUnknown   = errors.New("zerodha request delivery unknown")
	ErrTransportRejected = errors.New("zerodha request rejected")
)

type DeliveryState string

const (
	DeliveryNotSent      DeliveryState = "NOT_SENT"
	DeliveryResponse     DeliveryState = "RESPONSE_RECEIVED"
	DeliveryPossiblySent DeliveryState = "POSSIBLY_SENT"
)

type OrderRequest struct {
	Tag             string
	TradingSymbol   string
	Exchange        string
	TransactionType string
	OrderType       string
	Product         string
	Validity        string
	Variety         string
	Quantity        int64
	Price           string
}

type PlaceResponse struct {
	BrokerOrderID string
	Rejected      bool
	Delivery      DeliveryState
}

type CancelResponse struct {
	Accepted bool
	Delivery DeliveryState
}

type BrokerOrder struct {
	BrokerOrderID   string
	Tag             string
	InstrumentToken string
	TradingSymbol   string
	Exchange        string
	TransactionType string
	OrderType       string
	Product         string
	Validity        string
	Variety         string
	Quantity        int64
	Price           string
	Status          string
	FilledQuantity  int64
	UpdatedAt       time.Time
}

type BrokerTrade struct {
	TradeID       string
	BrokerOrderID string
	Quantity      int64
	Price         string
	OccurredAt    time.Time
}

type OrderTransport interface {
	Place(context.Context, OrderRequest) (PlaceResponse, error)
	Cancel(context.Context, string, string) (CancelResponse, error)
	Orders(context.Context) ([]BrokerOrder, error)
	Trades(context.Context) ([]BrokerTrade, error)
	Close()
}

type MutationGate interface {
	AllowSubmission(context.Context) error
	AllowCancellation(context.Context) error
}

type DisabledMutationGate struct{}

func (DisabledMutationGate) AllowSubmission(context.Context) error   { return ErrMutationDisabled }
func (DisabledMutationGate) AllowCancellation(context.Context) error { return ErrMutationDisabled }

type PermitMutationGate struct {
	Submission   bool
	Cancellation bool
}

func (gate PermitMutationGate) AllowSubmission(context.Context) error {
	if !gate.Submission {
		return ErrMutationDisabled
	}
	return nil
}

func (gate PermitMutationGate) AllowCancellation(context.Context) error {
	if !gate.Cancellation {
		return ErrMutationDisabled
	}
	return nil
}
