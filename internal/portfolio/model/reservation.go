package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bibhuyash/tradeedge/internal/domain"
)

var ErrInvalidCapitalReservation = errors.New("invalid capital reservation")

type CapitalReservationSpec struct {
	SchemaVersion        string
	ID                   CapitalReservationID
	PortfolioID          PortfolioID
	PortfolioRevision    PortfolioRevision
	CandidateID          AllocationCandidateID
	StrategyAllocationID StrategyAllocationID
	Amount               domain.Money
	CreatedAt            time.Time
	ExpiresAt            time.Time
}

type CapitalReservation struct {
	spec CapitalReservationSpec
	raw  []byte
}

func NewCapitalReservation(spec CapitalReservationSpec) (CapitalReservation, error) {
	spec.SchemaVersion = strings.TrimSpace(spec.SchemaVersion)
	if spec.SchemaVersion == "" || spec.ID.IsZero() || spec.PortfolioID.IsZero() ||
		spec.PortfolioRevision.Validate() != nil || spec.CandidateID.IsZero() ||
		spec.StrategyAllocationID.IsZero() || spec.Amount.IsZeroValue() ||
		spec.Amount.MinorUnits() <= 0 || spec.CreatedAt.IsZero() ||
		!spec.ExpiresAt.After(spec.CreatedAt) {
		return CapitalReservation{}, ErrInvalidCapitalReservation
	}
	spec.CreatedAt = spec.CreatedAt.UTC()
	spec.ExpiresAt = spec.ExpiresAt.UTC()
	raw, err := json.Marshal(struct {
		SchemaVersion, ID, PortfolioID, CandidateID, StrategyAllocationID string
		PortfolioRevision                                                 uint64
		AmountMinor                                                       int64
		Currency, CreatedAt, ExpiresAt                                    string
	}{
		SchemaVersion: spec.SchemaVersion, ID: spec.ID.String(), PortfolioID: spec.PortfolioID.String(),
		CandidateID: spec.CandidateID.String(), StrategyAllocationID: spec.StrategyAllocationID.String(),
		PortfolioRevision: uint64(spec.PortfolioRevision), AmountMinor: spec.Amount.MinorUnits(),
		Currency: spec.Amount.Currency().String(), CreatedAt: spec.CreatedAt.Format(time.RFC3339Nano),
		ExpiresAt: spec.ExpiresAt.Format(time.RFC3339Nano),
	})
	if err != nil {
		return CapitalReservation{}, fmt.Errorf("%w: %v", ErrInvalidCapitalReservation, err)
	}
	return CapitalReservation{spec: spec, raw: raw}, nil
}

func (value CapitalReservation) ID() CapitalReservationID     { return value.spec.ID }
func (value CapitalReservation) Spec() CapitalReservationSpec { return value.spec }
func (value CapitalReservation) CanonicalJSON() []byte        { return append([]byte(nil), value.raw...) }
