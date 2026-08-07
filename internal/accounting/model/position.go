package model

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
	executionmodel "github.com/bibhuyash/tradeedge/internal/execution/model"
	portfoliomodel "github.com/bibhuyash/tradeedge/internal/portfolio/model"
)

var ErrInvalidPosition = errors.New("invalid accounting position")

type PositionState string

const (
	PositionOpenLong  PositionState = "OPEN_LONG"
	PositionOpenShort PositionState = "OPEN_SHORT"
	PositionFlat      PositionState = "FLAT"
)

type PositionLot struct {
	Side               domain.Side
	Quantity           OpenQuantity
	TotalBasis         CostBasis
	AverageNumerator   int64
	AverageDenominator int64
	OpenedAt           time.Time
}

type PositionSnapshotSpec struct {
	SchemaVersion            string
	PositionID               PositionID
	PortfolioID              portfoliomodel.PortfolioID
	InstrumentID             domain.InstrumentID
	Revision                 PositionRevision
	NetQuantity              NetQuantity
	OpenLot                  *PositionLot
	OpenCostBasis            CostBasis
	CumulativeBoughtQuantity int64
	CumulativeBoughtValue    domain.Money
	CumulativeSoldQuantity   int64
	CumulativeSoldValue      domain.Money
	GrossRealizedPnL         RealizedPnL
	AuthoritativeCharges     domain.Money
	ChargesAvailable         bool
	LastOrderingKey          FillOrderingKey
	LastFillID               executionmodel.FillID
	AppliedFillCount         uint64
	AppliedFillChecksum      StateChecksum
	OpenedAt                 time.Time
	UpdatedAt                time.Time
	FlatAt                   time.Time
}

type PositionSnapshot struct {
	spec      PositionSnapshotSpec
	state     PositionState
	checksum  StateChecksum
	canonical []byte
}

func NewPositionSnapshot(spec PositionSnapshotSpec) (PositionSnapshot, error) {
	spec.SchemaVersion = strings.TrimSpace(spec.SchemaVersion)
	if spec.SchemaVersion == "" || spec.PositionID.IsZero() || spec.PortfolioID.IsZero() || spec.InstrumentID.IsZero() ||
		spec.Revision.Validate() != nil || spec.CumulativeBoughtQuantity < 0 || spec.CumulativeSoldQuantity < 0 ||
		spec.CumulativeBoughtValue.IsZeroValue() || spec.CumulativeSoldValue.IsZeroValue() || spec.GrossRealizedPnL.IsZeroValue() ||
		spec.OpenCostBasis.IsZeroValue() || spec.AuthoritativeCharges.IsZeroValue() || spec.LastOrderingKey.IsZero() ||
		spec.LastFillID.IsZero() || spec.LastFillID != spec.LastOrderingKey.FillID || spec.AppliedFillCount == 0 ||
		spec.AppliedFillChecksum.IsZero() || spec.OpenedAt.IsZero() || spec.UpdatedAt.IsZero() {
		return PositionSnapshot{}, ErrInvalidPosition
	}
	currency := spec.OpenCostBasis.Currency()
	expectedPositionID, err := NewPositionID(spec.PortfolioID.String(), spec.InstrumentID.String())
	if err != nil || expectedPositionID != spec.PositionID {
		return PositionSnapshot{}, ErrInvalidPosition
	}
	for _, money := range []domain.Money{spec.CumulativeBoughtValue, spec.CumulativeSoldValue, spec.GrossRealizedPnL, spec.AuthoritativeCharges} {
		if money.Currency() != currency {
			return PositionSnapshot{}, domain.ErrCurrencyMismatch
		}
	}
	if spec.OpenCostBasis.MinorUnits() < 0 || spec.CumulativeBoughtValue.MinorUnits() < 0 || spec.CumulativeSoldValue.MinorUnits() < 0 ||
		spec.AuthoritativeCharges.MinorUnits() < 0 || spec.UpdatedAt.Before(spec.OpenedAt) {
		return PositionSnapshot{}, ErrInvalidPosition
	}
	state := PositionFlat
	if spec.NetQuantity > 0 {
		state = PositionOpenLong
	}
	if spec.NetQuantity < 0 {
		state = PositionOpenShort
	}
	if spec.NetQuantity == NetQuantity(math.MinInt64) {
		return PositionSnapshot{}, ErrInvalidPosition
	}
	if state == PositionFlat {
		if spec.OpenLot != nil || spec.OpenCostBasis.MinorUnits() != 0 || spec.FlatAt.IsZero() || spec.FlatAt.After(spec.UpdatedAt) {
			return PositionSnapshot{}, ErrInvalidPosition
		}
	} else {
		if spec.OpenLot == nil || spec.OpenCostBasis.MinorUnits() <= 0 || !spec.FlatAt.IsZero() || spec.OpenLot.OpenedAt != spec.OpenedAt ||
			spec.OpenLot.Quantity != OpenQuantity(abs(int64(spec.NetQuantity))) || spec.OpenLot.TotalBasis.MinorUnits() != spec.OpenCostBasis.MinorUnits() ||
			spec.OpenLot.TotalBasis.Currency() != currency || spec.OpenLot.AverageNumerator != spec.OpenCostBasis.MinorUnits() ||
			spec.OpenLot.AverageDenominator != abs(int64(spec.NetQuantity)) || spec.OpenLot.Quantity <= 0 {
			return PositionSnapshot{}, ErrInvalidPosition
		}
		expectedSide := domain.SideBuy
		if state == PositionOpenShort {
			expectedSide = domain.SideSell
		}
		if spec.OpenLot.Side != expectedSide {
			return PositionSnapshot{}, ErrInvalidPosition
		}
	}
	raw, err := canonicalPosition(spec, state)
	if err != nil {
		return PositionSnapshot{}, ErrInvalidPosition
	}
	checksum, _ := NewStateChecksum("accounting-position-state/v1", raw)
	return PositionSnapshot{spec: spec, state: state, checksum: checksum, canonical: raw}, nil
}

func canonicalPosition(spec PositionSnapshotSpec, state PositionState) ([]byte, error) {
	type moneyWire struct {
		Minor    int64  `json:"minor"`
		Currency string `json:"currency"`
	}
	money := func(value domain.Money) moneyWire { return moneyWire{value.MinorUnits(), value.Currency().String()} }
	type lotWire struct {
		Side                                 string
		Quantity                             int64
		TotalBasis                           moneyWire
		AverageNumerator, AverageDenominator int64
		OpenedAt                             string
	}
	var lot *lotWire
	if spec.OpenLot != nil {
		lot = &lotWire{string(spec.OpenLot.Side), spec.OpenLot.Quantity.Int64(), money(spec.OpenLot.TotalBasis), spec.OpenLot.AverageNumerator, spec.OpenLot.AverageDenominator, spec.OpenLot.OpenedAt.UTC().Format(time.RFC3339Nano)}
	}
	flat := ""
	if !spec.FlatAt.IsZero() {
		flat = spec.FlatAt.UTC().Format(time.RFC3339Nano)
	}
	return json.Marshal(struct {
		SchemaVersion, PositionID, PortfolioID, InstrumentID, State                                       string
		Revision                                                                                          uint64
		NetQuantity                                                                                       NetQuantity
		OpenLot                                                                                           *lotWire
		OpenCostBasis, CumulativeBoughtValue, CumulativeSoldValue, GrossRealizedPnL, AuthoritativeCharges moneyWire
		CumulativeBoughtQuantity, CumulativeSoldQuantity                                                  int64
		ChargesAvailable                                                                                  bool
		LastOccurredAt, LastReceivedAt, LastFillID                                                        string
		AppliedFillCount                                                                                  uint64
		AppliedFillChecksum, OpenedAt, UpdatedAt, FlatAt                                                  string
	}{spec.SchemaVersion, spec.PositionID.String(), spec.PortfolioID.String(), spec.InstrumentID.String(), string(state), uint64(spec.Revision), spec.NetQuantity, lot,
		money(spec.OpenCostBasis), money(spec.CumulativeBoughtValue), money(spec.CumulativeSoldValue), money(spec.GrossRealizedPnL), money(spec.AuthoritativeCharges),
		spec.CumulativeBoughtQuantity, spec.CumulativeSoldQuantity, spec.ChargesAvailable,
		spec.LastOrderingKey.OccurredAt.UTC().Format(time.RFC3339Nano), spec.LastOrderingKey.ReceivedAt.UTC().Format(time.RFC3339Nano), spec.LastFillID.String(),
		spec.AppliedFillCount, spec.AppliedFillChecksum.String(), spec.OpenedAt.UTC().Format(time.RFC3339Nano), spec.UpdatedAt.UTC().Format(time.RFC3339Nano), flat})
}

func abs(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
func (value PositionSnapshot) ID() PositionID             { return value.spec.PositionID }
func (value PositionSnapshot) Revision() PositionRevision { return value.spec.Revision }
func (value PositionSnapshot) State() PositionState       { return value.state }
func (value PositionSnapshot) Spec() PositionSnapshotSpec {
	result := value.spec
	if value.spec.OpenLot != nil {
		lot := *value.spec.OpenLot
		result.OpenLot = &lot
	}
	return result
}
func (value PositionSnapshot) Checksum() StateChecksum { return value.checksum }
func (value PositionSnapshot) CanonicalJSON() []byte   { return append([]byte(nil), value.canonical...) }
func (value PositionSnapshot) IsZero() bool            { return value.spec.PositionID.IsZero() }
