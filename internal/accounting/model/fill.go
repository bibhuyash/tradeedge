package model

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

var (
	ErrInvalidAccountingFill = errors.New("invalid accounting fill")
	ErrUnorderableFill       = errors.New("accounting fill lacks canonical ordering evidence")
)

type FillOrderingKey struct {
	OccurredAt time.Time
	ReceivedAt time.Time
	FillID     executionmodel.FillID
}

func NewFillOrderingKey(occurredAt, receivedAt time.Time, fillID executionmodel.FillID) (FillOrderingKey, error) {
	if occurredAt.IsZero() || receivedAt.IsZero() || fillID.IsZero() {
		return FillOrderingKey{}, ErrUnorderableFill
	}
	return FillOrderingKey{OccurredAt: occurredAt.UTC(), ReceivedAt: receivedAt.UTC(), FillID: fillID}, nil
}

func (value FillOrderingKey) Compare(other FillOrderingKey) int {
	if value.OccurredAt.Before(other.OccurredAt) {
		return -1
	}
	if value.OccurredAt.After(other.OccurredAt) {
		return 1
	}
	if value.ReceivedAt.Before(other.ReceivedAt) {
		return -1
	}
	if value.ReceivedAt.After(other.ReceivedAt) {
		return 1
	}
	return strings.Compare(value.FillID.String(), other.FillID.String())
}

func (value FillOrderingKey) IsZero() bool {
	return value.OccurredAt.IsZero() || value.ReceivedAt.IsZero() || value.FillID.IsZero()
}

type AccountingFillSpec struct {
	SchemaVersion string
	Fill          executionmodel.Fill
	PortfolioID   portfoliomodel.PortfolioID
	InstrumentID  domain.InstrumentID
	Side          domain.Side
	ReceivedAt    time.Time
}

type AccountingFill struct {
	spec      AccountingFillSpec
	position  PositionID
	ordering  FillOrderingKey
	checksum  StateChecksum
	canonical []byte
}

func NewAccountingFill(spec AccountingFillSpec) (AccountingFill, error) {
	spec.SchemaVersion = strings.TrimSpace(spec.SchemaVersion)
	fillSpec := spec.Fill.Spec()
	if spec.SchemaVersion == "" || spec.Fill.ID().IsZero() || spec.PortfolioID.IsZero() ||
		spec.InstrumentID.IsZero() || (spec.Side != domain.SideBuy && spec.Side != domain.SideSell) ||
		spec.ReceivedAt.IsZero() || fillSpec.Price.IsZeroValue() || fillSpec.Price.MinorUnits() <= 0 ||
		!fillSpec.Quantity.IsValid() {
		return AccountingFill{}, ErrInvalidAccountingFill
	}
	ordering, err := NewFillOrderingKey(fillSpec.OccurredAt, spec.ReceivedAt, spec.Fill.ID())
	if err != nil {
		return AccountingFill{}, err
	}
	position, err := NewPositionID(spec.PortfolioID.String(), spec.InstrumentID.String())
	if err != nil {
		return AccountingFill{}, err
	}
	spec.ReceivedAt = spec.ReceivedAt.UTC()
	raw, err := json.Marshal(struct {
		SchemaVersion, PositionID, PortfolioID, InstrumentID, Side, ReceivedAt string
		Fill                                                                   json.RawMessage
	}{spec.SchemaVersion, position.String(), spec.PortfolioID.String(), spec.InstrumentID.String(), string(spec.Side), spec.ReceivedAt.Format(time.RFC3339Nano), spec.Fill.CanonicalJSON()})
	if err != nil {
		return AccountingFill{}, ErrInvalidAccountingFill
	}
	checksum, _ := NewStateChecksum("accounting-fill-checksum/v1", raw)
	return AccountingFill{spec: spec, position: position, ordering: ordering, checksum: checksum, canonical: raw}, nil
}

func (value AccountingFill) Spec() AccountingFillSpec     { return value.spec }
func (value AccountingFill) PositionID() PositionID       { return value.position }
func (value AccountingFill) OrderingKey() FillOrderingKey { return value.ordering }
func (value AccountingFill) Checksum() StateChecksum      { return value.checksum }
func (value AccountingFill) CanonicalJSON() []byte        { return append([]byte(nil), value.canonical...) }
func (value AccountingFill) IsZero() bool {
	return value.position.IsZero() || value.spec.Fill.ID().IsZero()
}
